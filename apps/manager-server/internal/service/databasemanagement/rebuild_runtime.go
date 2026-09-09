package databasemanagement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	cacheRebuildPollInterval = time.Second
	cacheRebuildBatchRows    = 1000
	cacheRebuildStallLimit   = 256
)

// The order is both the field-completeness contract and the FK-safe insert
// order. Keep it explicit: adding an authoritative manifest table without a
// deliberate cache policy must fail the plan audit below.
var sqliteCacheRebuildOrder = []string{
	"settings",
	"model_prices",
	"model_price_context_tiers",
	"model_price_service_tiers",
	"api_key_aliases",
	"account_action_candidates",
	"account_quota_observations",
	"account_quota_windows",
	"account_quota_window_activations",
	"account_quota_cycles",
	"account_quota_snapshots",
	"quota_cooldowns",
	"codex_inspection_runs",
	"codex_inspection_leases",
	"codex_inspection_logs",
	"codex_inspection_results",
	"codex_inspection_disable_ownership",
	"dead_letter_events",
	"usage_events",
	// This derived table is a permanent de-duplication tombstone. It remains
	// full-history even though the raw usage cache is windowed.
	"usage_event_identity_ledger",
}

var sqliteCacheRebuildInternalTables = []string{
	"database_routing_state",
	"database_migrations",
	"database_migration_tables",
	"database_inbox_sources",
	"database_row_versions",
	"database_replication_state",
	"database_cache_policy",
	"database_maintenance_tasks",
	"database_operation_idempotency",
}

type rebuildCopyPlan struct {
	Table string
	CTE   string
	Where string
	Args  []any
}

type rebuildTableSnapshot struct {
	Rows   int64
	MinKey string
	MaxKey string
	SHA256 string
}

type sqliteCacheRebuildState struct {
	path      string
	cutoffMS  int64
	coverage  databasemigration.CacheCoverage
	snapshots map[string]rebuildTableSnapshot
}

type sqliteCacheRebuilder struct {
	runtime            *Runtime
	expectedGeneration uint64
	expectedWatermark  int64
	now                func() time.Time
	built              *sqliteCacheRebuildState
}

func newSQLiteCacheRebuilder(r *Runtime, generation uint64, watermark int64) *sqliteCacheRebuilder {
	return &sqliteCacheRebuilder{runtime: r, expectedGeneration: generation,
		expectedWatermark: watermark, now: time.Now}
}

func (b *sqliteCacheRebuilder) RebuildTemporary(
	ctx context.Context,
	retentionDays int,
	synchronizedWatermark int64,
) (temporaryPath string, coverage databasemigration.CacheCoverage, err error) {
	if b == nil || b.runtime == nil {
		return "", coverage, errors.New("sqlite cache rebuilder requires a runtime")
	}
	if retentionDays < 1 || retentionDays > 3650 {
		return "", coverage, errors.New("sqlite cache retention must be between 1 and 3650 days")
	}
	if synchronizedWatermark < 0 || synchronizedWatermark != b.expectedWatermark {
		return "", coverage, errors.New("sqlite cache rebuild watermark changed before copy")
	}
	if b.runtime.sqliteDB() == nil {
		return "", coverage, errors.New("sqlite cache rebuild requires the live sqlite database")
	}
	mysqlDB, releaseMySQL := b.runtime.mysqlDB()
	defer releaseMySQL()
	if mysqlDB == nil {
		return "", coverage, errors.New("sqlite cache rebuild requires mysql")
	}
	mysqlValidation, err := schema.Validate(ctx, mysqlDB)
	if err != nil {
		return "", coverage, err
	}
	if !mysqlValidation.Valid {
		return "", coverage, fmt.Errorf("mysql schema differs from the canonical manifest: %v",
			mysqlValidation.Differences)
	}

	placeholder, err := os.CreateTemp(b.runtime.dataDir, ".sqlite-cache-rebuild-*.sqlite")
	if err != nil {
		return "", coverage, fmt.Errorf("create sqlite cache temporary file: %w", err)
	}
	temporaryPath = placeholder.Name()
	if err := placeholder.Close(); err != nil {
		b.cleanupTemporary(temporaryPath)
		return "", coverage, err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return "", coverage, fmt.Errorf("prepare sqlite cache temporary path: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			b.cleanupTemporary(temporaryPath)
		}
	}()

	target, err := sqliterepo.Open(temporaryPath)
	if err != nil {
		return "", coverage, fmt.Errorf("initialize temporary sqlite cache: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = target.Close()
		}
	}()
	targetRepository := databasemigration.NewSQLRepository(target, databasemigration.DialectSQLite)
	if err := targetRepository.EnsureSchema(ctx); err != nil {
		return "", coverage, err
	}
	if err := clearRebuildTables(ctx, target); err != nil {
		return "", coverage, err
	}

	cutoffMS := b.now().Add(-time.Duration(retentionDays) * 24 * time.Hour).UnixMilli()
	plans, err := sqliteCacheRebuildPlans(cutoffMS)
	if err != nil {
		return "", coverage, err
	}
	sourceTx, err := mysqlDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true,
		Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return "", coverage, fmt.Errorf("begin mysql cache snapshot: %w", err)
	}
	snapshots := make(map[string]rebuildTableSnapshot, len(plans))
	for _, plan := range plans {
		// The full-history identity ledger is copied after the retained raw
		// derivations are rebuilt. Aggregate catch-up legitimately mutates its
		// per-event derived markers; copying the authoritative MySQL ledger last
		// restores the exact de-duplication tombstones without copying any other
		// MySQL-derived table.
		if plan.Table == "usage_event_identity_ledger" {
			continue
		}
		table, exists := rebuildSchemaTable(plan.Table)
		if !exists {
			_ = sourceTx.Rollback()
			return "", coverage, fmt.Errorf("cache rebuild schema table %q is missing", plan.Table)
		}
		snapshot, copyErr := copyRebuildTable(ctx, sourceTx, target, table, plan)
		if copyErr != nil {
			_ = sourceTx.Rollback()
			return "", coverage, fmt.Errorf("copy mysql cache table %s: %w", plan.Table, copyErr)
		}
		snapshots[plan.Table] = snapshot
	}
	if err := sourceTx.Commit(); err != nil {
		return "", coverage, fmt.Errorf("finish mysql cache snapshot: %w", err)
	}

	if err := copySQLiteRebuildInternalState(ctx, b.runtime.sqliteDB(), target,
		synchronizedWatermark); err != nil {
		return "", coverage, fmt.Errorf("copy sqlite replication control state: %w", err)
	}
	if err := rebuildSQLiteDerived(ctx, target, b.now); err != nil {
		return "", coverage, fmt.Errorf("rebuild sqlite cache derivations: %w", err)
	}
	if _, err := target.ExecContext(ctx, `DELETE FROM usage_event_identity_ledger`); err != nil {
		return "", coverage, fmt.Errorf("replace temporary usage identity ledger: %w", err)
	}
	ledgerPlan := plans[len(plans)-1]
	if ledgerPlan.Table != "usage_event_identity_ledger" {
		return "", coverage, errors.New("sqlite cache rebuild identity ledger is not last in the fixed plan")
	}
	ledgerTable, _ := rebuildSchemaTable(ledgerPlan.Table)
	ledgerTx, err := mysqlDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true,
		Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return "", coverage, fmt.Errorf("begin mysql identity-ledger snapshot: %w", err)
	}
	ledgerSnapshot, err := copyRebuildTable(ctx, ledgerTx, target, ledgerTable, ledgerPlan)
	if err != nil {
		_ = ledgerTx.Rollback()
		return "", coverage, fmt.Errorf("copy mysql identity ledger: %w", err)
	}
	if err := ledgerTx.Commit(); err != nil {
		return "", coverage, fmt.Errorf("finish mysql identity-ledger snapshot: %w", err)
	}
	snapshots[ledgerPlan.Table] = ledgerSnapshot
	coverage, err = calculateRebuildCoverage(ctx, target, synchronizedWatermark)
	if err != nil {
		return "", coverage, err
	}
	currentCoverage, err := targetRepository.CacheCoverage(ctx)
	if err != nil {
		return "", coverage, err
	}
	coverage, err = targetRepository.SaveCacheCoverage(ctx, currentCoverage.UpdatedAtMS, coverage)
	if err != nil {
		return "", coverage, err
	}
	if err := checkpointSQLiteRebuild(ctx, target); err != nil {
		return "", coverage, err
	}
	if err := target.Close(); err != nil {
		return "", coverage, fmt.Errorf("close temporary sqlite cache: %w", err)
	}
	closed = true
	if err := syncFile(temporaryPath); err != nil {
		return "", coverage, err
	}
	b.built = &sqliteCacheRebuildState{path: temporaryPath, cutoffMS: cutoffMS,
		coverage: coverage, snapshots: snapshots}
	complete = true
	return temporaryPath, coverage, nil
}

