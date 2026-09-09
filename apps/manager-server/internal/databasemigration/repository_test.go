package databasemigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestMigrationStateAndGenerationConflicts(t *testing.T) {
	repository, _ := openTestRepository(t)
	ctx := context.Background()
	migration, created, err := repository.CreateMigration(ctx, CreateMigrationRequest{
		IdempotencyKey: "start-1", ExpectedGeneration: 1, Source: BackendSQLite, Target: BackendMySQL,
		FrozenPriceHash: "prices-v1", FrozenPriceBook: json.RawMessage(`{}`),
	})
	if err != nil || !created {
		t.Fatalf("create migration: created=%v err=%v", created, err)
	}
	if _, err := repository.AdvanceMigration(ctx, migration.ID, migration.Generation, PhaseValidate); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("skip phases error = %v, want invalid transition", err)
	}
	paused, err := repository.PauseMigration(ctx, migration.ID, migration.Generation)
	if err != nil {
		t.Fatalf("pause migration: %v", err)
	}
	if _, err := repository.ResumeMigration(ctx, migration.ID, migration.Generation); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("stale resume error = %v, want generation conflict", err)
	}
	resumed, err := repository.ResumeMigration(ctx, migration.ID, paused.Generation)
	if err != nil || resumed.Status != StatusRunning {
		t.Fatalf("resume migration = %#v, err=%v", resumed, err)
	}
	canceled, err := repository.CancelMigration(ctx, migration.ID, resumed.Generation)
	if err != nil || canceled.Status != StatusCanceled {
		t.Fatalf("cancel migration = %#v, err=%v", canceled, err)
	}
	if _, err := repository.ResumeMigration(ctx, migration.ID, canceled.Generation); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("resume canceled error = %v, want invalid transition", err)
	}
}

func TestEnsureSchemaDisablesScheduledCacheCleanupByDefault(t *testing.T) {
	repository, _ := openTestRepository(t)
	ctx := context.Background()
	policy, err := repository.CachePolicy(ctx)
	if err != nil {
		t.Fatalf("read default cache policy: %v", err)
	}
	if policy.Enabled {
		t.Fatal("scheduled SQLite cache cleanup is enabled by default")
	}

	// EnsureSchema is idempotent and must not overwrite an administrator's
	// explicit choice on an existing installation.
	if _, err := repository.SetCachePolicy(ctx, policy.Generation, true,
		DefaultRetentionDays, DefaultBatchSize); err != nil {
		t.Fatalf("enable cache cleanup: %v", err)
	}
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("re-ensure migration schema: %v", err)
	}
	updated, err := repository.CachePolicy(ctx)
	if err != nil {
		t.Fatalf("read persisted cache policy: %v", err)
	}
	if !updated.Enabled {
		t.Fatal("EnsureSchema overwrote an explicit cache cleanup choice")
	}
}

