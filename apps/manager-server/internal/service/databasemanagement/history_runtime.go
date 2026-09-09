package databasemanagement

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
)

const historyPollInterval = 25 * time.Millisecond

type sqliteHistorySource struct {
	db            *sql.DB
	maxBatchBytes int64
}

type mysqlHistoryTarget struct {
	db         *sql.DB
	repository *databasemigration.SQLRepository
	epoch      int64
}

type historyTableWatermark struct {
	MaxRowID int64 `json:"maxRowId"`
	Rows     int64 `json:"rows"`
}

func (r *Runtime) runHistoryMigration(ctx context.Context) {
	ticker := time.NewTicker(historyPollInterval)
	defer ticker.Stop()
	for {
		r.copyHistoryOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runtime) copyHistoryOnce(ctx context.Context) {
	r.backgroundMu.RLock()
	defer r.backgroundMu.RUnlock()
	state, err := r.loadState()
	if err != nil || state.Migration.ID == "" {
		return
	}
	sourceDB := r.sqliteDB()
	mysqlDB, release := r.mysqlDB()
	defer release()
	if sourceDB == nil || mysqlDB == nil {
		return
	}
	repository := databasemigration.NewSQLRepository(sourceDB, databasemigration.DialectSQLite)
	migration, err := repository.Migration(ctx, state.Migration.ID)
	if err != nil || migration.Status != databasemigration.StatusRunning {
		return
	}
	if migration.Phase == databasemigration.PhaseRebuildDerived {
		r.rebuildMySQLDerivedOnce(ctx, repository, migration, mysqlDB)
		return
	}
	if migration.Phase != databasemigration.PhaseCopyHistory {
		return
	}
	manifest := migrationManifest()
	ordered, err := databasemigration.OrderedTables(manifest)
	if err != nil {
		_, _ = repository.FailMigration(ctx, migration.ID, migration.Generation, err)
		return
	}
	copier := databasemigration.HistoryCopier{
		Manifest: manifest, Source: sqliteHistorySource{
			db: sourceDB, maxBatchBytes: databasemigration.DefaultMaxBatchBytes,
		},
		Target: mysqlHistoryTarget{
			db: mysqlDB, repository: databasemigration.NewSQLRepository(mysqlDB, databasemigration.DialectMySQL),
			epoch: int64(max(state.RoutingEpoch, 1)),
		},
		Progress: repository, MaxBatchBytes: databasemigration.DefaultMaxBatchBytes,
	}
	for _, table := range ordered {
		progress, exists, progressErr := repository.TableProgress(ctx, migration.ID, table.Name)
		if progressErr != nil {
			_, _ = repository.FailMigration(ctx, migration.ID, migration.Generation, progressErr)
			return
		}
		if exists && progress.Completed {
			continue
		}
		workCtx, finish := r.beginMigrationWork(
			ctx, migration.ID, databasemigration.PhaseCopyHistory, table.Name, historyMigrationBatchTimeout,
		)
		if currentErr := requireCurrentMigrationWork(workCtx, repository, migration.ID,
			databasemigration.PhaseCopyHistory, migration.Generation); currentErr != nil {
			finish()
			return
		}
		_, copyErr := copier.RunBatch(workCtx, migration.ID, table.Name, migration.Generation)
		finish()
		if copyErr != nil {
			if isSupersededMigrationWorkError(copyErr) {
				return
			}
			latest, latestErr := repository.Migration(ctx, migration.ID)
			if latestErr == nil {
				failed, failErr := repository.FailMigration(ctx, migration.ID, latest.Generation, copyErr)
				if failErr == nil {
					r.updateControlMigration(failed)
				}
			}
		}
		return
	}
	next, err := repository.AdvanceMigration(ctx, migration.ID, migration.Generation, databasemigration.PhaseRebuildDerived)
	if err != nil {
		return
	}
	r.updateControlMigration(next)
}

func (r *Runtime) updateControlMigration(migration databasemigration.Migration) {
	state, err := r.loadState()
	if err != nil || state.Migration.ID != migration.ID {
		return
	}
	_, _ = r.control.Update(state.Generation, func(current *control.State) error {
		if current.Migration.ID != migration.ID {
			return control.ErrGenerationConflict
		}
		current.Migration = control.MigrationRef{
			ID: migration.ID, Phase: string(migration.Phase), Status: string(migration.Status),
			ValidationToken: migration.ValidationToken,
		}
		return nil
	})
}

func migrationManifest() databasemigration.StaticManifest {
	current := schema.Current()
	authoritative := current.AuthoritativeTables()
	known := make(map[string]bool, len(authoritative))
	for _, table := range authoritative {
		known[table.Name] = true
	}
	result := make(databasemigration.StaticManifest, 0, len(authoritative))
	for _, table := range authoritative {
		spec := databasemigration.TableSpec{Name: table.Name}
		for _, column := range table.Columns {
			nullable := column.Nullable
			// The journaling contract treats every logical primary-key member as
			// non-null. SQLite's PRAGMA reports rowid-table TEXT primary keys as
			// nullable even though all application repositories require a stable
			// identity and MySQL primary/unique identities cannot safely apply a
			// mutation whose key is NULL.
			if column.PrimaryKeyPosition > 0 {
				nullable = false
			}
			spec.Columns = append(spec.Columns, databasemigration.ColumnSpec{
				Name: column.Name, Nullable: nullable,
				LogicalType: string(column.Kind), DefaultSQL: column.Default,
			})
			if column.PrimaryKeyPosition > 0 {
				spec.PrimaryKey = append(spec.PrimaryKey, column.Name)
			}
		}
		dependencies := map[string]bool{}
		for _, foreignKey := range table.ForeignKeys {
			if foreignKey.RefTable != table.Name && known[foreignKey.RefTable] && !dependencies[foreignKey.RefTable] {
				spec.Dependencies = append(spec.Dependencies, foreignKey.RefTable)
				dependencies[foreignKey.RefTable] = true
			}
		}
		result = append(result, spec)
	}
	return result
}

func (s sqliteHistorySource) CaptureWatermark(ctx context.Context, table databasemigration.TableSpec) (json.RawMessage, error) {
	var watermark historyTableWatermark
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(rowid),0),COUNT(*) FROM "+
		sqliteQuote(table.Name)).Scan(&watermark.MaxRowID, &watermark.Rows)
	if err != nil {
		return nil, err
	}
	return json.Marshal(watermark)
}