func clearRebuildTables(ctx context.Context, target *sql.DB) error {
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for index := len(sqliteCacheRebuildOrder) - 1; index >= 0; index-- {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+sqliteQuote(sqliteCacheRebuildOrder[index])); err != nil {
			return fmt.Errorf("clear fresh cache table %s: %w", sqliteCacheRebuildOrder[index], err)
		}
	}
	return tx.Commit()
}

func sqliteCacheRebuildPlans(cutoffMS int64) ([]rebuildCopyPlan, error) {
	if cutoffMS <= 0 {
		return nil, errors.New("sqlite cache rebuild cutoff must be positive")
	}
	inspectionRuns := inspectionRebuildPredicate("src")
	snapshotPredicate := quotaSnapshotRebuildPredicate("src")
	windowPredicate := quotaWindowRebuildPredicate("src")
	activationPredicate := quotaActivationRebuildPredicate("src")
	cycleCTE, cycleArgs := quotaCycleRebuildCTE(cutoffMS)

	plans := []rebuildCopyPlan{
		{Table: "settings", Where: "1=1"},
		{Table: "model_prices", Where: "1=1"},
		{Table: "model_price_context_tiers", Where: "1=1"},
		{Table: "model_price_service_tiers", Where: "1=1"},
		{Table: "api_key_aliases", Where: "1=1"},
		{Table: "account_action_candidates", Where: `(src.updated_at_ms >= ? OR src.status='pending')`, Args: []any{cutoffMS}},
		{Table: "account_quota_observations", CTE: cycleCTE,
			Where: quotaObservationRebuildPredicate("src"),
			Args:  append(append([]any{}, cycleArgs...), repeatRebuildArg(cutoffMS, 10)...)},
		{Table: "account_quota_windows", CTE: cycleCTE, Where: windowPredicate,
			Args: append(append(append([]any{}, cycleArgs...), cutoffMS), cutoffMS, cutoffMS, cutoffMS)},
		{Table: "account_quota_window_activations", CTE: cycleCTE, Where: activationPredicate,
			Args: append(append(append([]any{}, cycleArgs...), cutoffMS), cutoffMS)},
		{Table: "account_quota_cycles", CTE: cycleCTE,
			Where: `src.id IN (SELECT id FROM selected_cycles)`, Args: append([]any{}, cycleArgs...)},
		{Table: "account_quota_snapshots", Where: snapshotPredicate, Args: []any{cutoffMS}},
		{Table: "quota_cooldowns", Where: `(COALESCE(src.recovered_at_ms,src.updated_at_ms) >= ? OR src.status='active')`, Args: []any{cutoffMS}},
		{Table: "codex_inspection_runs", Where: inspectionRuns, Args: []any{cutoffMS}},
		{Table: "codex_inspection_leases", Where: `src.run_id IN (SELECT r.id FROM codex_inspection_runs r WHERE ` + inspectionRebuildPredicate("r") + `)`, Args: []any{cutoffMS}},
		{Table: "codex_inspection_logs", Where: `src.run_id IN (SELECT r.id FROM codex_inspection_runs r WHERE ` + inspectionRebuildPredicate("r") + `)`, Args: []any{cutoffMS}},
		{Table: "codex_inspection_results", Where: `src.run_id IN (SELECT r.id FROM codex_inspection_runs r WHERE ` + inspectionRebuildPredicate("r") + `)`, Args: []any{cutoffMS}},
		{Table: "codex_inspection_disable_ownership", Where: "1=1"},
		{Table: "dead_letter_events", Where: `src.created_at_ms >= ?`, Args: []any{cutoffMS}},
		{Table: "usage_events", Where: `src.timestamp_ms >= ?`, Args: []any{cutoffMS}},
		{Table: "usage_event_identity_ledger", Where: "1=1"},
	}
	if err := auditSQLiteCacheRebuildPlans(plans); err != nil {
		return nil, err
	}
	return plans, nil
}

func quotaWindowRebuildPredicate(alias string) string {
	return `(
		COALESCE(` + alias + `.deactivated_at_ms,` + alias + `.last_seen_at_ms) >= ?
		OR ` + alias + `.availability <> 'inactive' OR ` + alias + `.deactivated_at_ms IS NULL
		OR EXISTS (SELECT 1 FROM account_quota_snapshots snapshot
			WHERE snapshot.logical_window_id=` + alias + `.id AND ` + quotaSnapshotRebuildPredicate("snapshot") + `)
		OR EXISTS (SELECT 1 FROM account_quota_window_activations activation
			WHERE activation.window_id=` + alias + `.id AND ` + quotaActivationRebuildPredicate("activation") + `))`
}

