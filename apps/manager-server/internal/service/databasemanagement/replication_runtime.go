package databasemanagement

import (
	"context"
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

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/outboxcontext"
)

const (
	replicationPollInterval = time.Second
	replicationMaxGroups    = 32
	reverseDirection        = "mysql_to_sqlite"
	mysqlReconnectInterval  = 5 * time.Second
	mysqlReconnectTimeout   = 8 * time.Second
)

func (r *Runtime) Start(ctx context.Context) {
	if ctx == nil {
		return
	}
	go r.runReplication(ctx)
	go r.runHistoryMigration(ctx)
	go r.runCacheCleanup(ctx)
	go r.runCacheRebuild(ctx)
	go r.runMySQLReconnect(ctx)
}

// runMySQLReconnect restores a configured backend that was unavailable during
// process startup. It never changes read/write routing; once the connection is
// back, the ordinary replication worker drains the durable Outbox.
func (r *Runtime) runMySQLReconnect(ctx context.Context) {
	ticker := time.NewTicker(mysqlReconnectInterval)
	defer ticker.Stop()
	for {
		r.reconnectConfiguredMySQLOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runtime) reconnectConfiguredMySQLOnce(ctx context.Context) {
	if ctx == nil || ctx.Err() != nil {
		return
	}
	r.backendMu.RLock()
	connected := r.mysql != nil && r.mysql.DB() != nil
	r.backendMu.RUnlock()
	if connected {
		return
	}
	state, err := r.loadState()
	if err != nil || state.MySQL.Host == "" {
		return
	}
	reconnectCtx, cancel := context.WithTimeout(ctx, mysqlReconnectTimeout)
	defer cancel()
	_ = r.ConnectConfiguredMySQL(reconnectCtx)
}

func (r *Runtime) runReplication(ctx context.Context) {
	ticker := time.NewTicker(replicationPollInterval)
	defer ticker.Stop()
	for {
		r.replicateOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Runtime) replicateOnce(ctx context.Context) {
	r.backgroundMu.RLock()
	defer r.backgroundMu.RUnlock()
	r.replicateOnceWhileBackgroundPaused(ctx)
}

// replicateOnceWhileBackgroundPaused applies one replication batch without
// acquiring backgroundMu. Final validation calls this only after it has taken
// the exclusive background/write fence; using replicateOnce there would
// recursively RLock the same RWMutex and deadlock whenever catch-up is needed.
func (r *Runtime) replicateOnceWhileBackgroundPaused(ctx context.Context) {
	state, err := r.loadState()
	if err != nil || !state.ReplicationEnabled {
		return
	}
	if state.WritePrimary == database.BackendMySQL {
		r.replicateMySQLToSQLite(ctx, state)
		return
	}
	r.replicateSQLiteToMySQL(ctx, state)
}

func (r *Runtime) replicateSQLiteToMySQL(ctx context.Context, state control.State) {
	sourceDB := r.sqliteDB()
	if sourceDB == nil {
		return
	}
	source := databasemigration.NewSQLRepository(sourceDB, databasemigration.DialectSQLite)
	rows, bytes, oldest, sourceWatermark, backlogErr := source.OutboxBacklog(ctx)
	if backlogErr != nil {
		return
	}
	targetWatermark := int64(0)
	retries := int64(0)
	if previous, previousErr := source.Replication(ctx, forwardDirection); previousErr == nil {
		targetWatermark = previous.TargetWatermark
		retries = previous.Retries
	}
	update := databasemigration.ReplicationUpdate{
		Direction: forwardDirection, Source: databasemigration.BackendSQLite,
		Target: databasemigration.BackendMySQL, Epoch: int64(state.RoutingEpoch),
		SourceWatermark: sourceWatermark, TargetWatermark: targetWatermark,
		BacklogRows: rows, BacklogBytes: bytes, OldestBacklogAtMS: oldest, Retries: retries,
	}
	started := time.Now()
	appliedRows := int64(0)
	mysqlDB, release := r.mysqlDB()
	defer release()
	if mysqlDB == nil {
		update.Retries++
		update.LastError = "mysql is not connected"
		_, _ = source.UpdateReplication(ctx, update)
		return
	}
	target := databasemigration.NewSQLRepository(mysqlDB, databasemigration.DialectMySQL)
	groups, err := source.PendingOutbox(ctx, replicationMaxGroups)
	if err != nil {
		update.Retries++
		update.LastError = err.Error()
		_, _ = source.UpdateReplication(ctx, update)
		return
	}
	for _, group := range groups {
		result, applyErr := target.ApplyInboxGroup(ctx, group, applyMySQLMutation)
		if applyErr != nil {
			update.Retries++
			update.LastError = applyErr.Error()
			break
		}
		if markErr := source.MarkOutboxApplied(ctx, group, time.Now()); markErr != nil {
			update.Retries++
			update.LastError = markErr.Error()
			break
		}
		update.TargetWatermark = max(update.TargetWatermark, result.Watermark)
		update.MadeProgress = true
		update.Succeeded = true
		appliedRows += int64(len(group.Mutations))
	}
	rows, bytes, oldest, sourceWatermark, err = source.OutboxBacklog(ctx)
	if err == nil {
		update.SourceWatermark = sourceWatermark
		update.BacklogRows = rows
		update.BacklogBytes = bytes
		update.OldestBacklogAtMS = oldest
	}
	elapsed := time.Since(started).Seconds()
	if elapsed > 0 {
		update.ThroughputRowsPerSec = float64(appliedRows) / elapsed
	}
	_, _ = source.UpdateReplication(ctx, update)
}

func (r *Runtime) replicateMySQLToSQLite(ctx context.Context, state control.State) {
	targetDB := r.sqliteDB()
	if targetDB == nil {
		return
	}
	target := databasemigration.NewSQLRepository(targetDB, databasemigration.DialectSQLite)
	update := databasemigration.ReplicationUpdate{
		Direction: reverseDirection, Source: databasemigration.BackendMySQL,
		Target: databasemigration.BackendSQLite, Epoch: int64(state.RoutingEpoch),
	}
	if previous, previousErr := target.Replication(ctx, reverseDirection); previousErr == nil {
		update.SourceWatermark = previous.SourceWatermark
		update.TargetWatermark = previous.TargetWatermark
		update.BacklogRows = previous.BacklogRows
		update.BacklogBytes = previous.BacklogBytes
		update.OldestBacklogAtMS = previous.OldestBacklogAtMS
		update.Retries = previous.Retries
	}
	mysqlDB, release := r.mysqlDB()
	defer release()
	if mysqlDB == nil {
		update.Retries++
		update.LastError = "mysql write primary is not connected"
		_, _ = target.UpdateReplication(ctx, update)
		return
	}
	source := databasemigration.NewSQLRepository(mysqlDB, databasemigration.DialectMySQL)
	rows, bytes, oldest, sourceWatermark, err := source.OutboxBacklog(ctx)
	if err != nil {
		update.Retries++
		update.LastError = err.Error()
		_, _ = target.UpdateReplication(ctx, update)
		return
	}
	update.SourceWatermark = sourceWatermark
	update.BacklogRows = rows
	update.BacklogBytes = bytes
	update.OldestBacklogAtMS = oldest
	groups, err := source.PendingOutbox(ctx, replicationMaxGroups)
	if err != nil {
		update.Retries++
		update.LastError = err.Error()
		_, _ = target.UpdateReplication(ctx, update)
		return
	}
	started := time.Now()
	appliedRows := int64(0)
	for _, group := range groups {
		apply, beginErr := outboxcontext.BeginReplicaApply(ctx, targetDB)
		if beginErr != nil {
			update.Retries++
			update.LastError = beginErr.Error()
			break
		}
		result, applyErr := apply.ApplyInboxGroup(ctx, target, group, applySQLiteMutation)
		if applyErr == nil {
			_, applyErr = apply.RefreshCacheCoverage(ctx, result.Watermark)
		}
		if applyErr == nil {
			applyErr = apply.Commit()
		} else {
			_ = apply.Rollback()
		}
		if applyErr != nil {
			update.Retries++
			update.LastError = applyErr.Error()
			break
		}
		if markErr := source.MarkOutboxApplied(ctx, group, time.Now()); markErr != nil {
			update.Retries++
			update.LastError = markErr.Error()
			break
		}
		update.TargetWatermark = max(update.TargetWatermark, result.Watermark)
		update.MadeProgress = true
		update.Succeeded = true
		appliedRows += int64(len(group.Mutations))
	}
	rows, bytes, oldest, sourceWatermark, err = source.OutboxBacklog(ctx)
	if err == nil {
		update.SourceWatermark = sourceWatermark
		update.BacklogRows = rows
		update.BacklogBytes = bytes
		update.OldestBacklogAtMS = oldest
	}
	if elapsed := time.Since(started).Seconds(); elapsed > 0 {
		update.ThroughputRowsPerSec = float64(appliedRows) / elapsed
	}
	_, _ = target.UpdateReplication(ctx, update)
	// Keep a copy on the MySQL control plane so recovery mode can expose the
	// same heartbeat even when SQLite later becomes unavailable.
	_, _ = source.UpdateReplication(ctx, update)
}

type journalValue struct {
	Type  string
	Value json.RawMessage
	Hex   string
}

func applyMySQLMutation(ctx context.Context, tx *sql.Tx, mutation databasemigration.Mutation) error {
	table, ok := authoritativeTable(mutation.Table)
	if !ok {
		return fmt.Errorf("outbox mutation references non-authoritative table %q", mutation.Table)
	}
	primaryKey, err := decodeJournalObject(mutation.PrimaryKey)
	if err != nil {
		return fmt.Errorf("decode %s primary key: %w", table.Name, err)
	}
	for _, column := range table.PrimaryKey() {
		if _, exists := primaryKey[column.Name]; !exists {
			return fmt.Errorf("outbox mutation omits primary key %s.%s", table.Name, column.Name)
		}
	}
	if mutation.Operation == databasemigration.OperationDelete {
		return deleteMySQLRow(ctx, tx, table, primaryKey)
	}
	if mutation.Operation != databasemigration.OperationInsert && mutation.Operation != databasemigration.OperationUpdate {
		return fmt.Errorf("unsupported outbox operation %q", mutation.Operation)
	}
	payload, err := decodeJournalObject(mutation.Payload)
	if err != nil {
		return fmt.Errorf("decode %s payload: %w", table.Name, err)
	}
	if len(payload) != len(table.Columns) {
		return fmt.Errorf("outbox payload for %s has %d fields, want all %d", table.Name, len(payload), len(table.Columns))
	}
	columns := make([]string, len(table.Columns))
	values := make([]any, len(table.Columns))
	updates := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		value, exists := payload[column.Name]
		if !exists {
			return fmt.Errorf("outbox payload omits %s.%s", table.Name, column.Name)
		}
		if value == nil && !column.Nullable {
			return fmt.Errorf("outbox payload changes non-null %s.%s to NULL", table.Name, column.Name)
		}
		columns[index] = mysqlQuote(column.Name)
		values[index] = value
		updates[index] = columns[index] + "=VALUES(" + columns[index] + ")"
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	statement := "INSERT INTO " + mysqlQuote(table.Name) + " (" + strings.Join(columns, ",") +
		") VALUES (" + placeholders + ") ON DUPLICATE KEY UPDATE " + strings.Join(updates, ",")
	if _, err := tx.ExecContext(ctx, statement, values...); err != nil {
		return fmt.Errorf("apply %s %s: %w", mutation.Operation, table.Name, err)
	}
	return nil
}

func deleteMySQLRow(ctx context.Context, tx *sql.Tx, table schema.Table, primaryKey map[string]any) error {
	predicates := make([]string, 0, len(table.PrimaryKey()))
	values := make([]any, 0, len(table.PrimaryKey()))
	for _, column := range table.PrimaryKey() {
		value := primaryKey[column.Name]
		if value == nil {
			predicates = append(predicates, mysqlQuote(column.Name)+" IS NULL")
			continue
		}
		predicates = append(predicates, mysqlQuote(column.Name)+" = ?")
		values = append(values, value)
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM "+mysqlQuote(table.Name)+" WHERE "+
		strings.Join(predicates, " AND "), values...)
	if err != nil {
		return err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr == nil && affected > 1 {
		return fmt.Errorf("delete for %s primary key affected %d rows", table.Name, affected)
	}
	return nil
}

func applySQLiteMutation(ctx context.Context, tx *sql.Tx, mutation databasemigration.Mutation) error {
	table, ok := authoritativeTable(mutation.Table)
	if !ok {
		return fmt.Errorf("reverse outbox mutation references non-authoritative table %q", mutation.Table)
	}
	primaryKey, err := decodeJournalObject(mutation.PrimaryKey)
	if err != nil {
		return fmt.Errorf("decode reverse %s primary key: %w", table.Name, err)
	}
	for _, column := range table.PrimaryKey() {
		if _, exists := primaryKey[column.Name]; !exists {
			return fmt.Errorf("reverse outbox mutation omits primary key %s.%s", table.Name, column.Name)
		}
	}
	if mutation.Operation == databasemigration.OperationDelete {
		return deleteSQLiteRow(ctx, tx, table, primaryKey)
	}
	if mutation.Operation != databasemigration.OperationInsert &&
		mutation.Operation != databasemigration.OperationUpdate {
		return fmt.Errorf("unsupported reverse outbox operation %q", mutation.Operation)
	}
	payload, err := decodeJournalObject(mutation.Payload)
	if err != nil {
		return fmt.Errorf("decode reverse %s payload: %w", table.Name, err)
	}
	if len(payload) != len(table.Columns) {
		return fmt.Errorf("reverse outbox payload for %s has %d fields, want all %d",
			table.Name, len(payload), len(table.Columns))
	}
	columns := make([]string, len(table.Columns))
	values := make([]any, len(table.Columns))
	primaryColumns := make([]string, 0, len(table.PrimaryKey()))
	primaryNames := make(map[string]bool, len(table.PrimaryKey()))
	for _, column := range table.PrimaryKey() {
		primaryColumns = append(primaryColumns, sqliteQuote(column.Name))
		primaryNames[column.Name] = true
	}
	updates := make([]string, 0, len(table.Columns)-len(primaryColumns))
	for index, column := range table.Columns {
		value, exists := payload[column.Name]
		if !exists {
			return fmt.Errorf("reverse outbox payload omits %s.%s", table.Name, column.Name)
		}
		if value == nil && !column.Nullable {
			return fmt.Errorf("reverse outbox payload changes non-null %s.%s to NULL", table.Name, column.Name)
		}
		columns[index] = sqliteQuote(column.Name)
		values[index] = value
		if !primaryNames[column.Name] {
			updates = append(updates, columns[index]+"=excluded."+columns[index])
		}
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	statement := "INSERT INTO " + sqliteQuote(table.Name) + " (" + strings.Join(columns, ",") +
		") VALUES (" + placeholders + ") ON CONFLICT (" + strings.Join(primaryColumns, ",") + ") "
	if len(updates) == 0 {
		statement += "DO NOTHING"
	} else {
		statement += "DO UPDATE SET " + strings.Join(updates, ",")
	}
	if _, err := tx.ExecContext(ctx, statement, values...); err != nil {
		return fmt.Errorf("apply reverse %s %s: %w", mutation.Operation, table.Name, err)
	}
	return nil
}

func deleteSQLiteRow(ctx context.Context, tx *sql.Tx, table schema.Table, primaryKey map[string]any) error {
	predicates := make([]string, 0, len(table.PrimaryKey()))
	values := make([]any, 0, len(table.PrimaryKey()))
	for _, column := range table.PrimaryKey() {
		value := primaryKey[column.Name]
		if value == nil {
			predicates = append(predicates, sqliteQuote(column.Name)+" IS NULL")
			continue
		}
		predicates = append(predicates, sqliteQuote(column.Name)+" = ?")
		values = append(values, value)
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM "+sqliteQuote(table.Name)+" WHERE "+
		strings.Join(predicates, " AND "), values...)
	if err != nil {
		return err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr == nil && affected > 1 {
		return fmt.Errorf("reverse delete for %s primary key affected %d rows", table.Name, affected)
	}
	return nil
}

func decodeJournalObject(encoded json.RawMessage) (map[string]any, error) {
	if len(encoded) == 0 || !json.Valid(encoded) {
		return nil, errors.New("journal object is not valid JSON")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return nil, err
	}
	result := make(map[string]any, len(raw))
	for name, encodedValue := range raw {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encodedValue, &fields); err != nil {
			return nil, err
		}
		var value journalValue
		if err := json.Unmarshal(fields["type"], &value.Type); err != nil {
			return nil, fmt.Errorf("%s has no type", name)
		}
		value.Value = fields["value"]
		_ = json.Unmarshal(fields["hex"], &value.Hex)
		decoded, err := decodeJournalValue(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		result[name] = decoded
	}
	return result, nil
}

func decodeJournalValue(value journalValue) (any, error) {
	switch value.Type {
	case "null":
		return nil, nil
	case "bytes":
		decoded, err := hex.DecodeString(value.Hex)
		if err != nil {
			return nil, errors.New("invalid hexadecimal byte payload")
		}
		return decoded, nil
	case "integer":
		var text string
		if err := json.Unmarshal(value.Value, &text); err != nil {
			return nil, errors.New("integer payload is not a string")
		}
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, errors.New("integer payload is outside int64")
		}
		return parsed, nil
	case "real":
		var text string
		if err := json.Unmarshal(value.Value, &text); err != nil {
			return nil, errors.New("real payload is not a string")
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return nil, errors.New("real payload is not a finite float64")
		}
		return parsed, nil
	case "text":
		var text string
		if err := json.Unmarshal(value.Value, &text); err != nil || !utf8.ValidString(text) {
			return nil, errors.New("text payload is not valid UTF-8")
		}
		return text, nil
	default:
		return nil, fmt.Errorf("unsupported journal value type %q", value.Type)
	}
}

func authoritativeTable(name string) (schema.Table, bool) {
	for _, table := range schema.Current().AuthoritativeTables() {
		if table.Name == name {
			return table, true
		}
	}
	return schema.Table{}, false
}
