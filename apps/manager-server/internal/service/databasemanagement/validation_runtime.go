package databasemanagement

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/pricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	validationReplicationPoll  = 100 * time.Millisecond
	validationProgressInterval = 150 * time.Millisecond
)

type sqlValidationReader struct {
	source               *sql.DB
	target               *sql.DB
	priceBook            map[string]model.ModelPrice
	priceHash            string
	derivedConformanceOK bool
	tableTotals          map[string]int64
	progress             func(databasemigration.ValidationProgress)
}

func (r *Runtime) validateMigration(
	ctx context.Context,
	migration databasemigration.Migration,
	repository *databasemigration.SQLRepository,
) (databasemigration.Migration, error) {
	if (migration.Phase != databasemigration.PhaseValidate &&
		migration.Phase != databasemigration.PhaseReadyToCutover) ||
		migration.Status != databasemigration.StatusRunning {
		return databasemigration.Migration{}, errors.Join(ErrInvalidRequest,
			fmt.Errorf("migration validation requires phase %s or %s/running",
				databasemigration.PhaseValidate, databasemigration.PhaseReadyToCutover))
	}
	manifest := migrationManifest()
	authoritativeTables := make([]databasemigration.TableSpec, 0, len(manifest))
	for _, table := range manifest.Tables() {
		if !table.Derived {
			authoritativeTables = append(authoritativeTables, table)
		}
	}
	validationDone := r.beginValidationProgress(migration.ID, len(authoritativeTables)*2+4)
	defer validationDone()
	if r.acquireWriteFence == nil {
		return databasemigration.Migration{}, errors.Join(ErrUnavailable,
			errors.New("global authoritative write fence is not installed"))
	}
	releaseFence, err := r.acquireWriteFence(ctx)
	if err != nil {
		return databasemigration.Migration{}, fmt.Errorf("acquire final validation write fence: %w", err)
	}
	defer releaseFence()

	sourceDB := r.sqliteDB()
	if sourceDB == nil {
		return databasemigration.Migration{}, errors.Join(ErrUnavailable,
			errors.New("both sqlite and mysql are required for final validation"))
	}
	progress := func(update databasemigration.ValidationProgress) {
		r.updateValidationProgress(migration.ID, update)
	}
	progress(databasemigration.ValidationProgress{
		Stage:      databasemigration.ValidationStagePreparing,
		TotalSteps: len(authoritativeTables)*2 + 4,
	})
	finalWatermark, err := r.catchUpForValidation(ctx, repository, migration.ID)
	if err != nil {
		return databasemigration.Migration{}, err
	}
	targetDB, releaseTarget := r.mysqlDB()
	defer releaseTarget()
	if targetDB == nil {
		return databasemigration.Migration{}, errors.Join(ErrUnavailable,
			errors.New("mysql disconnected before final validation"))
	}
	// A migration can span application upgrades. Internal migration metadata
	// tables added by a newer build are not authoritative business data and are
	// therefore not copied by the history worker. Reconcile that additive,
	// idempotent schema before the strict manifest comparison so an existing
	// migration can be validated without requiring a destructive reinitialize.
	targetRepository := databasemigration.NewSQLRepository(targetDB, databasemigration.DialectMySQL)
	if err := targetRepository.EnsureSchema(ctx); err != nil {
		return databasemigration.Migration{}, fmt.Errorf("ensure mysql migration metadata before final validation: %w", err)
	}
	if err := databasemigration.ValidateSQLiteManifestParity(ctx, sourceDB, manifest); err != nil {
		return databasemigration.Migration{}, fmt.Errorf("validate sqlite authoritative schema: %w", err)
	}
	mysqlSchema, err := schema.Validate(ctx, targetDB)
	if err != nil {
		return databasemigration.Migration{}, err
	}
	if !mysqlSchema.Valid {
		return databasemigration.Migration{}, fmt.Errorf("mysql schema differs from manifest: %v", mysqlSchema.Differences)
	}
	priceBook, err := decodeFrozenPriceBook(migration)
	if err != nil {
		return databasemigration.Migration{}, err
	}
	tableTotals, err := r.validationTableTotals(ctx, repository, migration.ID, authoritativeTables)
	if err != nil {
		return databasemigration.Migration{}, err
	}
	reader := &sqlValidationReader{source: sourceDB, target: targetDB,
		priceBook: priceBook, priceHash: migration.FrozenPriceHash,
		derivedConformanceOK: r.derivedConformanceOK != nil && r.derivedConformanceOK(),
		tableTotals:          tableTotals, progress: progress}
	validator := databasemigration.Validator{Manifest: manifest, Reader: reader,
		NowMS: func() int64 { return time.Now().UnixMilli() }, Progress: progress}
	result, err := validator.Run(ctx, migration, finalWatermark)
	if err != nil {
		return databasemigration.Migration{}, err
	}
	validated, err := repository.RecordValidation(ctx, migration.ID, migration.Generation, result)
	if err != nil {
		return databasemigration.Migration{}, mapMigrationError(err)
	}
	return validated, nil
}