func (s sqliteHistorySource) ReadBatch(
	ctx context.Context,
	table databasemigration.TableSpec,
	checkpoint json.RawMessage,
	sourceWatermark json.RawMessage,
	limit int,
) (databasemigration.HistoryBatch, error) {
	if limit <= 0 {
		limit = databasemigration.DefaultBatchSize
	}
	var after int64
	if len(checkpoint) > 0 {
		if err := json.Unmarshal(checkpoint, &after); err != nil {
			return databasemigration.HistoryBatch{}, err
		}
	}
	watermark, err := decodeHistoryTableWatermark(sourceWatermark)
	if err != nil {
		return databasemigration.HistoryBatch{}, err
	}
	if watermark.MaxRowID == 0 || after >= watermark.MaxRowID {
		return databasemigration.HistoryBatch{NextCheckpoint: sourceWatermark, Done: true}, nil
	}
	columns := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		columns[index] = sqliteQuote(column.Name)
	}
	query := "SELECT rowid," + strings.Join(columns, ",") + " FROM " + sqliteQuote(table.Name) +
		" WHERE rowid > ? AND rowid <= ? ORDER BY rowid LIMIT ?"
	rows, err := s.db.QueryContext(ctx, query, after, watermark.MaxRowID, limit)
	if err != nil {
		return databasemigration.HistoryBatch{}, err
	}
	defer rows.Close()
	schemaTable, ok := authoritativeTable(table.Name)
	if !ok {
		return databasemigration.HistoryBatch{}, fmt.Errorf("unknown authoritative table %q", table.Name)
	}
	batch := databasemigration.HistoryBatch{}
	lastRowID := after
	batchBytes := int64(0)
	byteLimited := false
	for rows.Next() {
		values := make([]any, len(table.Columns))
		destinations := make([]any, len(values)+1)
		var rowID int64
		destinations[0] = &rowID
		for index := range values {
			destinations[index+1] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return databasemigration.HistoryBatch{}, err
		}
		if err := validateRowValues(schemaTable, values); err != nil {
			return databasemigration.HistoryBatch{}, err
		}
		primaryKey, err := journalPrimaryKey(schemaTable, values)
		if err != nil {
			return databasemigration.HistoryBatch{}, err
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return databasemigration.HistoryBatch{}, err
		}
		rowBytes := int64(len(encoded))
		if len(batch.Rows) > 0 && s.maxBatchBytes > 0 && batchBytes+rowBytes > s.maxBatchBytes {
			byteLimited = true
			break
		}
		batch.Rows = append(batch.Rows, databasemigration.AuthoritativeRow{
			Values: values, PrimaryKey: primaryKey, RowVersion: 0, Bytes: rowBytes,
		})
		batchBytes += rowBytes
		lastRowID = rowID
	}
	if err := rows.Err(); err != nil {
		return databasemigration.HistoryBatch{}, err
	}
	next, _ := json.Marshal(lastRowID)
	batch.NextCheckpoint = next
	batch.Done = !byteLimited && (len(batch.Rows) < limit || lastRowID >= watermark.MaxRowID)
	return batch, nil
}