func TestMigrationHistoryPreservesFailureAfterResume(t *testing.T) {
	repository, _ := openTestRepository(t)
	ctx := context.Background()
	migration, created, err := repository.CreateMigration(ctx, CreateMigrationRequest{
		ID: "migration-audit", IdempotencyKey: "audit-start", ExpectedGeneration: 1,
		Source: BackendSQLite, Target: BackendMySQL, FrozenPriceHash: "prices-v1",
		FrozenPriceBook: json.RawMessage(`{}`),
	})
	if err != nil || !created {
		t.Fatalf("create migration: created=%v err=%v", created, err)
	}
	migration, err = repository.AdvanceMigration(ctx, migration.ID, migration.Generation, PhaseCopyHistory)
	if err != nil {
		t.Fatalf("advance migration: %v", err)
	}
	migration, err = repository.FailMigration(ctx, migration.ID, migration.Generation,
		errors.New("mysql batch failed: visible detail"))
	if err != nil {
		t.Fatalf("fail migration: %v", err)
	}
	migration, err = repository.ResumeMigration(ctx, migration.ID, migration.Generation)
	if err != nil || migration.LastError != "" {
		t.Fatalf("resume migration = %#v, err=%v", migration, err)
	}

	migrations, err := repository.ListMigrations(ctx, 10)
	if err != nil || len(migrations) != 1 || migrations[0].ID != migration.ID {
		t.Fatalf("migration history = %#v, err=%v", migrations, err)
	}
	events, err := repository.MigrationEvents(ctx, migration.ID, 100)
	if err != nil {
		t.Fatalf("migration events: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("migration events = %#v, want created/phase/failed/resumed", events)
	}
	if events[2].EventType != "failed" || events[2].Error != "mysql batch failed: visible detail" ||
		events[3].EventType != "resumed" || events[3].Error != "" {
		t.Fatalf("migration failure audit was not retained: %#v", events)
	}
}

func TestMigrationHistoryRecordsOperationErrorWithoutChangingState(t *testing.T) {
	ctx := t.Context()
	repository, _ := openTestRepository(t)
	migration, _, err := repository.CreateMigration(ctx, CreateMigrationRequest{
		ID: "migration-operation-error", IdempotencyKey: "migration-operation-error",
		ExpectedGeneration: 1, Source: BackendSQLite, Target: BackendMySQL,
		FrozenPriceHash: "prices-v1", FrozenPriceBook: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("mysql schema differs from manifest: database_migration_events missing table")
	if err := repository.RecordMigrationError(ctx, migration.ID, "validate_failed", cause); err != nil {
		t.Fatal(err)
	}

	current, err := repository.Migration(ctx, migration.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Phase != migration.Phase || current.Status != migration.Status ||
		current.Generation != migration.Generation || current.LastError != migration.LastError {
		t.Fatalf("operation error changed migration state: before=%#v after=%#v", migration, current)
	}
	events, err := repository.MigrationEvents(ctx, migration.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].EventType != "validate_failed" || events[1].Error != cause.Error() {
		t.Fatalf("operation failure events = %#v", events)
	}
}

func TestReadyMigrationCanBeRevalidatedAfterItsWatermarkBecomesStale(t *testing.T) {
	ctx := t.Context()
	repository, _ := openTestRepository(t)
	migration, _, err := repository.CreateMigration(ctx, CreateMigrationRequest{
		ID: "migration-revalidate", IdempotencyKey: "migration-revalidate",
		ExpectedGeneration: 1, Source: BackendSQLite, Target: BackendMySQL,
		FrozenPriceHash: "prices-v1", FrozenPriceBook: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []MigrationPhase{PhaseCopyHistory, PhaseRebuildDerived, PhaseValidate} {
		migration, err = repository.AdvanceMigration(ctx, migration.ID, migration.Generation, phase)
		if err != nil {
			t.Fatalf("advance to %s: %v", phase, err)
		}
	}
	passing := ValidationResult{
		FinalOutboxWatermark: 8, AppliedWatermark: 8, DerivedDataReady: true,
		Tables: []TableValidation{{
			Table: "settings", Passed: true,
			SchemaHashSource: "schema", SchemaHashTarget: "schema",
			SourceRows: 1, TargetRows: 1, SourceMinKey: "a", TargetMinKey: "a",
			SourceMaxKey: "a", TargetMaxKey: "a", SourceSHA256: "fields", TargetSHA256: "fields",
		}},
	}
	migration, err = repository.RecordValidation(ctx, migration.ID, migration.Generation, passing)
	if err != nil || migration.Phase != PhaseReadyToCutover || migration.ValidationToken == "" {
		t.Fatalf("first validation = %#v, err=%v", migration, err)
	}

	passing.FinalOutboxWatermark = 9
	passing.AppliedWatermark = 9
	migration, err = repository.RecordValidation(ctx, migration.ID, migration.Generation, passing)
	if err != nil || migration.Phase != PhaseReadyToCutover || migration.FinalOutboxWatermark != 9 {
		t.Fatalf("revalidation = %#v, err=%v", migration, err)
	}

	failing := passing
	failing.AppliedWatermark = 8
	migration, err = repository.RecordValidation(ctx, migration.ID, migration.Generation, failing)
	if err != nil || migration.Phase != PhaseValidate || migration.ValidationToken != "" ||
		migration.LastError == "" {
		t.Fatalf("failed revalidation = %#v, err=%v", migration, err)
	}
}

func TestRoutingEpochFencesOldWriter(t *testing.T) {
	repository, _ := openTestRepository(t)
	ctx := context.Background()
	next, err := repository.CompareAndSwapRouting(ctx, 1, 1, RoutingChange{
		WritePrimary: BackendMySQL, BusinessRead: BackendMySQL, SystemRead: BackendSQLite,
	})
	if err != nil {
		t.Fatalf("switch write primary: %v", err)
	}
	if next.Generation != 2 || next.Epoch != 2 {
		t.Fatalf("routing after switch = %#v", next)
	}
	if err := repository.AssertWriteEpoch(ctx, BackendSQLite, 1); !errors.Is(err, ErrEpochFenced) {
		t.Fatalf("old writer error = %v, want fenced", err)
	}
	if _, err := repository.CompareAndSwapRouting(ctx, 1, 1, RoutingChange{
		WritePrimary: BackendSQLite, BusinessRead: BackendSQLite, SystemRead: BackendSQLite,
	}); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("stale routing CAS error = %v, want generation conflict", err)
	}
}

func TestOutboxInboxGroupIsAtomicAndIdempotent(t *testing.T) {
	repository, db := openTestRepository(t)
	ctx := context.Background()
	if _, err := db.Exec(`CREATE TABLE authoritative_rows (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE replicated_rows (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	group := fixtureMutationGroup()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoritative_rows(id, value) VALUES (1, 'complete')`); err != nil {
		t.Fatal(err)
	}
	if err := repository.AppendOutbox(ctx, tx, group); err != nil {
		t.Fatalf("append outbox: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	pending, err := repository.PendingOutbox(ctx, 1)
	if err != nil || len(pending) != 1 || len(pending[0].Mutations) != 2 {
		t.Fatalf("pending outbox = %#v, err=%v", pending, err)
	}
	applierCalls := 0
	applier := func(ctx context.Context, tx *sql.Tx, mutation Mutation) error {
		applierCalls++
		if mutation.Sequence == 0 {
			_, err := tx.ExecContext(ctx, `INSERT INTO replicated_rows(id, value) VALUES (1, 'complete')`)
			return err
		}
		return nil
	}
	first, err := repository.ApplyInboxGroup(ctx, pending[0], applier)
	if err != nil || !first.Applied || first.Watermark == 0 {
		t.Fatalf("first inbox apply = %#v, err=%v", first, err)
	}
	second, err := repository.ApplyInboxGroup(ctx, pending[0], applier)
	if err != nil || second.Applied {
		t.Fatalf("duplicate inbox apply = %#v, err=%v", second, err)
	}
	if applierCalls != 2 {
		t.Fatalf("applier calls = %d, want exactly one two-mutation group", applierCalls)
	}
	var replicated int
	if err := db.QueryRow(`SELECT COUNT(*) FROM replicated_rows`).Scan(&replicated); err != nil || replicated != 1 {
		t.Fatalf("replicated rows = %d, err=%v", replicated, err)
	}
	if err := repository.MarkOutboxApplied(ctx, pending[0], time.UnixMilli(100)); err != nil {
		t.Fatalf("acknowledge outbox: %v", err)
	}
	groups, err := repository.PendingOutbox(ctx, 1)
	if err != nil || len(groups) != 0 {
		t.Fatalf("pending after ack = %#v, err=%v", groups, err)
	}
}

func TestInboxRejectsPartialDuplicateAndOldEpoch(t *testing.T) {
	repository, _ := openTestRepository(t)
	ctx := context.Background()
	group := fixtureMutationGroup()
	if _, err := repository.ApplyInboxGroup(ctx, group, func(context.Context, *sql.Tx, Mutation) error { return nil }); err != nil {
		t.Fatalf("seed inbox: %v", err)
	}
	partial := group
	partial.Mutations = append([]Mutation(nil), group.Mutations...)
	partial.Mutations[1].ID = "mutation-new"
	if _, err := repository.ApplyInboxGroup(ctx, partial, func(context.Context, *sql.Tx, Mutation) error { return nil }); !errors.Is(err, ErrPartialDuplicate) {
		t.Fatalf("partial duplicate error = %v", err)
	}
	newEpoch := fixtureMutationGroup()
	newEpoch.TransactionID = "tx-new-epoch"
	newEpoch.SourceEpoch = 2
	for index := range newEpoch.Mutations {
		newEpoch.Mutations[index].ID += "-epoch2"
		newEpoch.Mutations[index].TransactionID = ""
		newEpoch.Mutations[index].SourceEpoch = 0
	}
	if _, err := repository.ApplyInboxGroup(ctx, newEpoch, func(context.Context, *sql.Tx, Mutation) error { return nil }); err != nil {
		t.Fatalf("apply newer epoch: %v", err)
	}
	duplicate, err := repository.ApplyInboxGroup(ctx, group, func(context.Context, *sql.Tx, Mutation) error {
		t.Fatal("already applied old-epoch duplicate reached applier")
		return nil
	})
	if err != nil || duplicate.Applied {
		t.Fatalf("old-epoch idempotent replay = %#v, err=%v", duplicate, err)
	}
	old := fixtureMutationGroup()
	old.TransactionID = "tx-old"
	for index := range old.Mutations {
		old.Mutations[index].ID += "-old"
		old.Mutations[index].TransactionID = ""
	}
	if _, err := repository.ApplyInboxGroup(ctx, old, func(context.Context, *sql.Tx, Mutation) error { return nil }); !errors.Is(err, ErrEpochFenced) {
		t.Fatalf("old epoch error = %v, want fenced", err)
	}
}

func TestRowVersionPreventsHistoricalOverwriteOfNewerRealtimeRow(t *testing.T) {
	repository, _ := openTestRepository(t)
	ctx := context.Background()
	newer := fixtureMutationGroup()
	newer.Mutations = newer.Mutations[:1]
	newer.Mutations[0].RowVersion = 10
	newer.Mutations[0].Payload = json.RawMessage(`{"id":1,"raw_json":"newer"}`)
	applyCalls := 0
	if _, err := repository.ApplyInboxGroup(ctx, newer, func(context.Context, *sql.Tx, Mutation) error {
		applyCalls++
		return nil
	}); err != nil {
		t.Fatalf("apply newer row: %v", err)
	}
	older := fixtureMutationGroup()
	older.TransactionID = "historical-batch"
	older.Mutations = older.Mutations[:1]
	older.Mutations[0].ID = "historical-row-1"
	older.Mutations[0].TransactionID = ""
	older.Mutations[0].RowVersion = 5
	older.Mutations[0].Payload = json.RawMessage(`{"id":1,"raw_json":"older"}`)
	result, err := repository.ApplyInboxGroup(ctx, older, func(context.Context, *sql.Tx, Mutation) error {
		applyCalls++
		return nil
	})
	if err != nil || !result.Applied {
		t.Fatalf("record stale historical mutation = %#v, err=%v", result, err)
	}
	if applyCalls != 1 {
		t.Fatalf("row applier calls = %d, stale historical row overwrote newer data", applyCalls)
	}
	version, err := repository.RowVersion(ctx, "usage_events", json.RawMessage(`{"id":1}`))
	if err != nil || version.RowVersion != 10 || version.MutationID != "mutation-1" {
		t.Fatalf("stored row version = %#v, err=%v", version, err)
	}
}

func TestHistoricalRowVersionCanRebindOnlyHistoricalContents(t *testing.T) {
	repository, db := openTestRepository(t)
	ctx := context.Background()
	key := json.RawMessage(`{"key":"usage_monitoring_model_format_version"}`)
	oldHistory := RowVersionRecord{Table: "settings", PrimaryKey: key, SourceEpoch: 1,
		RowVersion: 0, MutationID: "history-0123456789abcdef0123456789abcdef", MutationHash: "old"}
	newHistory := RowVersionRecord{Table: "settings", PrimaryKey: key, SourceEpoch: 1,
		RowVersion: 0, MutationID: "history-fedcba9876543210fedcba9876543210", MutationHash: "new"}

	accept := func(candidate RowVersionRecord, historical bool) (bool, error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return false, err
		}
		defer tx.Rollback()
		var accepted bool
		if historical {
			accepted, err = repository.AcceptHistoricalRowVersion(ctx, tx, candidate)
		} else {
			accepted, err = repository.AcceptRowVersion(ctx, tx, candidate)
		}
		if err != nil {
			return false, err
		}
		return accepted, tx.Commit()
	}

	if accepted, err := accept(oldHistory, true); err != nil || !accepted {
		t.Fatalf("seed history version: accepted=%v err=%v", accepted, err)
	}
	if accepted, err := accept(newHistory, true); err != nil || !accepted {
		t.Fatalf("rebind history version: accepted=%v err=%v", accepted, err)
	}
	stored, err := repository.RowVersion(ctx, "settings", key)
	if err != nil || stored.MutationID != newHistory.MutationID || stored.MutationHash != "new" {
		t.Fatalf("rebound history version=%#v err=%v", stored, err)
	}

	ordinaryConflict := newHistory
	ordinaryConflict.MutationID = "history-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ordinaryConflict.MutationHash = "ordinary-conflict"
	if accepted, err := accept(ordinaryConflict, false); !errors.Is(err, ErrRowVersionConflict) || accepted {
		t.Fatalf("ordinary same-version conflict: accepted=%v err=%v", accepted, err)
	}

	realtimeKey := json.RawMessage(`{"key":"realtime"}`)
	realtime := RowVersionRecord{Table: "settings", PrimaryKey: realtimeKey, SourceEpoch: 1,
		RowVersion: 0, MutationID: "mutation-realtime", MutationHash: "realtime"}
	if accepted, err := accept(realtime, false); err != nil || !accepted {
		t.Fatalf("seed realtime version: accepted=%v err=%v", accepted, err)
	}
	historyAgainstRealtime := newHistory
	historyAgainstRealtime.PrimaryKey = realtimeKey
	if accepted, err := accept(historyAgainstRealtime, true); !errors.Is(err, ErrRowVersionConflict) || accepted {
		t.Fatalf("history replaced realtime version: accepted=%v err=%v", accepted, err)
	}

	newerKey := json.RawMessage(`{"key":"newer-realtime"}`)
	newerRealtime := realtime
	newerRealtime.PrimaryKey = newerKey
	newerRealtime.RowVersion = 1
	if accepted, err := accept(newerRealtime, false); err != nil || !accepted {
		t.Fatalf("seed newer realtime version: accepted=%v err=%v", accepted, err)
	}
	staleHistory := newHistory
	staleHistory.PrimaryKey = newerKey
	if accepted, err := accept(staleHistory, true); err != nil || accepted {
		t.Fatalf("stale history was not ignored: accepted=%v err=%v", accepted, err)
	}
}

func TestHistoryCheckpointSurvivesRepositoryRestart(t *testing.T) {
	repository, db := openTestRepository(t)
	ctx := context.Background()
	migration, _, err := repository.CreateMigration(ctx, CreateMigrationRequest{
		IdempotencyKey: "history", ExpectedGeneration: 1, Source: BackendSQLite, Target: BackendMySQL,
		FrozenPriceHash: "prices-v1", FrozenPriceBook: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	migration, err = repository.AdvanceMigration(ctx, migration.ID, migration.Generation, PhaseCopyHistory)
	if err != nil {
		t.Fatal(err)
	}
	manifest := StaticManifest{{Name: "usage_events", Columns: []ColumnSpec{
		{Name: "id", LogicalType: "integer"}, {Name: "raw_json", LogicalType: "text", Nullable: true},
		{Name: "fail_body", LogicalType: "text", Nullable: true},
	}, PrimaryKey: []string{"id"}, VersionColumn: "id"}}
	source := &fixtureHistorySource{}
	target := &fixtureHistoryTarget{}
	copier := HistoryCopier{Manifest: manifest, Source: source, Target: target, Progress: repository}
	initialized, err := copier.RunBatch(ctx, migration.ID, "usage_events", migration.Generation)
	if err != nil || initialized.Progress.SourceWatermark == nil {
		t.Fatalf("initialize copy = %#v, err=%v", initialized, err)
	}
	copied, err := copier.RunBatch(ctx, migration.ID, "usage_events", initialized.Migration.Generation)
	if err != nil || !copied.Done || copied.Progress.RowsCopied != 1 || len(target.rows) != 1 {
		t.Fatalf("copy batch = %#v, target=%#v err=%v", copied, target.rows, err)
	}
	failed, err := repository.FailMigration(ctx, migration.ID, copied.Migration.Generation,
		errors.New("temporary target failure"))
	if err != nil {
		t.Fatalf("fail migration: %v", err)
	}
	restarted := NewSQLRepository(db, DialectSQLite)
	resumed, err := restarted.ResumeMigration(ctx, migration.ID, failed.Generation)
	if err != nil || resumed.ID != migration.ID || resumed.Status != StatusRunning {
		t.Fatalf("resume failed migration after restart = %#v, err=%v", resumed, err)
	}
	progress, found, err := restarted.TableProgress(ctx, migration.ID, "usage_events")
	if err != nil || !found || !progress.Completed || progress.RowsCopied != 1 ||
		string(progress.Checkpoint) != `{"id":1}` || string(progress.SourceWatermark) != `{"max":1}` {
		t.Fatalf("recovered checkpoint = %#v, found=%v err=%v", progress, found, err)
	}
}

func TestReplicationStalledThresholds(t *testing.T) {
	now := time.Unix(1_000, 0)
	base := ReplicationState{BacklogRows: 1, LastProgressAtMS: now.Add(-59 * time.Second).UnixMilli()}
	if got := ReplicationHealth(base, now); got.Warning || got.Stalled {
		t.Fatalf("59 second health = %#v", got)
	}
	base.LastProgressAtMS = now.Add(-60 * time.Second).UnixMilli()
	if got := ReplicationHealth(base, now); !got.Warning || got.Stalled {
		t.Fatalf("60 second health = %#v", got)
	}
	base.LastProgressAtMS = now.Add(-120 * time.Second).UnixMilli()
	if got := ReplicationHealth(base, now); !got.Warning || !got.Stalled {
		t.Fatalf("120 second health = %#v", got)
	}
	base.BacklogRows = 0
	if got := ReplicationHealth(base, now); got.Warning || got.Stalled {
		t.Fatalf("caught-up health = %#v", got)
	}
}

func TestReplicationHeartbeatDoesNotHideLackOfProgress(t *testing.T) {
	repository, _ := openTestRepository(t)
	ctx := context.Background()
	now := time.Unix(2_000, 0)
	clocked := repository.WithClock(func() time.Time { return now })
	update := ReplicationUpdate{Direction: "sqlite_to_mysql", Source: BackendSQLite,
		Target: BackendMySQL, Epoch: 1, SourceWatermark: 10, TargetWatermark: 1,
		BacklogRows: 9, BacklogBytes: 900}
	initial, err := clocked.UpdateReplication(ctx, update)
	if err != nil || initial.Warning || initial.Stalled || initial.LastProgressAtMS != now.UnixMilli() {
		t.Fatalf("initial replication state = %#v, err=%v", initial, err)
	}
	now = now.Add(60 * time.Second)
	warning, err := clocked.UpdateReplication(ctx, update)
	if err != nil || !warning.Warning || warning.Stalled || warning.HeartbeatAtMS != now.UnixMilli() {
		t.Fatalf("warning replication state = %#v, err=%v", warning, err)
	}
	now = now.Add(60 * time.Second)
	stalled, err := clocked.UpdateReplication(ctx, update)
	if err != nil || !stalled.Warning || !stalled.Stalled {
		t.Fatalf("stalled replication state = %#v, err=%v", stalled, err)
	}
}

func TestCleanupSafetyGateRejectsEveryUnsafeCondition(t *testing.T) {
	now := time.Unix(10_000, 0)
	safe := CleanupSafety{MySQLAvailable: true, MigrationComplete: true, ReplicationCaughtUp: true,
		ValidationPassed: true, ValidationToken: "validated", ValidationAtMS: now.UnixMilli(), ValidationMaxAge: time.Hour}
	if err := EvaluateCleanupSafety(safe, now); err != nil {
		t.Fatalf("safe cleanup rejected: %v", err)
	}
	cases := []CleanupSafety{
		withSafety(safe, func(value *CleanupSafety) { value.MySQLAvailable = false }),
		withSafety(safe, func(value *CleanupSafety) { value.MigrationComplete = false }),
		withSafety(safe, func(value *CleanupSafety) { value.ReplicationCaughtUp = false }),
		withSafety(safe, func(value *CleanupSafety) { value.ValidationPassed = false }),
		withSafety(safe, func(value *CleanupSafety) { value.ValidationToken = "" }),
		withSafety(safe, func(value *CleanupSafety) { value.ValidationAtMS = now.Add(-2 * time.Hour).UnixMilli() }),
	}
	for index, value := range cases {
		if err := EvaluateCleanupSafety(value, now); !errors.Is(err, ErrCleanupUnsafe) {
			t.Errorf("unsafe case %d error = %v", index, err)
		}
	}
}

func TestRunningCleanupIsDurablyPausedWhenSafetyGateCloses(t *testing.T) {
	repository, _ := openTestRepository(t)
	ctx := context.Background()
	now := time.Unix(20_000, 0)
	manager := CacheManager{Store: repository, Data: fixtureCleanupData{}, Now: func() time.Time { return now }}
	preview, err := manager.PreviewCleanup(ctx, 15)
	if err != nil {
		t.Fatal(err)
	}
	safety := CleanupSafety{MySQLAvailable: true, MigrationComplete: true, ReplicationCaughtUp: true,
		ValidationPassed: true, ValidationToken: "validation", ValidationAtMS: now.UnixMilli(), ValidationMaxAge: time.Hour}
	task, created, err := manager.StartCleanup(ctx, "cleanup-1", preview, 15, safety)
	if err != nil || !created {
		t.Fatalf("start cleanup: task=%#v created=%v err=%v", task, created, err)
	}
	safety.ReplicationCaughtUp = false
	paused, err := manager.RunCleanupBatch(ctx, task.ID, task.Generation, safety)
	if !errors.Is(err, ErrCleanupUnsafe) || paused.Status != StatusPaused {
		t.Fatalf("unsafe cleanup run = %#v, err=%v", paused, err)
	}
	recovered, err := repository.MaintenanceTask(ctx, task.ID)
	if err != nil || recovered.Status != StatusPaused {
		t.Fatalf("persisted paused cleanup = %#v, err=%v", recovered, err)
	}
}

func TestRunRebuildFinalizesInReplacementDatabaseAndPersistsPreSwapFailure(t *testing.T) {
	repository, _ := openTestRepository(t)
	ctx := context.Background()
	now := time.Unix(25_000, 0)
	safety := CleanupSafety{MySQLAvailable: true, MigrationComplete: true,
		ReplicationCaughtUp: true, ValidationPassed: true, ValidationToken: "rebuild-validation",
		ValidationAtMS: now.UnixMilli(), ValidationMaxAge: time.Hour}
	manager := CacheManager{Store: repository, Now: func() time.Time { return now }}
	task, created, err := manager.StartRebuild(ctx, "rebuild-finalize", 15, safety)
	if err != nil || !created {
		t.Fatalf("start rebuild task=%#v created=%v err=%v", task, created, err)
	}
	rebuilder := &fixtureCacheRebuilder{}
	finalized, err := manager.RunRebuild(ctx, task.ID, task.Generation, safety, rebuilder)
	if err != nil || finalized.Status != StatusSucceeded || finalized.Generation != task.Generation+1 ||
		rebuilder.replacedTask.ID != task.ID {
		t.Fatalf("finalized rebuild=%#v callback=%#v err=%v", finalized,
			rebuilder.replacedTask, err)
	}

	failedTask, created, err := manager.StartRebuild(ctx, "rebuild-replacement-failure", 15, safety)
	if err != nil || !created {
		t.Fatalf("start failed rebuild task=%#v created=%v err=%v", failedTask, created, err)
	}
	replacementErr := errors.New("replacement kept old sqlite live")
	_, err = manager.RunRebuild(ctx, failedTask.ID, failedTask.Generation, safety,
		&fixtureCacheRebuilder{replaceErr: replacementErr})
	if !errors.Is(err, replacementErr) {
		t.Fatalf("replacement error=%v", err)
	}
	persisted, err := repository.MaintenanceTask(ctx, failedTask.ID)
	if err != nil || persisted.Status != StatusFailed ||
		!strings.Contains(persisted.LastError, replacementErr.Error()) {
		t.Fatalf("persisted replacement failure=%#v err=%v", persisted, err)
	}
}

func TestMaintenanceTaskQueriesSeparateLatestFromActive(t *testing.T) {
	repository, _ := openTestRepository(t)
	ctx := context.Background()
	task, created, err := repository.CreateMaintenanceTask(ctx, CreateMaintenanceTaskRequest{
		IdempotencyKey: "active-query", Kind: TaskCleanup, RetentionDays: 15,
		ValidationToken: "validation", Preview: CleanupPreview{CutoffMS: 1, CreatedAtMS: 1},
	})
	if err != nil || !created {
		t.Fatalf("create maintenance task=%#v created=%v err=%v", task, created, err)
	}
	active, exists, err := repository.ActiveMaintenanceTask(ctx, TaskCleanup)
	if err != nil || !exists || active.ID != task.ID || active.Status != StatusRunning {
		t.Fatalf("active maintenance task=%#v exists=%v err=%v", active, exists, err)
	}
	paused, err := repository.PauseMaintenanceTask(ctx, task.ID, task.Generation)
	if err != nil {
		t.Fatal(err)
	}
	active, exists, err = repository.ActiveMaintenanceTask(ctx, TaskCleanup)
	if err != nil || !exists || active.Status != StatusPaused {
		t.Fatalf("paused active task=%#v exists=%v err=%v", active, exists, err)
	}
	canceled, err := repository.CancelMaintenanceTask(ctx, paused.ID, paused.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, err := repository.ActiveMaintenanceTask(ctx, TaskCleanup); err != nil || exists {
		t.Fatalf("terminal task remained active: exists=%v err=%v", exists, err)
	}
	latest, exists, err := repository.LatestMaintenanceTask(ctx, TaskCleanup)
	if err != nil || !exists || latest.ID != canceled.ID || latest.Status != StatusCanceled {
		t.Fatalf("latest maintenance task=%#v exists=%v err=%v", latest, exists, err)
	}
}

func TestOperationIdempotencyPersistsResultAndRejectsKeyReuse(t *testing.T) {
	repository, db := openTestRepository(t)
	ctx := context.Background()
	hash, err := HashIdempotencyRequest(map[string]any{"migrationId": "m1", "generation": 7})
	if err != nil {
		t.Fatal(err)
	}
	want := OperationIdempotencyRecord{Key: "request-1", Operation: "migration.validate",
		RequestHash: hash, Result: json.RawMessage(`{"generation":8,"status":"ready"}`), Generation: 8}
	stored, created, err := repository.StoreOperationIdempotency(ctx, want)
	if err != nil || !created {
		t.Fatalf("store idempotency = %#v, created=%v err=%v", stored, created, err)
	}
	restarted := NewSQLRepository(db, DialectSQLite)
	replayed, created, err := restarted.StoreOperationIdempotency(ctx, OperationIdempotencyRecord{
		Key: want.Key, Operation: want.Operation, RequestHash: want.RequestHash,
		Result: json.RawMessage(`{"different":"response"}`), Generation: 99,
	})
	if err != nil || created || string(replayed.Result) != string(want.Result) || replayed.Generation != 8 {
		t.Fatalf("replayed idempotency = %#v, created=%v err=%v", replayed, created, err)
	}
	if _, _, err := restarted.StoreOperationIdempotency(ctx, OperationIdempotencyRecord{
		Key: want.Key, Operation: want.Operation, RequestHash: "different",
		Result: json.RawMessage(`{}`),
	}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different request error = %v, want idempotency conflict", err)
	}
}

func openTestRepository(t *testing.T) (*SQLRepository, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "migration.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	repository := NewSQLRepository(db, DialectSQLite)
	if err := repository.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure migration schema: %v", err)
	}
	return repository, db
}

func fixtureMutationGroup() MutationGroup {
	return MutationGroup{TransactionID: "tx-1", Source: BackendSQLite, Target: BackendMySQL,
		SourceEpoch: 1, Mutations: []Mutation{
			{ID: "mutation-1", Sequence: 0, Table: "usage_events", Operation: OperationInsert,
				PrimaryKey: json.RawMessage(`{"id":1}`), Payload: json.RawMessage(`{"id":1,"raw_json":"完整"}`), SchemaVersion: 1, RowVersion: 1},
			{ID: "mutation-2", Sequence: 1, Table: "usage_relations", Operation: OperationInsert,
				PrimaryKey: json.RawMessage(`{"eventId":1,"key":"a"}`), Payload: json.RawMessage(`{"eventId":1,"key":"a"}`), SchemaVersion: 1, RowVersion: 1},
		}}
}

type fixtureHistorySource struct{}

func (*fixtureHistorySource) CaptureWatermark(context.Context, TableSpec) (json.RawMessage, error) {
	return json.RawMessage(`{"max":1}`), nil
}

func (*fixtureHistorySource) ReadBatch(_ context.Context, _ TableSpec, checkpoint, _ json.RawMessage, _ int) (HistoryBatch, error) {
	if len(checkpoint) != 0 {
		return HistoryBatch{Done: true}, nil
	}
	return HistoryBatch{Rows: []AuthoritativeRow{{Values: []any{int64(1), "{\"full\":true}", nil},
		PrimaryKey: json.RawMessage(`{"id":1}`), RowVersion: 1, Bytes: 32}},
		NextCheckpoint: json.RawMessage(`{"id":1}`), Done: true}, nil
}

type fixtureHistoryTarget struct{ rows []AuthoritativeRow }

func (target *fixtureHistoryTarget) ApplyBatch(_ context.Context, _ TableSpec, _ json.RawMessage, rows []AuthoritativeRow) error {
	target.rows = append(target.rows, rows...)
	return nil
}

func withSafety(value CleanupSafety, change func(*CleanupSafety)) CleanupSafety {
	change(&value)
	return value
}

type fixtureCleanupData struct{}

func (fixtureCleanupData) Preview(context.Context, int64) ([]TableCleanupPreview, error) {
	return []TableCleanupPreview{{Table: "usage_events", Rows: 10}}, nil
}

type fixtureCacheRebuilder struct {
	replacedTask MaintenanceTask
	replaceErr   error
}

func (*fixtureCacheRebuilder) RebuildTemporary(
	context.Context,
	int,
	int64,
) (string, CacheCoverage, error) {
	return "temporary.sqlite", CacheCoverage{}, nil
}

func (*fixtureCacheRebuilder) ValidateTemporary(context.Context, string, CacheCoverage) error {
	return nil
}

func (rebuilder *fixtureCacheRebuilder) AtomicReplaceUnderWriteFence(
	_ context.Context,
	_ string,
	task MaintenanceTask,
) (MaintenanceTask, error) {
	rebuilder.replacedTask = task
	if rebuilder.replaceErr != nil {
		return MaintenanceTask{}, rebuilder.replaceErr
	}
	task.Generation++
	task.Status = StatusSucceeded
	task.FinishedAtMS = time.Now().UnixMilli()
	return task, nil
}

func (fixtureCleanupData) DeleteBatch(context.Context, string, CleanupConstraints) (CleanupBatchResult, error) {
	return CleanupBatchResult{Deleted: 10, Done: true}, nil
}
