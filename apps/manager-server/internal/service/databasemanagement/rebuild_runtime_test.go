package databasemanagement

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestSQLiteCacheRebuildPlanCoversEveryAuthoritativeFieldTable(t *testing.T) {
	plans, err := sqliteCacheRebuildPlans(time.Now().Add(-15 * 24 * time.Hour).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != len(schema.Current().AuthoritativeTables())+1 {
		t.Fatalf("plans=%d authoritative=%d", len(plans), len(schema.Current().AuthoritativeTables()))
	}
	for _, table := range schema.Current().AuthoritativeTables() {
		found := false
		for _, plan := range plans {
			if plan.Table == table.Name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("authoritative table %s is not covered", table.Name)
		}
	}
}

func TestCopyRebuildUsagePreservesEveryManifestFieldAndWindow(t *testing.T) {
	ctx := context.Background()
	source := openRebuildTestSQLite(t, "source.sqlite")
	target := openRebuildTestSQLite(t, "target.sqlite")
	table, ok := rebuildSchemaTable("usage_events")
	if !ok {
		t.Fatal("usage_events missing from manifest")
	}
	cutoff := int64(1_800_000_000_000)
	longBody := strings.Repeat("长文本🙂", 3000)
	insertManifestRow(t, source, table, map[string]any{
		"id": 101, "event_hash": "rebuild-recent", "request_id": nil,
		"timestamp_ms": cutoff + 1, "timestamp": "2027-01-15T08:00:00Z",
		"model": "模型-🙂", "raw_json": `{"完整":true}`, "fail_body": longBody,
		"client_ip": "2001:db8::1", "x_forwarded_for": "203.0.113.8",
		"normalized_uncached_input_tokens": nil, "request_service_tier": "priority",
		"response_service_tier": "default",
	})
	insertManifestRow(t, source, table, map[string]any{
		"id": 7, "event_hash": "rebuild-old", "timestamp_ms": cutoff - 1,
		"timestamp": "2027-01-01T00:00:00Z", "model": "old",
	})
	if _, err := target.Exec(`DELETE FROM usage_events`); err != nil {
		t.Fatal(err)
	}
	tx, err := source.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := copyRebuildTable(ctx, tx, target, table,
		rebuildCopyPlan{Table: table.Name, Where: `src.timestamp_ms >= ?`, Args: []any{cutoff}})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if snapshot.Rows != 1 {
		t.Fatalf("copied rows=%d, want 1", snapshot.Rows)
	}
	targetSnapshot, err := sqliteRebuildTableSnapshot(ctx, target, table)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != targetSnapshot {
		t.Fatalf("snapshot mismatch source=%+v target=%+v", snapshot, targetSnapshot)
	}
	var gotBody, gotRaw, gotModel string
	var requestID, normalized sql.NullString
	if err := target.QueryRow(`SELECT fail_body,raw_json,model,request_id,
		CAST(normalized_uncached_input_tokens AS TEXT) FROM usage_events WHERE id=101`).Scan(
		&gotBody, &gotRaw, &gotModel, &requestID, &normalized); err != nil {
		t.Fatal(err)
	}
	if gotBody != longBody || gotRaw != `{"完整":true}` || gotModel != "模型-🙂" ||
		requestID.Valid || normalized.Valid {
		t.Fatalf("field preservation failed body=%d raw=%q model=%q request=%v normalized=%v",
			len(gotBody), gotRaw, gotModel, requestID, normalized)
	}
}

func TestCopySQLiteRebuildInternalStateCompactsAppliedOutboxWatermark(t *testing.T) {
	ctx := context.Background()
	source := openRebuildTestSQLite(t, "live.sqlite")
	target := openRebuildTestSQLite(t, "temporary.sqlite")
	for _, db := range []*sql.DB{source, target} {
		if err := databasemigration.NewSQLRepository(db,
			databasemigration.DialectSQLite).EnsureSchema(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := source.Exec(`INSERT INTO database_outbox (
		outbox_id,mutation_id,mutation_digest,transaction_id,sequence_no,source_backend,
		target_backend,source_epoch,table_name,mutation_operation,primary_key_json,
		payload_json,schema_version,row_version,payload_bytes,created_at_ms,applied_at_ms)
		VALUES (42,'m42','digest','tx42',0,'sqlite','mysql',1,'settings','upsert',
		'{"key":"x"}','{"key":"x","value":"y","updated_at_ms":1}',1,42000,64,1,2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`UPDATE database_authority_write_context SET last_row_version=42000 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := copySQLiteRebuildInternalState(ctx, source, target, 42); err != nil {
		t.Fatal(err)
	}
	repository := databasemigration.NewSQLRepository(target, databasemigration.DialectSQLite)
	pending, _, _, watermark, err := repository.OutboxBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 || watermark != 42 {
		t.Fatalf("pending=%d watermark=%d", pending, watermark)
	}
	var outboxRows, lastVersion int64
	if err := target.QueryRow(`SELECT COUNT(*) FROM database_outbox`).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if err := target.QueryRow(`SELECT last_row_version FROM database_authority_write_context
		WHERE id=1`).Scan(&lastVersion); err != nil {
		t.Fatal(err)
	}
	if outboxRows != 1 || lastVersion != 42000 {
		t.Fatalf("outbox rows=%d last row version=%d", outboxRows, lastVersion)
	}
}

func TestSQLiteCacheRebuildQuotaSelectionKeepsActiveParentClosure(t *testing.T) {
	ctx := context.Background()
	source := openRebuildTestSQLite(t, "quota-source.sqlite")
	target := openRebuildTestSQLite(t, "quota-target.sqlite")
	observation, _ := rebuildSchemaTable("account_quota_observations")
	window, _ := rebuildSchemaTable("account_quota_windows")
	activation, _ := rebuildSchemaTable("account_quota_window_activations")
	cycle, _ := rebuildSchemaTable("account_quota_cycles")
	old := int64(1000)
	cutoff := int64(1_800_000_000_000)
	insertManifestRow(t, source, observation, map[string]any{
		"id": 1, "observation_hash": "observation-1", "observed_at_ms": old,
		"lifecycle_applied": 1,
	})
	insertManifestRow(t, source, observation, map[string]any{
		"id": 2, "observation_hash": "observation-expired", "observed_at_ms": old,
		"lifecycle_applied": 1,
	})
	insertManifestRow(t, source, observation, map[string]any{
		"id": 3, "observation_hash": "observation-pending", "observed_at_ms": old,
		"lifecycle_applied": 0,
	})
	insertManifestRow(t, source, observation, map[string]any{
		"id": 4, "observation_hash": "observation-recent", "observed_at_ms": cutoff + 1,
		"lifecycle_applied": 1,
	})
	insertManifestRow(t, source, window, map[string]any{
		"id": 10, "account_key": "account-1", "provider": "codex",
		"provider_window_id": "window-1", "scope_fingerprint": "scope-1",
		"availability": "inactive", "last_seen_at_ms": old, "deactivated_at_ms": old,
		"last_observation_id": 1,
	})
	insertManifestRow(t, source, activation, map[string]any{
		"id": 20, "window_id": 10, "generation": 1, "status": "inactive",
		"activated_at_ms": old, "deactivated_at_ms": old,
		"activate_observation_id": 1, "deactivate_observation_id": 1,
	})
	insertManifestRow(t, source, cycle, map[string]any{
		"id": 30, "activation_id": 20, "provider_cycle_key": "parent",
		"state": "closed", "actual_start_ms": old, "actual_end_ms": old,
		"first_observation_id": 1, "last_observation_id": 1,
	})
	insertManifestRow(t, source, cycle, map[string]any{
		"id": 31, "activation_id": 20, "provider_cycle_key": "active-child",
		"state": "active", "actual_start_ms": old, "actual_end_ms": nil,
		"first_observation_id": 1, "last_observation_id": 1, "parent_cycle_id": 30,
	})
	for _, tableName := range []string{"account_quota_cycles", "account_quota_window_activations",
		"account_quota_windows", "account_quota_observations"} {
		if _, err := target.Exec(`DELETE FROM ` + sqliteQuote(tableName)); err != nil {
			t.Fatal(err)
		}
	}
	plans, err := sqliteCacheRebuildPlans(cutoff)
	if err != nil {
		t.Fatal(err)
	}
	planByTable := map[string]rebuildCopyPlan{}
	for _, plan := range plans {
		planByTable[plan.Table] = plan
	}
	tx, err := source.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []schema.Table{observation, window, activation, cycle} {
		if _, err := copyRebuildTable(ctx, tx, target, table, planByTable[table.Name]); err != nil {
			_ = tx.Rollback()
			t.Fatalf("copy %s: %v", table.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var observations, cycles, activations, windows int
	if err := target.QueryRow(`SELECT COUNT(*) FROM account_quota_observations`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := target.QueryRow(`SELECT COUNT(*) FROM account_quota_cycles`).Scan(&cycles); err != nil {
		t.Fatal(err)
	}
	if err := target.QueryRow(`SELECT COUNT(*) FROM account_quota_window_activations`).Scan(&activations); err != nil {
		t.Fatal(err)
	}
	if err := target.QueryRow(`SELECT COUNT(*) FROM account_quota_windows`).Scan(&windows); err != nil {
		t.Fatal(err)
	}
	if observations != 3 || cycles != 2 || activations != 1 || windows != 1 {
		t.Fatalf("closure observations=%d cycles=%d activations=%d windows=%d",
			observations, cycles, activations, windows)
	}
	var expiredObservations int
	if err := target.QueryRow(`SELECT COUNT(*) FROM account_quota_observations WHERE id=2`).Scan(
		&expiredObservations); err != nil {
		t.Fatal(err)
	}
	if expiredObservations != 0 {
		t.Fatal("expired unreferenced quota observation was copied into the recent cache")
	}
	foreignKeys, err := target.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer foreignKeys.Close()
	if foreignKeys.Next() {
		t.Fatal("quota closure left a foreign-key violation")
	}
}

func TestSQLiteCacheRebuildTrimsExpiredDeadLetters(t *testing.T) {
	ctx := context.Background()
	source := openRebuildTestSQLite(t, "dead-letter-source.sqlite")
	target := openRebuildTestSQLite(t, "dead-letter-target.sqlite")
	table, ok := rebuildSchemaTable("dead_letter_events")
	if !ok {
		t.Fatal("dead_letter_events missing from manifest")
	}
	cutoff := int64(1_800_000_000_000)
	insertManifestRow(t, source, table, map[string]any{
		"id": 1, "payload": "expired", "error": "old", "created_at_ms": cutoff - 1,
	})
	insertManifestRow(t, source, table, map[string]any{
		"id": 2, "payload": "recent", "error": "new", "created_at_ms": cutoff,
	})
	if _, err := target.Exec(`DELETE FROM dead_letter_events`); err != nil {
		t.Fatal(err)
	}
	plans, err := sqliteCacheRebuildPlans(cutoff)
	if err != nil {
		t.Fatal(err)
	}
	var plan rebuildCopyPlan
	for _, candidate := range plans {
		if candidate.Table == table.Name {
			plan = candidate
			break
		}
	}
	tx, err := source.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := copyRebuildTable(ctx, tx, target, table, plan); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var rows, recent int
	if err := target.QueryRow(`SELECT COUNT(*),COUNT(CASE WHEN id=2 THEN 1 END)
		FROM dead_letter_events`).Scan(&rows, &recent); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || recent != 1 {
		t.Fatalf("dead-letter cache rows=%d recent=%d, want 1/1", rows, recent)
	}
}

func TestRuntimeRebuildCacheFailsClosedWithoutReplacementCallback(t *testing.T) {
	runtime, _, _, _, state := openReadyCacheTaskRuntime(t, false)
	_, err := runtime.RebuildCache(context.Background(), CacheRebuildMutation{
		MutationControl: MutationControl{ExpectedGeneration: state.Generation,
			IdempotencyKey: "rebuild-without-replacement"},
		DangerousOperationConfirmation: confirmedOperation(state, database.BackendSQLite),
		RetentionDays:                  15,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("RebuildCache error=%v, want ErrUnavailable", err)
	}
}

func TestRuntimeRebuildCacheRepairsReplayAfterTaskCreationCrashWindow(t *testing.T) {
	runtime, source, _, _, state := openReadyCacheTaskRuntime(t, false)
	runtime.acquireWriteFence = func(context.Context) (func(), error) { return func() {}, nil }
	runtime.replaceSQLiteCache = func(context.Context, string, uint64, int64) (database.Backend, error) {
		return runtime.sqlite, nil
	}
	ctx := context.Background()
	request := CacheRebuildMutation{MutationControl: MutationControl{
		ExpectedGeneration: state.Generation, IdempotencyKey: "rebuild-crash-window",
	}, DangerousOperationConfirmation: confirmedOperation(state, database.BackendSQLite), RetentionDays: 15}
	if _, err := runtime.RebuildCache(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ExecContext(ctx, `DELETE FROM database_operation_idempotency
		WHERE idempotency_key=?`, request.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RebuildCache(ctx, request); err != nil {
		t.Fatalf("repair rebuild replay after task creation: %v", err)
	}
	var tasks, replayRows int
	if err := source.QueryRowContext(ctx, `SELECT COUNT(*) FROM database_maintenance_tasks
		WHERE idempotency_key=?`, request.IdempotencyKey).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if err := source.QueryRowContext(ctx, `SELECT COUNT(*) FROM database_operation_idempotency
		WHERE idempotency_key=?`, request.IdempotencyKey).Scan(&replayRows); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || replayRows != 1 {
		t.Fatalf("tasks=%d replay rows=%d, want one each", tasks, replayRows)
	}
}

func TestRebuildSQLiteDerivedProducesReadyWindowProjections(t *testing.T) {
	db := openRebuildTestSQLite(t, "derived.sqlite")
	table, _ := rebuildSchemaTable("usage_events")
	insertManifestRow(t, db, table, map[string]any{
		"id": 9001, "event_hash": "derived-rebuild", "timestamp_ms": int64(1_900_000_000_000),
		"timestamp": "2030-03-17T17:46:40Z", "model": "gpt-derived",
	})
	if err := rebuildSQLiteDerived(context.Background(), db, time.Now); err != nil {
		t.Fatal(err)
	}
	if err := validateSQLiteRebuildDerived(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}

func openRebuildTestSQLite(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertManifestRow(t *testing.T, db *sql.DB, table schema.Table, overrides map[string]any) {
	t.Helper()
	columns := make([]string, len(table.Columns))
	values := make([]any, len(table.Columns))
	for index, column := range table.Columns {
		columns[index] = sqliteQuote(column.Name)
		if value, exists := overrides[column.Name]; exists {
			values[index] = value
			continue
		}
		if column.Nullable && column.PrimaryKeyPosition == 0 {
			values[index] = nil
			continue
		}
		switch column.Kind {
		case schema.KindInteger:
			values[index] = int64(0)
		case schema.KindReal:
			values[index] = float64(0)
		case schema.KindText:
			values[index] = column.Name + "-value"
		case schema.KindBlob:
			values[index] = []byte(column.Name + "-value")
		default:
			t.Fatal(errors.New("unsupported manifest column kind"))
		}
	}
	query := `INSERT INTO ` + sqliteQuote(table.Name) + ` (` + strings.Join(columns, ",") +
		`) VALUES (` + strings.TrimRight(strings.Repeat("?,", len(columns)), ",") + `)`
	if _, err := db.Exec(query, values...); err != nil {
		t.Fatalf("insert %s: %v", table.Name, err)
	}
}