func decodeHistoryTableWatermark(encoded json.RawMessage) (historyTableWatermark, error) {
	encoded = bytes.TrimSpace(encoded)
	if len(encoded) == 0 {
		return historyTableWatermark{}, errors.New("invalid history table watermark")
	}
	var watermark historyTableWatermark
	if len(encoded) > 0 && encoded[0] == '{' {
		if err := json.Unmarshal(encoded, &watermark); err == nil && watermark.MaxRowID >= 0 && watermark.Rows >= 0 {
			return watermark, nil
		}
	}
	// Migration records created by earlier builds persisted only MAX(rowid).
	if encoded[0] < '0' || encoded[0] > '9' {
		return historyTableWatermark{}, errors.New("invalid history table watermark")
	}
	var legacy int64
	if err := json.Unmarshal(encoded, &legacy); err != nil || legacy < 0 {
		return historyTableWatermark{}, errors.New("invalid history table watermark")
	}
	return historyTableWatermark{MaxRowID: legacy}, nil
}

func (t mysqlHistoryTarget) ApplyBatch(
	ctx context.Context,
	table databasemigration.TableSpec,
	_ json.RawMessage,
	rows []databasemigration.AuthoritativeRow,
) error {
	schemaTable, ok := authoritativeTable(table.Name)
	if !ok {
		return fmt.Errorf("unknown authoritative table %q", table.Name)
	}
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, row := range rows {
		digest := historyRowDigest(table.Name, row)
		accepted, err := t.repository.AcceptHistoricalRowVersion(ctx, tx, databasemigration.RowVersionRecord{
			Table: table.Name, PrimaryKey: row.PrimaryKey, SourceEpoch: t.epoch,
			RowVersion: row.RowVersion, MutationID: "history-" + digest[:32], MutationHash: digest,
		})
		if err != nil {
			return err
		}
		if !accepted {
			continue
		}
		if err := upsertMySQLValues(ctx, tx, schemaTable, row.Values); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func upsertMySQLValues(ctx context.Context, tx *sql.Tx, table schema.Table, values []any) error {
	if len(values) != len(table.Columns) {
		return fmt.Errorf("history row for %s has %d fields, want %d", table.Name, len(values), len(table.Columns))
	}
	columns := make([]string, len(table.Columns))
	updates := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		columns[index] = mysqlQuote(column.Name)
		updates[index] = columns[index] + "=VALUES(" + columns[index] + ")"
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	query := "INSERT INTO " + mysqlQuote(table.Name) + " (" + strings.Join(columns, ",") +
		") VALUES (" + placeholders + ") ON DUPLICATE KEY UPDATE " + strings.Join(updates, ",")
	_, err := tx.ExecContext(ctx, query, values...)
	return err
}

func historyRowDigest(table string, row databasemigration.AuthoritativeRow) string {
	hash := sha256.New()
	hash.Write([]byte(table))
	hash.Write([]byte{0})
	hash.Write(row.PrimaryKey)
	hash.Write([]byte{0})
	encoded, _ := json.Marshal(row.Values)
	hash.Write(encoded)
	return hex.EncodeToString(hash.Sum(nil))
}

func journalPrimaryKey(table schema.Table, values []any) (json.RawMessage, error) {
	result := map[string]any{}
	for index, column := range table.Columns {
		if column.PrimaryKeyPosition == 0 {
			continue
		}
		typed, err := encodeJournalValue(column, values[index])
		if err != nil {
			return nil, err
		}
		result[column.Name] = typed
	}
	return json.Marshal(result)
}

func encodeJournalValue(column schema.Column, value any) (map[string]any, error) {
	if value == nil {
		return map[string]any{"type": "null"}, nil
	}
	switch typed := value.(type) {
	case int64:
		return map[string]any{"type": "integer", "value": strconv.FormatInt(typed, 10)}, nil
	case float64:
		return map[string]any{"type": "real", "value": strconv.FormatFloat(typed, 'g', -1, 64)}, nil
	case string:
		return map[string]any{"type": "text", "value": typed}, nil
	case []byte:
		if column.Kind == schema.KindBlob {
			return map[string]any{"type": "bytes", "hex": hex.EncodeToString(typed)}, nil
		}
		return map[string]any{"type": "text", "value": string(typed)}, nil
	case bool:
		if typed {
			return map[string]any{"type": "integer", "value": "1"}, nil
		}
		return map[string]any{"type": "integer", "value": "0"}, nil
	default:
		return nil, fmt.Errorf("cannot encode %s primary key dynamic type %T", column.Name, value)
	}
}