func quotaActivationRebuildPredicate(alias string) string {
	return `(
		COALESCE(` + alias + `.deactivated_at_ms,` + alias + `.activated_at_ms) >= ?
		OR ` + alias + `.status <> 'inactive' OR ` + alias + `.deactivated_at_ms IS NULL
		OR EXISTS (SELECT 1 FROM account_quota_snapshots snapshot
			WHERE snapshot.activation_id=` + alias + `.id AND ` + quotaSnapshotRebuildPredicate("snapshot") + `)
		OR ` + alias + `.id IN (SELECT activation_id FROM selected_cycles))`
}

func quotaObservationRebuildPredicate(alias string) string {
	return `(
		` + alias + `.observed_at_ms >= ? OR ` + alias + `.lifecycle_applied=0
		OR ` + alias + `.id IN (SELECT snapshot.observation_id FROM account_quota_snapshots snapshot
			WHERE snapshot.observation_id IS NOT NULL AND ` + quotaSnapshotRebuildPredicate("snapshot") + `)
		OR ` + alias + `.id IN (SELECT window.last_observation_id FROM account_quota_windows window
			WHERE window.last_observation_id IS NOT NULL AND ` + quotaWindowRebuildPredicate("window") + `)
		OR ` + alias + `.id IN (
			SELECT activation.activate_observation_id FROM account_quota_window_activations activation
				WHERE activation.activate_observation_id IS NOT NULL AND ` + quotaActivationRebuildPredicate("activation") + `
			UNION
			SELECT activation.deactivate_observation_id FROM account_quota_window_activations activation
				WHERE activation.deactivate_observation_id IS NOT NULL AND ` + quotaActivationRebuildPredicate("activation") + `)
		OR ` + alias + `.id IN (
			SELECT cycle.first_observation_id FROM account_quota_cycles cycle
				WHERE cycle.first_observation_id IS NOT NULL AND cycle.id IN (SELECT id FROM selected_cycles)
			UNION
			SELECT cycle.last_observation_id FROM account_quota_cycles cycle
				WHERE cycle.last_observation_id IS NOT NULL AND cycle.id IN (SELECT id FROM selected_cycles)))`
}

func repeatRebuildArg(value any, count int) []any {
	values := make([]any, count)
	for index := range values {
		values[index] = value
	}
	return values
}

func inspectionRebuildPredicate(alias string) string {
	return `(
		COALESCE(` + alias + `.finished_at_ms,` + alias + `.started_at_ms) >= ?
		OR ` + alias + `.status IN ('running','cancelling')
		OR EXISTS (SELECT 1 FROM codex_inspection_leases lease WHERE lease.run_id=` + alias + `.id)
		OR EXISTS (SELECT 1 FROM codex_inspection_results pending WHERE pending.run_id=` + alias + `.id
			AND COALESCE(pending.action_status,'') IN ('pending','needs_review')))`
}

func quotaSnapshotRebuildPredicate(alias string) string {
	return `(
		` + alias + `.observed_at_ms >= ?
		OR EXISTS (SELECT 1 FROM account_quota_observations observation
			WHERE observation.id=` + alias + `.observation_id AND (
				observation.lifecycle_applied=0 OR NOT EXISTS (
					SELECT 1 FROM account_quota_observations newer
					WHERE newer.account_key=observation.account_key
						AND newer.provider=observation.provider
						AND newer.inventory_scope_key=observation.inventory_scope_key
						AND newer.lifecycle_applied=1
						AND (newer.observed_at_ms>observation.observed_at_ms
							OR (newer.observed_at_ms=observation.observed_at_ms AND newer.id>observation.id)))))
		OR EXISTS (SELECT 1 FROM account_quota_windows window
			WHERE window.id=` + alias + `.logical_window_id
				AND (window.availability<>'inactive' OR window.deactivated_at_ms IS NULL))
		OR EXISTS (SELECT 1 FROM account_quota_window_activations activation
			WHERE activation.id=` + alias + `.activation_id
				AND (activation.status<>'inactive' OR activation.deactivated_at_ms IS NULL))
		OR EXISTS (SELECT 1 FROM account_quota_cycles cycle
			WHERE cycle.id=` + alias + `.cycle_id AND cycle.actual_end_ms IS NULL))`
}

func quotaCycleRebuildCTE(cutoffMS int64) (string, []any) {
	return `WITH RECURSIVE selected_cycles(id,parent_cycle_id,activation_id) AS (
		SELECT cycle.id,cycle.parent_cycle_id,cycle.activation_id
		FROM account_quota_cycles cycle WHERE
			COALESCE(cycle.actual_end_ms,cycle.actual_start_ms) >= ? OR cycle.actual_end_ms IS NULL
			OR EXISTS (SELECT 1 FROM account_quota_snapshots snapshot
				WHERE snapshot.cycle_id=cycle.id AND ` + quotaSnapshotRebuildPredicate("snapshot") + `)
		UNION
		SELECT parent.id,parent.parent_cycle_id,parent.activation_id
		FROM account_quota_cycles parent
		JOIN selected_cycles child ON child.parent_cycle_id=parent.id
	)`, []any{cutoffMS, cutoffMS}
}

func auditSQLiteCacheRebuildPlans(plans []rebuildCopyPlan) error {
	seen := make(map[string]bool, len(plans))
	for _, plan := range plans {
		if plan.Table == "" || seen[plan.Table] {
			return fmt.Errorf("sqlite cache rebuild has duplicate or empty table %q", plan.Table)
		}
		if placeholders := strings.Count(plan.CTE+" "+plan.Where, "?"); placeholders != len(plan.Args) {
			return fmt.Errorf("sqlite cache rebuild table %s has %d placeholders and %d arguments",
				plan.Table, placeholders, len(plan.Args))
		}
		seen[plan.Table] = true
	}
	for _, table := range schema.Current().AuthoritativeTables() {
		if !seen[table.Name] {
			return fmt.Errorf("sqlite cache rebuild policy omits authoritative table %s", table.Name)
		}
		delete(seen, table.Name)
	}
	if !seen["usage_event_identity_ledger"] {
		return errors.New("sqlite cache rebuild policy omits the full identity ledger")
	}
	delete(seen, "usage_event_identity_ledger")
	if len(seen) != 0 {
		return fmt.Errorf("sqlite cache rebuild policy contains non-contract tables: %v", seen)
	}
	return nil
}

func rebuildSchemaTable(name string) (schema.Table, bool) {
	for _, table := range schema.Current().Tables {
		if table.Name == name {
			return table, true
		}
	}
	return schema.Table{}, false
}