func (r *Runtime) validationTableTotals(
	ctx context.Context,
	repository *databasemigration.SQLRepository,
	migrationID string,
	tables []databasemigration.TableSpec,
) (map[string]int64, error) {
	totals := make(map[string]int64, len(tables))
	for _, table := range tables {
		progress, exists, err := repository.TableProgress(ctx, migrationID, table.Name)
		if err != nil {
			return nil, fmt.Errorf("read validation row total for %s: %w", table.Name, err)
		}
		if !exists {
			continue
		}
		watermark, err := decodeHistoryTableWatermark(progress.SourceWatermark)
		if err == nil && watermark.Rows > 0 {
			totals[table.Name] = watermark.Rows
		}
	}
	return totals, nil
}

func (r *Runtime) catchUpForValidation(
	ctx context.Context,
	source *databasemigration.SQLRepository,
	migrationID string,
) (int64, error) {
	for {
		r.updateValidationProgress(migrationID, databasemigration.ValidationProgress{
			Stage: databasemigration.ValidationStageCatchUp,
		})
		pending, _, _, watermark, err := source.OutboxBacklog(ctx)
		if err != nil {
			return 0, fmt.Errorf("read final sqlite outbox watermark: %w", err)
		}
		if pending == 0 {
			targetDB, release := r.mysqlDB()
			if targetDB == nil {
				release()
				return 0, errors.Join(ErrUnavailable, errors.New("mysql disconnected during final catch-up"))
			}
			applied, appliedErr := mysqlAppliedWatermark(ctx, targetDB)
			release()
			if appliedErr != nil {
				return 0, appliedErr
			}
			if applied == watermark {
				return watermark, nil
			}
		}
		// validateMigration owns the exclusive background/write fence. Execute
		// the catch-up batch directly so the replication worker remains paused
		// without recursively acquiring backgroundMu.
		r.replicateOnceWhileBackgroundPaused(ctx)
		timer := time.NewTimer(validationReplicationPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, fmt.Errorf("wait for final mysql catch-up: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func mysqlAppliedWatermark(ctx context.Context, db *sql.DB) (int64, error) {
	var watermark int64
	err := db.QueryRowContext(ctx, `SELECT watermark FROM database_inbox_sources WHERE source_backend=?`,
		databasemigration.BackendSQLite).Scan(&watermark)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read mysql applied outbox watermark: %w", err)
	}
	return watermark, nil
}

func decodeFrozenPriceBook(migration databasemigration.Migration) (map[string]model.ModelPrice, error) {
	if len(migration.FrozenPriceBook) == 0 || !json.Valid(migration.FrozenPriceBook) {
		return nil, errors.New("migration has no durable frozen price-book snapshot; start a new migration")
	}
	digest := sha256.Sum256(migration.FrozenPriceBook)
	if hex.EncodeToString(digest[:]) != migration.FrozenPriceHash {
		return nil, errors.New("frozen price-book snapshot hash differs from migration metadata")
	}
	var prices map[string]model.ModelPrice
	if err := json.Unmarshal(migration.FrozenPriceBook, &prices); err != nil {
		return nil, fmt.Errorf("decode frozen price book: %w", err)
	}
	return prices, nil
}

func (r *sqlValidationReader) TableSnapshot(
	ctx context.Context,
	side databasemigration.ValidationSide,
	table databasemigration.TableSpec,
) (databasemigration.TableSnapshot, error) {
	db, quote, err := r.side(side)
	if err != nil {
		return databasemigration.TableSnapshot{}, err
	}
	columns := make([]string, len(table.Columns))
	columnNames := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		columns[index] = quote(column.Name)
		columnNames[index] = column.Name
	}
	order := make([]string, len(table.PrimaryKey))
	for index, column := range table.PrimaryKey {
		order[index] = quote(column)
	}
	if len(order) == 0 {
		return databasemigration.TableSnapshot{}, fmt.Errorf("table %s has no primary key", table.Name)
	}
	totalRows := r.tableTotals[table.Name]
	lastProgress := time.Time{}
	reportRows := func(processed int64, force bool) {
		now := time.Now()
		if !force && processed%4096 != 0 && now.Sub(lastProgress) < validationProgressInterval {
			return
		}
		lastProgress = now
		if r.progress != nil {
			r.progress(databasemigration.ValidationProgress{
				Stage: databasemigration.ValidationStageTableSnapshot, Table: table.Name,
				Side: side, ProcessedRows: processed, TotalRows: totalRows,
			})
		}
	}
	reportRows(0, true)
	rows, err := db.QueryContext(ctx, "SELECT "+strings.Join(columns, ",")+" FROM "+quote(table.Name)+
		" ORDER BY "+strings.Join(order, ","))
	if err != nil {
		return databasemigration.TableSnapshot{}, err
	}
	defer rows.Close()
	hasher, err := databasemigration.NewCanonicalHasher(columnNames)
	if err != nil {
		return databasemigration.TableSnapshot{}, err
	}
	primaryIndexes := make([]int, len(table.PrimaryKey))
	for keyIndex, key := range table.PrimaryKey {
		primaryIndexes[keyIndex] = -1
		for columnIndex, column := range table.Columns {
			if column.Name == key {
				primaryIndexes[keyIndex] = columnIndex
				break
			}
		}
		if primaryIndexes[keyIndex] < 0 {
			return databasemigration.TableSnapshot{}, fmt.Errorf("table %s primary key %s is missing", table.Name, key)
		}
	}
	var minKey, maxKey string
	var scannedRows int64
	for rows.Next() {
		raw := make([]any, len(table.Columns))
		destinations := make([]any, len(raw))
		for index := range raw {
			destinations[index] = &raw[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return databasemigration.TableSnapshot{}, err
		}
		canonical := make([]databasemigration.CanonicalValue, len(raw))
		for index, value := range raw {
			canonical[index], err = canonicalDatabaseValue(table.Columns[index], value)
			if err != nil {
				return databasemigration.TableSnapshot{}, fmt.Errorf("%s.%s: %w", table.Name, table.Columns[index].Name, err)
			}
		}
		if err := hasher.AddRow(canonical); err != nil {
			return databasemigration.TableSnapshot{}, err
		}
		scannedRows++
		reportRows(scannedRows, false)
		key := canonicalKey(canonical, primaryIndexes)
		if minKey == "" {
			minKey = key
		}
		maxKey = key
	}
	if err := rows.Err(); err != nil {
		return databasemigration.TableSnapshot{}, err
	}
	reportRows(scannedRows, true)
	digest, count := hasher.Sum()
	foreignKeyErrors, err := r.foreignKeyErrors(ctx, side, table.Name)
	if err != nil {
		return databasemigration.TableSnapshot{}, err
	}
	return databasemigration.TableSnapshot{SchemaHash: databasemigration.ManifestSchemaHash(table),
		Rows: count, MinKey: minKey, MaxKey: maxKey, SHA256: digest,
		ForeignKeyErrors: foreignKeyErrors}, nil
}

func (r *sqlValidationReader) AggregateSnapshot(
	ctx context.Context,
	side databasemigration.ValidationSide,
	frozenPriceHash string,
) (databasemigration.AggregateValidation, error) {
	if frozenPriceHash != r.priceHash {
		return databasemigration.AggregateValidation{}, errors.New("validator price hash changed")
	}
	db, quote, err := r.side(side)
	if err != nil {
		return databasemigration.AggregateValidation{}, err
	}
	query := `SELECT model,requested_model,resolved_model,service_tier,input_tokens,output_tokens,
		reasoning_tokens,cached_tokens,cache_tokens,cache_read_tokens,cache_creation_tokens,
		normalized_total_input_tokens,normalized_cache_read_tokens,normalized_cache_creation_tokens,failed
		FROM ` + quote("usage_events") + ` ORDER BY id`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return databasemigration.AggregateValidation{}, err
	}
	defer rows.Close()
	var result databasemigration.AggregateValidation
	totalRows := r.tableTotals["usage_events"]
	lastProgress := time.Time{}
	reportRows := func(processed int64, force bool) {
		now := time.Now()
		if !force && processed%4096 != 0 && now.Sub(lastProgress) < validationProgressInterval {
			return
		}
		lastProgress = now
		if r.progress != nil {
			r.progress(databasemigration.ValidationProgress{
				Stage: databasemigration.ValidationStageAggregates, Table: "usage_events",
				Side: side, ProcessedRows: processed, TotalRows: totalRows,
			})
		}
	}
	reportRows(0, true)
	var scannedRows int64
	for rows.Next() {
		var modelName string
		var requested, resolved, serviceTier sql.NullString
		var input, output, reasoning, cached, cache, cacheRead, cacheCreation int64
		var normalizedInput, normalizedRead, normalizedCreation sql.NullInt64
		var failed int64
		if err := rows.Scan(&modelName, &requested, &resolved, &serviceTier, &input, &output,
			&reasoning, &cached, &cache, &cacheRead, &cacheCreation, &normalizedInput,
			&normalizedRead, &normalizedCreation, &failed); err != nil {
			return databasemigration.AggregateValidation{}, err
		}
		effectiveInput := input
		if normalizedInput.Valid {
			effectiveInput = normalizedInput.Int64
		}
		effectiveRead := cacheRead
		if normalizedRead.Valid {
			effectiveRead = normalizedRead.Int64
		}
		effectiveCreation := cacheCreation
		if normalizedCreation.Valid {
			effectiveCreation = normalizedCreation.Int64
		}
		residualCached := max(max(cached, cache)-max(cacheRead, 0)-max(cacheCreation, 0), 0)
		result.InputTokens += effectiveInput
		result.OutputTokens += output
		result.ReasoningTokens += reasoning
		result.CachedTokens += residualCached + effectiveRead
		if failed == 0 {
			result.Successful++
		} else {
			result.Failed++
		}
		requestedModel := usageidentity.EffectiveRequestedModel(modelName, requested.String)
		analyticsModel := usageidentity.AnalyticsModel(requestedModel)
		billingModel := resolved.String
		if billingModel == "" {
			billingModel = analyticsModel
		}
		priceModel, price := validationPrice([]string{billingModel, analyticsModel, requestedModel}, r.priceBook)
		_, contextThreshold := model.ModelPriceForContext(price, effectiveInput)
		tokens := pricing.ModelTokens{PricingModel: priceModel, ContextThresholdTokens: contextThreshold,
			InputTokens: effectiveInput, OutputTokens: output, CachedTokens: residualCached,
			CacheReadTokens: effectiveRead, CacheCreationTokens: effectiveCreation}
		if effectiveInput > usage.LongContextInputTokenThreshold {
			tokens.LongInputTokens, tokens.LongOutputTokens = effectiveInput, output
			tokens.LongCachedTokens, tokens.LongCacheReadTokens = residualCached, effectiveRead
			tokens.LongCacheCreationTokens = effectiveCreation
		}
		result.Cost += pricing.CostForModelCandidatesWithServiceTier(
			[]string{billingModel, analyticsModel, requestedModel}, serviceTier.String, tokens, r.priceBook)
		scannedRows++
		reportRows(scannedRows, false)
	}
	reportRows(scannedRows, true)
	return result, rows.Err()
}

func validationPrice(candidates []string, prices map[string]model.ModelPrice) (string, model.ModelPrice) {
	for _, candidate := range candidates {
		if price, ok := prices[candidate]; ok {
			return candidate, price
		}
	}
	return "", model.ModelPrice{}
}

func (r *sqlValidationReader) AppliedOutboxWatermark(ctx context.Context) (int64, error) {
	return mysqlAppliedWatermark(ctx, r.target)
}

func (r *sqlValidationReader) DerivedDataReady(ctx context.Context) (bool, error) {
	if !r.derivedConformanceOK {
		return false, nil
	}
	manifest := schema.Current()
	for _, table := range manifest.Tables {
		if table.Class != schema.ClassDerived || table.SQLiteOnly {
			continue
		}
		var count int
		if err := r.target.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables
			WHERE table_schema=DATABASE() AND table_name=?`, table.Name).Scan(&count); err != nil {
			return false, err
		}
		if count != 1 {
			return false, nil
		}
	}
	var latestEvent, projectedEvent sql.NullInt64
	if err := r.target.QueryRowContext(ctx, `SELECT MAX(id) FROM usage_events`).Scan(&latestEvent); err != nil {
		return false, err
	}
	if err := r.target.QueryRowContext(ctx, `SELECT MAX(event_id) FROM usage_monitoring_event_projection_v1`).Scan(&projectedEvent); err != nil {
		return false, err
	}
	var searchReady int
	if err := r.target.QueryRowContext(ctx, `SELECT ready FROM usage_monitoring_search_index_state WHERE id=1`).Scan(&searchReady); err != nil {
		return false, err
	}
	return searchReady != 0 && latestEvent.Int64 == projectedEvent.Int64 &&
		latestEvent.Valid == projectedEvent.Valid, nil
}

func (r *sqlValidationReader) side(side databasemigration.ValidationSide) (*sql.DB, func(string) string, error) {
	switch side {
	case databasemigration.ValidationSourceSide:
		return r.source, sqliteQuote, nil
	case databasemigration.ValidationTargetSide:
		return r.target, mysqlQuote, nil
	default:
		return nil, nil, fmt.Errorf("unknown validation side %q", side)
	}
}

func canonicalDatabaseValue(column databasemigration.ColumnSpec, value any) (databasemigration.CanonicalValue, error) {
	if value == nil {
		if !column.Nullable {
			return databasemigration.CanonicalValue{}, errors.New("unexpected NULL")
		}
		return databasemigration.NullValue(), nil
	}
	switch strings.ToLower(column.LogicalType) {
	case "integer":
		integer, err := databaseInt64(value)
		if err != nil {
			return databasemigration.CanonicalValue{}, err
		}
		return databasemigration.IntValue(integer), nil
	case "real":
		float, err := databaseFloat64(value)
		if err != nil || math.IsNaN(float) || math.IsInf(float, 0) {
			return databasemigration.CanonicalValue{}, errors.New("invalid finite float64")
		}
		return databasemigration.FloatValue(float), nil
	case "text":
		var text string
		switch typed := value.(type) {
		case string:
			text = typed
		case []byte:
			text = string(typed)
		default:
			return databasemigration.CanonicalValue{}, fmt.Errorf("invalid text dynamic type %T", value)
		}
		if !utf8.ValidString(text) {
			return databasemigration.CanonicalValue{}, errors.New("invalid UTF-8")
		}
		return databasemigration.StringValue(text), nil
	case "blob":
		switch typed := value.(type) {
		case []byte:
			return databasemigration.BytesValue(typed), nil
		case string:
			return databasemigration.BytesValue([]byte(typed)), nil
		default:
			return databasemigration.CanonicalValue{}, fmt.Errorf("invalid blob dynamic type %T", value)
		}
	default:
		return databasemigration.CanonicalValue{}, fmt.Errorf("unsupported logical type %q", column.LogicalType)
	}
}

func databaseInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int32:
		return int64(typed), nil
	case int:
		return int64(typed), nil
	case uint64:
		if typed > math.MaxInt64 {
			return 0, errors.New("integer exceeds int64")
		}
		return int64(typed), nil
	case bool:
		if typed {
			return 1, nil
		}
		return 0, nil
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	case string:
		return strconv.ParseInt(typed, 10, 64)
	default:
		return 0, fmt.Errorf("invalid integer dynamic type %T", value)
	}
}

func databaseFloat64(value any) (float64, error) {
	switch typed := value.(type) {
	case float64:
		return typed, nil
	case float32:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case []byte:
		return strconv.ParseFloat(string(typed), 64)
	case string:
		return strconv.ParseFloat(typed, 64)
	default:
		return 0, fmt.Errorf("invalid real dynamic type %T", value)
	}
}

func canonicalKey(values []databasemigration.CanonicalValue, indexes []int) string {
	type keyValue struct {
		Kind  databasemigration.CanonicalKind `json:"kind"`
		Text  string                          `json:"text,omitempty"`
		Bytes string                          `json:"bytes,omitempty"`
	}
	key := make([]keyValue, len(indexes))
	for index, valueIndex := range indexes {
		value := values[valueIndex]
		key[index] = keyValue{Kind: value.Kind, Text: value.Text, Bytes: hex.EncodeToString(value.Bytes)}
	}
	encoded, _ := json.Marshal(key)
	return string(encoded)
}

func (r *sqlValidationReader) foreignKeyErrors(
	ctx context.Context,
	side databasemigration.ValidationSide,
	tableName string,
) (int64, error) {
	if side == databasemigration.ValidationSourceSide {
		rows, err := r.source.QueryContext(ctx, `PRAGMA foreign_key_check(`+sqliteQuote(tableName)+`)`)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		var count int64
		for rows.Next() {
			count++
		}
		return count, rows.Err()
	}
	table, ok := authoritativeTable(tableName)
	if !ok {
		return 0, fmt.Errorf("unknown authoritative table %s", tableName)
	}
	groups := map[string][]schema.ForeignKey{}
	for _, foreignKey := range table.ForeignKeys {
		groups[foreignKey.Name] = append(groups[foreignKey.Name], foreignKey)
	}
	var total int64
	for _, group := range groups {
		if len(group) == 0 {
			continue
		}
		joins := make([]string, len(group))
		nonNull := make([]string, len(group))
		for index, foreignKey := range group {
			joins[index] = "c." + mysqlQuote(foreignKey.Column) + "=p." + mysqlQuote(foreignKey.RefColumn)
			nonNull[index] = "c." + mysqlQuote(foreignKey.Column) + " IS NOT NULL"
		}
		query := "SELECT COUNT(*) FROM " + mysqlQuote(tableName) + " c LEFT JOIN " +
			mysqlQuote(group[0].RefTable) + " p ON " + strings.Join(joins, " AND ") +
			" WHERE " + strings.Join(nonNull, " AND ") + " AND p." + mysqlQuote(group[0].RefColumn) + " IS NULL"
		var count int64
		if err := r.target.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}