func copyRebuildTable(
	ctx context.Context,
	source *sql.Tx,
	target *sql.DB,
	table schema.Table,
	plan rebuildCopyPlan,
) (rebuildTableSnapshot, error) {
	columns := make([]string, len(table.Columns))
	columnNames := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		columns[index] = "src." + mysqlQuote(column.Name)
		columnNames[index] = column.Name
	}
	primaryKey := table.PrimaryKey()
	if len(primaryKey) == 0 {
		return rebuildTableSnapshot{}, fmt.Errorf("table %s has no primary key", table.Name)
	}
	order := make([]string, len(primaryKey))
	primaryIndexes := make([]int, len(primaryKey))
	for keyIndex, key := range primaryKey {
		order[keyIndex] = "src." + mysqlQuote(key.Name)
		primaryIndexes[keyIndex] = -1
		for columnIndex, column := range table.Columns {
			if column.Name == key.Name {
				primaryIndexes[keyIndex] = columnIndex
				break
			}
		}
	}
	query := strings.TrimSpace(plan.CTE)
	if query != "" {
		query += " "
	}
	query += "SELECT " + strings.Join(columns, ",") + " FROM " + mysqlQuote(table.Name) +
		" AS src WHERE " + plan.Where + " ORDER BY " + strings.Join(order, ",")
	rows, err := source.QueryContext(ctx, query, plan.Args...)
	if err != nil {
		return rebuildTableSnapshot{}, err
	}
	defer rows.Close()
	hasher, err := databasemigration.NewCanonicalHasher(columnNames)
	if err != nil {
		return rebuildTableSnapshot{}, err
	}
	var batch [][]any
	var batchBytes int64
	var minKey, maxKey string
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := insertSQLiteRebuildBatch(ctx, target, table, batch); err != nil {
			return err
		}
		batch = nil
		batchBytes = 0
		return nil
	}
	for rows.Next() {
		raw := make([]any, len(table.Columns))
		destinations := make([]any, len(raw))
		for index := range raw {
			destinations[index] = &raw[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return rebuildTableSnapshot{}, err
		}
		values := make([]any, len(raw))
		canonical := make([]databasemigration.CanonicalValue, len(raw))
		rowBytes := int64(0)
		for index, value := range raw {
			values[index], canonical[index], err = normalizeRebuildValue(table.Columns[index], value)
			if err != nil {
				return rebuildTableSnapshot{}, fmt.Errorf("%s.%s: %w", table.Name,
					table.Columns[index].Name, err)
			}
			rowBytes += rebuildValueBytes(values[index])
		}
		if err := hasher.AddRow(canonical); err != nil {
			return rebuildTableSnapshot{}, err
		}
		key := canonicalKey(canonical, primaryIndexes)
		if minKey == "" {
			minKey = key
		}
		maxKey = key
		if len(batch) > 0 && (len(batch) >= cacheRebuildBatchRows ||
			batchBytes+rowBytes > databasemigration.DefaultMaxBatchBytes) {
			if err := flush(); err != nil {
				return rebuildTableSnapshot{}, err
			}
		}
		batch = append(batch, values)
		batchBytes += rowBytes
	}
	if err := rows.Err(); err != nil {
		return rebuildTableSnapshot{}, err
	}
	if err := flush(); err != nil {
		return rebuildTableSnapshot{}, err
	}
	digest, count := hasher.Sum()
	return rebuildTableSnapshot{Rows: count, MinKey: minKey, MaxKey: maxKey,
		SHA256: digest}, nil
}

func normalizeRebuildValue(
	column schema.Column,
	value any,
) (any, databasemigration.CanonicalValue, error) {
	nullable := column.Nullable && column.PrimaryKeyPosition == 0
	if value == nil {
		if !nullable {
			return nil, databasemigration.CanonicalValue{}, errors.New("unexpected NULL")
		}
		return nil, databasemigration.NullValue(), nil
	}
	switch column.Kind {
	case schema.KindInteger:
		integer, err := databaseInt64(value)
		if err != nil {
			return nil, databasemigration.CanonicalValue{}, err
		}
		return integer, databasemigration.IntValue(integer), nil
	case schema.KindReal:
		real, err := databaseFloat64(value)
		if err != nil || math.IsNaN(real) || math.IsInf(real, 0) {
			return nil, databasemigration.CanonicalValue{}, errors.New("invalid finite float64")
		}
		return real, databasemigration.FloatValue(real), nil
	case schema.KindText:
		var text string
		switch typed := value.(type) {
		case string:
			text = typed
		case []byte:
			text = string(typed)
		default:
			return nil, databasemigration.CanonicalValue{}, fmt.Errorf("invalid text dynamic type %T", value)
		}
		if !utf8.ValidString(text) {
			return nil, databasemigration.CanonicalValue{}, errors.New("invalid UTF-8")
		}
		return text, databasemigration.StringValue(text), nil
	case schema.KindBlob:
		var blob []byte
		switch typed := value.(type) {
		case []byte:
			blob = append([]byte(nil), typed...)
		case string:
			blob = []byte(typed)
		default:
			return nil, databasemigration.CanonicalValue{}, fmt.Errorf("invalid blob dynamic type %T", value)
		}
		return blob, databasemigration.BytesValue(blob), nil
	default:
		return nil, databasemigration.CanonicalValue{}, fmt.Errorf("unsupported logical kind %q", column.Kind)
	}
}

func rebuildValueBytes(value any) int64 {
	switch typed := value.(type) {
	case nil:
		return 1
	case string:
		return int64(len(typed))
	case []byte:
		return int64(len(typed))
	default:
		return 8
	}
}

func insertSQLiteRebuildBatch(ctx context.Context, target *sql.DB, table schema.Table, batch [][]any) error {
	columns := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		columns[index] = sqliteQuote(column.Name)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	statement := `INSERT INTO ` + sqliteQuote(table.Name) + ` (` + strings.Join(columns, ",") +
		`) VALUES (` + placeholders + `)`
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	prepared, err := tx.PrepareContext(ctx, statement)
	if err != nil {
		return err
	}
	defer prepared.Close()
	for _, values := range batch {
		if _, err := prepared.ExecContext(ctx, values...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func copySQLiteRebuildInternalState(
	ctx context.Context,
	source *sql.DB,
	target *sql.DB,
	expectedWatermark int64,
) error {
	if source == nil || target == nil {
		return errors.New("sqlite internal state copy requires source and target databases")
	}
	var pending, watermark int64
	if err := source.QueryRowContext(ctx, `SELECT
		COUNT(CASE WHEN applied_at_ms=0 THEN 1 END),COALESCE(MAX(outbox_id),0)
		FROM database_outbox`).Scan(&pending, &watermark); err != nil {
		return err
	}
	if pending != 0 || watermark != expectedWatermark {
		return fmt.Errorf("%w: sqlite internal snapshot watermark is unsafe: pending=%d source=%d expected=%d",
			databasemigration.ErrCleanupUnsafe,
			pending, watermark, expectedWatermark)
	}
	for _, table := range sqliteCacheRebuildInternalTables {
		if _, err := copySQLiteInternalTable(ctx, source, target, table, "", nil); err != nil {
			return fmt.Errorf("copy sqlite internal table %s: %w", table, err)
		}
	}
	markerRows, err := copySQLiteInternalTable(ctx, source, target, "database_outbox",
		`outbox_id=?`, []any{expectedWatermark})
	if err != nil {
		return err
	}
	if expectedWatermark == 0 && markerRows != 0 || expectedWatermark > 0 && markerRows != 1 {
		return fmt.Errorf("sqlite outbox compaction marker rows=%d watermark=%d", markerRows,
			expectedWatermark)
	}
	var lastRowVersion int64
	if err := source.QueryRowContext(ctx, `SELECT last_row_version
		FROM database_authority_write_context WHERE id=1`).Scan(&lastRowVersion); err != nil {
		return err
	}
	_, err = target.ExecContext(ctx, `UPDATE database_authority_write_context SET
		enabled=0,active=0,transaction_id=NULL,source_epoch=0,started_at_ms=0,
		expires_at_ms=0,row_version_base=0,last_row_version=?,journal_suspended=0 WHERE id=1`,
		lastRowVersion)
	return err
}

func copySQLiteInternalTable(
	ctx context.Context,
	source *sql.DB,
	target *sql.DB,
	table string,
	where string,
	args []any,
) (int64, error) {
	allowed := table == "database_outbox"
	if !allowed {
		for _, candidate := range sqliteCacheRebuildInternalTables {
			if candidate == table {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		return 0, fmt.Errorf("internal cache table %q is not allowed", table)
	}
	sourceColumns, err := sqliteTableColumns(ctx, source, table)
	if err != nil {
		return 0, err
	}
	targetColumns, err := sqliteTableColumns(ctx, target, table)
	if err != nil {
		return 0, err
	}
	if strings.Join(sourceColumns, "\x00") != strings.Join(targetColumns, "\x00") {
		return 0, fmt.Errorf("internal table %s columns differ between live and temporary sqlite", table)
	}
	if _, err := target.ExecContext(ctx, `DELETE FROM `+sqliteQuote(table)); err != nil {
		return 0, err
	}
	quoted := make([]string, len(sourceColumns))
	for index, column := range sourceColumns {
		quoted[index] = sqliteQuote(column)
	}
	query := `SELECT ` + strings.Join(quoted, ",") + ` FROM ` + sqliteQuote(table)
	if strings.TrimSpace(where) != "" {
		query += ` WHERE ` + where
	}
	rows, err := source.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	statement := `INSERT INTO ` + sqliteQuote(table) + ` (` + strings.Join(quoted, ",") +
		`) VALUES (` + strings.TrimRight(strings.Repeat("?,", len(quoted)), ",") + `)`
	var batch [][]any
	var copied int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		prepared, err := tx.PrepareContext(ctx, statement)
		if err != nil {
			return err
		}
		defer prepared.Close()
		for _, values := range batch {
			if _, err := prepared.ExecContext(ctx, values...); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		batch = nil
		return nil
	}
	for rows.Next() {
		values := make([]any, len(sourceColumns))
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return copied, err
		}
		for index, value := range values {
			if blob, ok := value.([]byte); ok {
				values[index] = append([]byte(nil), blob...)
			}
		}
		batch = append(batch, values)
		copied++
		if len(batch) >= cacheRebuildBatchRows {
			if err := flush(); err != nil {
				return copied, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return copied, err
	}
	return copied, flush()
}

func sqliteTableColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+sqliteQuote(table)+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, declaredType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("sqlite table %s does not exist", table)
	}
	return columns, nil
}

type rebuildDerivedStepResult struct {
	pending   bool
	processed int
	marker    string
}

func rebuildSQLiteDerived(ctx context.Context, db *sql.DB, now func() time.Time) error {
	if _, err := db.ExecContext(ctx, `UPDATE usage_event_identity_ledger SET
		aggregate_schema_version=0,aggregate_structure_revision=''
		WHERE raw_event_id IN (SELECT id FROM usage_events)`); err != nil {
		return fmt.Errorf("reset retained usage identity derivations: %w", err)
	}
	st := store.New(db)
	steps := []struct {
		name string
		run  func(context.Context, int64) (rebuildDerivedStepResult, error)
	}{
		{name: "usage hourly aggregate", run: func(ctx context.Context, nowMS int64) (rebuildDerivedStepResult, error) {
			result, err := st.CatchUpUsageHourlyAggregate(ctx, cacheRebuildBatchRows, nowMS)
			return rebuildDerivedStepResult{pending: result.Pending, processed: result.Processed,
				marker: fmt.Sprintf("%d/%d", result.CoverageEventID, result.TargetEventID)}, err
		}},
		{name: "usage pricing", run: func(ctx context.Context, nowMS int64) (rebuildDerivedStepResult, error) {
			result, err := st.CatchUpUsagePricing(ctx, cacheRebuildBatchRows, nowMS)
			return rebuildDerivedStepResult{pending: result.Pending || result.ContinueSoon,
				processed: result.Processed, marker: fmt.Sprintf("%d/%d/%t", result.CoverageEventID,
					result.TargetEventID, result.ContinueSoon)}, err
		}},
		{name: "usage monitoring projection", run: func(ctx context.Context, nowMS int64) (rebuildDerivedStepResult, error) {
			result, err := st.CatchUpUsageMonitoringProjection(ctx, cacheRebuildBatchRows, nowMS)
			return rebuildDerivedStepResult{pending: result.Pending || result.ContinueSoon,
				processed: result.Processed, marker: fmt.Sprintf("%d/%d/%t", result.CoverageEventID,
					result.TargetEventID, result.ContinueSoon)}, err
		}},
		{name: "usage monitoring metadata", run: func(ctx context.Context, nowMS int64) (rebuildDerivedStepResult, error) {
			result, err := st.CatchUpUsageMonitoringMetadata(ctx, cacheRebuildBatchRows, nowMS)
			return rebuildDerivedStepResult{pending: result.Pending || result.ContinueSoon,
				processed: result.Processed, marker: fmt.Sprintf("%d/%d/%t", result.CoverageEventID,
					result.TargetEventID, result.ContinueSoon)}, err
		}},
		{name: "usage monitoring stats", run: func(ctx context.Context, nowMS int64) (rebuildDerivedStepResult, error) {
			result, err := st.CatchUpUsageMonitoringStats(ctx, cacheRebuildBatchRows, nowMS)
			return rebuildDerivedStepResult{pending: result.Pending || result.ContinueSoon,
				processed: result.Processed, marker: fmt.Sprintf("%d/%d/%t", result.CoverageEventID,
					result.TargetEventID, result.ContinueSoon)}, err
		}},
		{name: "Codex legacy identity evidence", run: func(ctx context.Context, nowMS int64) (rebuildDerivedStepResult, error) {
			result, err := st.CatchUpCodexLegacyIdentityEvidence(ctx, cacheRebuildBatchRows, nowMS)
			return rebuildDerivedStepResult{pending: result.Pending || result.ContinueSoon,
				processed: result.Processed, marker: fmt.Sprintf("%d/%d/%t", result.CoverageEventID,
					result.TargetEventID, result.ContinueSoon)}, err
		}},
		{name: "account history", run: func(ctx context.Context, nowMS int64) (rebuildDerivedStepResult, error) {
			result, err := st.CatchUpAccountHistoryRollups(ctx, cacheRebuildBatchRows, nowMS)
			return rebuildDerivedStepResult{pending: result.Pending, processed: result.Processed,
				marker: fmt.Sprintf("%d/%d", result.LastEventID, result.RebuildTargetEventID)}, err
		}},
		{name: "dashboard hourly", run: func(ctx context.Context, nowMS int64) (rebuildDerivedStepResult, error) {
			result, err := st.CatchUpDashboardHourlyRollups(ctx, cacheRebuildBatchRows, nowMS)
			return rebuildDerivedStepResult{pending: result.Pending, processed: result.Processed,
				marker: fmt.Sprintf("%d/%d", result.LastEventID, result.RebuildTargetEventID)}, err
		}},
	}
	for _, step := range steps {
		lastMarker := ""
		stalled := 0
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			result, err := step.run(ctx, now().UnixMilli())
			if err != nil {
				return fmt.Errorf("%s: %w", step.name, err)
			}
			if !result.pending {
				break
			}
			if result.processed == 0 && result.marker == lastMarker {
				stalled++
			} else {
				stalled = 0
			}
			if stalled >= cacheRebuildStallLimit {
				return fmt.Errorf("%s made no progress for %d batches", step.name, stalled)
			}
			lastMarker = result.marker
		}
	}
	return nil
}

func calculateRebuildCoverage(
	ctx context.Context,
	db *sql.DB,
	watermark int64,
) (databasemigration.CacheCoverage, error) {
	coverage := databasemigration.CacheCoverage{Watermark: watermark, Complete: false,
		UpdatedAtMS: time.Now().UnixMilli()}
	err := db.QueryRowContext(ctx, `SELECT COALESCE(MIN(timestamp_ms),0),
		COALESCE(MAX(timestamp_ms),0),COALESCE(MIN(id),0),COALESCE(MAX(id),0)
		FROM usage_events`).Scan(&coverage.EarliestAtMS, &coverage.LatestAtMS,
		&coverage.EarliestID, &coverage.LatestID)
	return coverage, err
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open temporary sqlite cache for fsync: %w", err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("fsync temporary sqlite cache: %w", err)
	}
	return nil
}

func checkpointSQLiteRebuild(ctx context.Context, db *sql.DB) error {
	var busy, logFrames, checkpointed int64
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(
		&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint temporary sqlite cache: %w", err)
	}
	if busy != 0 || logFrames != checkpointed {
		return fmt.Errorf("temporary sqlite checkpoint incomplete: busy=%d log=%d checkpointed=%d",
			busy, logFrames, checkpointed)
	}
	return nil
}

func (b *sqliteCacheRebuilder) ValidateTemporary(
	ctx context.Context,
	temporaryPath string,
	coverage databasemigration.CacheCoverage,
) (validationErr error) {
	if b == nil || b.built == nil || b.built.path != temporaryPath ||
		!sameCacheCoverage(b.built.coverage, coverage) {
		b.cleanupTemporary(temporaryPath)
		return errors.New("temporary sqlite cache does not match the completed rebuild")
	}
	defer func() {
		if validationErr != nil {
			b.cleanupTemporary(temporaryPath)
			b.built = nil
		}
	}()
	target, err := sqliterepo.Open(temporaryPath)
	if err != nil {
		return err
	}
	defer target.Close()
	if err := databasemigration.ValidateSQLiteManifestParity(ctx, target,
		sqliteCacheRebuildValidationManifest()); err != nil {
		return fmt.Errorf("validate temporary sqlite schema: %w", err)
	}
	rows, err := target.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	foreignKeyErrors := 0
	for rows.Next() {
		foreignKeyErrors++
	}
	rowsErr := rows.Err()
	_ = rows.Close()
	if rowsErr != nil {
		return rowsErr
	}
	if foreignKeyErrors != 0 {
		return fmt.Errorf("temporary sqlite cache has %d foreign-key violations", foreignKeyErrors)
	}
	for tableName, sourceSnapshot := range b.built.snapshots {
		table, exists := rebuildSchemaTable(tableName)
		if !exists {
			return fmt.Errorf("validation manifest lost table %s", tableName)
		}
		targetSnapshot, err := sqliteRebuildTableSnapshot(ctx, target, table)
		if err != nil {
			return fmt.Errorf("validate temporary sqlite table %s: %w", tableName, err)
		}
		if sourceSnapshot != targetSnapshot {
			return fmt.Errorf("temporary sqlite table %s differs: source=%+v target=%+v",
				tableName, sourceSnapshot, targetSnapshot)
		}
	}
	var outsideWindow int64
	if err := target.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events
		WHERE timestamp_ms < ?`, b.built.cutoffMS).Scan(&outsideWindow); err != nil {
		return err
	}
	if outsideWindow != 0 {
		return fmt.Errorf("temporary sqlite cache contains %d usage rows outside retention", outsideWindow)
	}
	repository := databasemigration.NewSQLRepository(target, databasemigration.DialectSQLite)
	storedCoverage, err := repository.CacheCoverage(ctx)
	if err != nil {
		return err
	}
	if !sameCacheCoverage(storedCoverage, coverage) || storedCoverage.Complete {
		return fmt.Errorf("temporary sqlite coverage differs: stored=%+v expected=%+v",
			storedCoverage, coverage)
	}
	pending, _, _, watermark, err := repository.OutboxBacklog(ctx)
	if err != nil {
		return err
	}
	if pending != 0 || watermark != b.expectedWatermark {
		return fmt.Errorf("temporary sqlite outbox marker is unsafe: pending=%d watermark=%d expected=%d",
			pending, watermark, b.expectedWatermark)
	}
	if err := validateSQLiteRebuildDerived(ctx, target); err != nil {
		return err
	}
	return nil
}

func sqliteCacheRebuildValidationManifest() databasemigration.StaticManifest {
	manifest := migrationManifest()
	ledger, exists := rebuildSchemaTable("usage_event_identity_ledger")
	if !exists {
		panic("schema manifest omits usage_event_identity_ledger")
	}
	manifest = append(manifest, rebuildTableSpec(ledger))
	return manifest
}

func rebuildTableSpec(table schema.Table) databasemigration.TableSpec {
	spec := databasemigration.TableSpec{Name: table.Name}
	for _, column := range table.Columns {
		nullable := column.Nullable && column.PrimaryKeyPosition == 0
		spec.Columns = append(spec.Columns, databasemigration.ColumnSpec{Name: column.Name,
			LogicalType: string(column.Kind), Nullable: nullable, DefaultSQL: column.Default})
		if column.PrimaryKeyPosition > 0 {
			spec.PrimaryKey = append(spec.PrimaryKey, column.Name)
		}
	}
	return spec
}

func sqliteRebuildTableSnapshot(
	ctx context.Context,
	db *sql.DB,
	table schema.Table,
) (rebuildTableSnapshot, error) {
	columns := make([]string, len(table.Columns))
	columnNames := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		columns[index] = sqliteQuote(column.Name)
		columnNames[index] = column.Name
	}
	primaryKey := table.PrimaryKey()
	if len(primaryKey) == 0 {
		return rebuildTableSnapshot{}, fmt.Errorf("table %s has no primary key", table.Name)
	}
	order := make([]string, len(primaryKey))
	primaryIndexes := make([]int, len(primaryKey))
	for keyIndex, key := range primaryKey {
		order[keyIndex] = sqliteQuote(key.Name)
		primaryIndexes[keyIndex] = -1
		for columnIndex, column := range table.Columns {
			if column.Name == key.Name {
				primaryIndexes[keyIndex] = columnIndex
				break
			}
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT `+strings.Join(columns, ",")+` FROM `+
		sqliteQuote(table.Name)+` ORDER BY `+strings.Join(order, ","))
	if err != nil {
		return rebuildTableSnapshot{}, err
	}
	defer rows.Close()
	hasher, err := databasemigration.NewCanonicalHasher(columnNames)
	if err != nil {
		return rebuildTableSnapshot{}, err
	}
	var minKey, maxKey string
	for rows.Next() {
		raw := make([]any, len(table.Columns))
		destinations := make([]any, len(raw))
		for index := range raw {
			destinations[index] = &raw[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return rebuildTableSnapshot{}, err
		}
		canonical := make([]databasemigration.CanonicalValue, len(raw))
		for index, value := range raw {
			_, canonical[index], err = normalizeRebuildValue(table.Columns[index], value)
			if err != nil {
				return rebuildTableSnapshot{}, err
			}
		}
		if err := hasher.AddRow(canonical); err != nil {
			return rebuildTableSnapshot{}, err
		}
		key := canonicalKey(canonical, primaryIndexes)
		if minKey == "" {
			minKey = key
		}
		maxKey = key
	}
	if err := rows.Err(); err != nil {
		return rebuildTableSnapshot{}, err
	}
	digest, count := hasher.Sum()
	return rebuildTableSnapshot{Rows: count, MinKey: minKey, MaxKey: maxKey,
		SHA256: digest}, nil
}

func validateSQLiteRebuildDerived(ctx context.Context, db *sql.DB) error {
	var latestEventID int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM usage_events`).Scan(&latestEventID); err != nil {
		return err
	}
	type stateCheck struct {
		query string
		args  []any
		name  string
	}
	checks := []stateCheck{
		{name: "usage hourly aggregate", query: `SELECT COUNT(*) FROM usage_hourly_aggregate_state
			WHERE aggregate_name='hourly_core' AND status='ready' AND coverage_event_id>=?`, args: []any{latestEventID}},
		{name: "usage pricing", query: `SELECT COUNT(*) FROM usage_pricing_rollup_state
			WHERE rollup_name='pricing_v1' AND status='ready' AND coverage_event_id>=?`, args: []any{latestEventID}},
		{name: "monitoring stats", query: `SELECT COUNT(*) FROM usage_monitoring_rollup_state
			WHERE rollup_name='stats_v1' AND status='ready' AND coverage_event_id>=?`, args: []any{latestEventID}},
		{name: "monitoring metadata", query: `SELECT COUNT(*) FROM usage_monitoring_rollup_state
			WHERE rollup_name='metadata_v1' AND status='ready' AND coverage_event_id>=?`, args: []any{latestEventID}},
		{name: "monitoring projection", query: `SELECT COUNT(*) FROM usage_monitoring_rollup_state
			WHERE rollup_name='projection_v1' AND status='ready' AND coverage_event_id>=?`, args: []any{latestEventID}},
		{name: "Codex legacy identity evidence", query: `SELECT COUNT(*) FROM usage_monitoring_rollup_state
			WHERE rollup_name='codex_legacy_identity_v1' AND structure_revision='1'
			AND status='ready' AND coverage_event_id>=?`, args: []any{latestEventID}},
		{name: "account history", query: `SELECT COUNT(*) FROM usage_rollup_checkpoints
			WHERE name='account_history' AND last_event_id>=?`, args: []any{latestEventID}},
		{name: "dashboard hourly", query: `SELECT COUNT(*) FROM usage_rollup_checkpoints
			WHERE name='dashboard_hourly' AND last_event_id>=?`, args: []any{latestEventID}},
		{name: "monitoring search", query: `SELECT COUNT(*) FROM usage_monitoring_search_index_state
			WHERE id=1 AND ready=1`},
	}
	for _, check := range checks {
		var ready int
		if err := db.QueryRowContext(ctx, check.query, check.args...).Scan(&ready); err != nil {
			return fmt.Errorf("validate %s state: %w", check.name, err)
		}
		if ready != 1 {
			return fmt.Errorf("temporary sqlite %s is not ready through event %d", check.name,
				latestEventID)
		}
	}
	var usageRows, projectionRows int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_events`).Scan(&usageRows); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_monitoring_event_projection_v1`).Scan(&projectionRows); err != nil {
		return err
	}
	if usageRows != projectionRows {
		return fmt.Errorf("temporary sqlite monitoring projection rows=%d usage rows=%d",
			projectionRows, usageRows)
	}
	return nil
}

func sameCacheCoverage(left, right databasemigration.CacheCoverage) bool {
	return left.EarliestAtMS == right.EarliestAtMS && left.LatestAtMS == right.LatestAtMS &&
		left.EarliestID == right.EarliestID && left.LatestID == right.LatestID &&
		left.Watermark == right.Watermark && left.Complete == right.Complete
}

func (b *sqliteCacheRebuilder) AtomicReplaceUnderWriteFence(
	ctx context.Context,
	temporaryPath string,
	task databasemigration.MaintenanceTask,
) (finalized databasemigration.MaintenanceTask, replaceErr error) {
	if b == nil || b.runtime == nil || b.built == nil || b.built.path != temporaryPath {
		return finalized, errors.New("validated temporary sqlite cache is unavailable")
	}
	if b.runtime.acquireWriteFence == nil || b.runtime.replaceSQLiteCache == nil {
		b.cleanupTemporary(temporaryPath)
		return finalized, errors.New("sqlite cache replacement callback is not installed")
	}
	releaseFence, err := b.runtime.acquireWriteFence(ctx)
	if err != nil {
		b.cleanupTemporary(temporaryPath)
		return finalized, fmt.Errorf("acquire sqlite cache replacement fence: %w", err)
	}
	defer releaseFence()
	defer func() {
		if replaceErr != nil {
			b.cleanupTemporary(temporaryPath)
			b.built = nil
		}
	}()

	state, err := b.runtime.requireGeneration(b.expectedGeneration)
	if err != nil {
		return finalized, fmt.Errorf("%w: control generation changed before cache replacement: %v",
			databasemigration.ErrCleanupUnsafe, err)
	}
	if state.WritePrimary != database.BackendSQLite {
		return finalized, fmt.Errorf("%w: sqlite stopped being the write primary during cache rebuild",
			databasemigration.ErrCleanupUnsafe)
	}
	liveRepository, err := b.runtime.metadataRepository(ctx, true)
	if err != nil {
		return finalized, err
	}
	liveTask, err := liveRepository.MaintenanceTask(ctx, task.ID)
	if err != nil {
		return finalized, err
	}
	if liveTask.Generation != task.Generation || liveTask.Status != databasemigration.StatusRunning ||
		liveTask.Kind != databasemigration.TaskRebuild || liveTask.ValidationToken != task.ValidationToken {
		return finalized, errors.New("sqlite rebuild task changed before replacement")
	}
	safety, err := b.runtime.cacheCleanupSafety(ctx, state, liveRepository)
	if err != nil {
		return finalized, err
	}
	if err := databasemigration.EvaluateCleanupSafety(safety, b.now()); err != nil {
		return finalized, err
	}
	if safety.SynchronizedWatermark != b.expectedWatermark || safety.ValidationToken != task.ValidationToken {
		return finalized, fmt.Errorf("%w: sqlite rebuild validation became stale: source=%d expected=%d",
			databasemigration.ErrCleanupUnsafe,
			safety.SynchronizedWatermark, b.expectedWatermark)
	}

	temporaryDB, err := sqliterepo.Open(temporaryPath)
	if err != nil {
		return finalized, err
	}
	temporaryRepository := databasemigration.NewSQLRepository(temporaryDB,
		databasemigration.DialectSQLite)
	storedCoverage, err := temporaryRepository.CacheCoverage(ctx)
	if err == nil && !sameCacheCoverage(storedCoverage, b.built.coverage) {
		err = errors.New("temporary sqlite coverage changed after validation")
	}
	if err == nil {
		var pending, watermark int64
		pending, _, _, watermark, err = temporaryRepository.OutboxBacklog(ctx)
		if err == nil && (pending != 0 || watermark != b.expectedWatermark) {
			err = fmt.Errorf("temporary sqlite outbox marker changed: pending=%d watermark=%d",
				pending, watermark)
		}
	}
	if err == nil {
		finalized, err = temporaryRepository.SaveMaintenanceProgress(ctx, task.ID,
			task.Generation, "", task.ProcessedRows, true, nil)
	}
	if err == nil {
		err = checkpointSQLiteRebuild(ctx, temporaryDB)
	}
	closeErr := temporaryDB.Close()
	if err != nil {
		return databasemigration.MaintenanceTask{}, err
	}
	if closeErr != nil {
		return databasemigration.MaintenanceTask{}, closeErr
	}
	if err := syncFile(temporaryPath); err != nil {
		return databasemigration.MaintenanceTask{}, err
	}

	nextBackend, err := b.runtime.replaceSQLiteCache(ctx, temporaryPath,
		b.expectedGeneration, b.expectedWatermark)
	if err != nil {
		return databasemigration.MaintenanceTask{}, err
	}
	if nextBackend == nil || nextBackend.Kind() != database.BackendSQLite || nextBackend.DB() == nil {
		return databasemigration.MaintenanceTask{},
			errors.New("sqlite replacement callback returned an invalid backend")
	}
	if err := nextBackend.Ping(ctx); err != nil {
		return databasemigration.MaintenanceTask{},
			fmt.Errorf("new sqlite backend is unavailable after replacement: %w", err)
	}
	b.runtime.backendMu.Lock()
	b.runtime.sqlite = nextBackend
	b.runtime.backendMu.Unlock()
	b.built = nil
	return finalized, nil
}

func (b *sqliteCacheRebuilder) cleanupTemporary(path string) {
	if b == nil || b.runtime == nil || strings.TrimSpace(path) == "" {
		return
	}
	absolute, err := filepath.Abs(path)
	if err != nil || filepath.Dir(absolute) != b.runtime.dataDir ||
		!strings.HasPrefix(filepath.Base(absolute), ".sqlite-cache-rebuild-") ||
		filepath.Clean(absolute) == filepath.Clean(b.runtime.sqlitePath) {
		return
	}
	for _, candidate := range []string{absolute, absolute + "-wal", absolute + "-shm"} {
		_ = os.Remove(candidate)
	}
}

func (r *Runtime) runCacheRebuild(ctx context.Context) {
	ticker := time.NewTicker(cacheRebuildPollInterval)
	defer ticker.Stop()
	for {
		r.rebuildCacheOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runtime) rebuildCacheOnce(ctx context.Context) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if ctx.Err() != nil || r.sqliteDB() == nil {
		return
	}
	repository, err := r.metadataRepository(ctx, true)
	if err != nil {
		return
	}
	task, exists, err := repository.ActiveMaintenanceTask(ctx, databasemigration.TaskRebuild)
	if err != nil || !exists {
		return
	}
	if _, cleanupActive, cleanupErr := repository.ActiveMaintenanceTask(ctx,
		databasemigration.TaskCleanup); cleanupErr != nil || cleanupActive {
		if cleanupErr == nil && task.Status == databasemigration.StatusRunning {
			_, _ = repository.PauseMaintenanceTask(ctx, task.ID, task.Generation)
		}
		return
	}
	if r.acquireWriteFence == nil || r.replaceSQLiteCache == nil {
		if task.Status == databasemigration.StatusRunning {
			_, _ = repository.SaveMaintenanceProgress(ctx, task.ID, task.Generation, "",
				task.ProcessedRows, false,
				errors.New("sqlite cache replacement callback is not installed"))
		}
		return
	}
	state, err := r.loadState()
	if err != nil || state.WritePrimary != database.BackendSQLite {
		if task.Status == databasemigration.StatusRunning {
			_, _ = repository.PauseMaintenanceTask(ctx, task.ID, task.Generation)
		}
		return
	}
	safety, err := r.cacheCleanupSafety(ctx, state, repository)
	if err != nil {
		if task.Status == databasemigration.StatusRunning {
			_, _ = repository.PauseMaintenanceTask(ctx, task.ID, task.Generation)
		}
		return
	}
	if task.ValidationToken != safety.ValidationToken ||
		databasemigration.EvaluateCleanupSafety(safety, time.Now()) != nil {
		if task.Status == databasemigration.StatusRunning {
			_, _ = repository.PauseMaintenanceTask(ctx, task.ID, task.Generation)
		}
		return
	}
	if task.Status == databasemigration.StatusPaused {
		task, err = repository.ResumeMaintenanceTask(ctx, task.ID, task.Generation)
		if err != nil {
			return
		}
	}
	rebuilder := newSQLiteCacheRebuilder(r, state.Generation, safety.SynchronizedWatermark)
	manager := r.cacheManager(repository)
	_, _ = manager.RunRebuild(ctx, task.ID, task.Generation, safety, rebuilder)
}
