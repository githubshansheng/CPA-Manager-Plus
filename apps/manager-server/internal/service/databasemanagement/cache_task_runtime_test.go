package databasemanagement

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/outboxcontext"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func TestRuntimeCacheCleanupPreviewStartIdempotencyAndRestartRecovery(t *testing.T) {
	runtime, source, _, repository, state := openReadyCacheTaskRuntime(t, true)
	ctx := context.Background()
	preview, err := runtime.PreviewCacheCleanup(ctx, MutationControl{
		ExpectedGeneration: state.Generation, IdempotencyKey: "preview-cleanup",
	})
	if err != nil || !preview.Eligible || preview.EstimatedRows != 1 {
		t.Fatalf("cleanup preview=%#v, err=%v", preview, err)
	}
	if _, err := runtime.PreviewCacheCleanup(ctx, MutationControl{
		ExpectedGeneration: state.Generation + 1, IdempotencyKey: "stale-preview",
	}); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("stale preview error=%v", err)
	}
	request := confirmedCacheCleanupMutation(state, "manual-cleanup")
	started, err := runtime.CleanupCache(ctx, request)
	if err != nil || started.Databases.SQLite.CleanupStatus != string(databasemigration.StatusRunning) {
		t.Fatalf("start cleanup status=%#v, err=%v", started.Databases.SQLite, err)
	}
	replayed, err := runtime.CleanupCache(ctx, request)
	if err != nil || replayed.Databases.SQLite.CleanupStatus != string(databasemigration.StatusRunning) {
		t.Fatalf("replay cleanup status=%#v, err=%v", replayed.Databases.SQLite, err)
	}
	if _, err := runtime.CleanupCache(ctx,
		confirmedCacheCleanupMutation(state, "parallel-cleanup")); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("parallel cleanup error=%v", err)
	}

	// Advance one bounded batch, then construct a new Runtime over the same
	// durable control file and SQLite metadata to simulate process restart.
	runtime.cleanupCacheOnce(ctx)
	task, exists, err := repository.ActiveMaintenanceTask(ctx, databasemigration.TaskCleanup)
	if err != nil || !exists || task.Status != databasemigration.StatusRunning || task.Generation <= 1 {
		t.Fatalf("checkpointed task=%#v exists=%v err=%v", task, exists, err)
	}
	restarted, err := NewRuntime(RuntimeOptions{Control: runtime.control,
		SQLite: runtime.sqlite, MySQL: runtime.mysql, SQLitePath: runtime.sqlitePath, DataDir: runtime.dataDir})
	if err != nil {
		t.Fatal(err)
	}
	for attempts := 0; attempts < 80; attempts++ {
		restarted.cleanupCacheOnce(ctx)
		latest, found, latestErr := repository.LatestMaintenanceTask(ctx, databasemigration.TaskCleanup)
		if latestErr != nil {
			t.Fatal(latestErr)
		}
		if found && latest.Status == databasemigration.StatusSucceeded {
			break
		}
		if attempts == 79 {
			t.Fatalf("cleanup did not recover to completion: %#v", latest)
		}
	}
	assertCacheCount(t, source, "account_action_candidates", 0)
	status, err := restarted.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Databases.SQLite.CleanupStatus != string(databasemigration.StatusSucceeded) ||
		status.Databases.SQLite.LastCleanupAtMS == 0 || status.CacheCoverage.LastValidatedAtMS == 0 {
		t.Fatalf("completed cleanup status=%#v coverage=%#v", status.Databases.SQLite, status.CacheCoverage)
	}
}

func TestRuntimeCacheCleanupRepairsReplayAfterTaskCreationCrashWindow(t *testing.T) {
	runtime, source, _, _, state := openReadyCacheTaskRuntime(t, false)
	ctx := context.Background()
	request := confirmedCacheCleanupMutation(state, "cleanup-crash-window")
	if _, err := runtime.CleanupCache(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ExecContext(ctx, `DELETE FROM database_operation_idempotency
		WHERE idempotency_key=?`, request.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.CleanupCache(ctx, request); err != nil {
		t.Fatalf("repair replay after task creation: %v", err)
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

func TestRuntimeCacheCleanupRejectsStaleExplicitConfirmationBeforeCreatingTask(t *testing.T) {
	runtime, _, _, repository, state := openReadyCacheTaskRuntime(t, false)
	request := confirmedCacheCleanupMutation(state, "stale-cleanup-confirmation")
	request.ValidationToken = "stale-validation-token"
	if _, err := runtime.CleanupCache(context.Background(), request); !errors.Is(err, ErrUnsafeOperation) {
		t.Fatalf("stale cleanup confirmation error=%v", err)
	}
	if _, exists, err := repository.LatestMaintenanceTask(context.Background(),
		databasemigration.TaskCleanup); err != nil || exists {
		t.Fatalf("stale confirmation created a cleanup task: exists=%v err=%v", exists, err)
	}
}

func TestRuntimeCacheCleanupPausesOnBacklogAndResumesAfterCatchup(t *testing.T) {
	runtime, source, target, repository, state := openReadyCacheTaskRuntime(t, true)
	ctx := context.Background()
	request := confirmedCacheCleanupMutation(state, "pause-cleanup")
	if _, err := runtime.CleanupCache(ctx, request); err != nil {
		t.Fatal(err)
	}
	tx, err := outboxcontext.Begin(ctx, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_action_candidates
		(action_type,status,auth_file_name,first_seen_at_ms,last_seen_at_ms,created_at_ms,updated_at_ms)
		VALUES('disable','pending','new-pending.json',1,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	runtime.cleanupCacheOnce(ctx)
	paused, exists, err := repository.ActiveMaintenanceTask(ctx, databasemigration.TaskCleanup)
	if err != nil || !exists || paused.Status != databasemigration.StatusPaused {
		t.Fatalf("paused cleanup=%#v exists=%v err=%v", paused, exists, err)
	}
	status, err := runtime.Status(ctx)
	if err != nil || !status.CacheCoverage.CleanupPaused || status.CacheCoverage.PauseReason == "" {
		t.Fatalf("paused status=%#v, err=%v", status.CacheCoverage, err)
	}
	watermark := markCacheRuntimeOutboxApplied(t, repository)
	if _, err := target.ExecContext(ctx, `INSERT INTO database_inbox_sources
		(source_backend,accepted_epoch,watermark,updated_at_ms) VALUES(?,1,?,?)
		ON CONFLICT(source_backend) DO UPDATE SET watermark=excluded.watermark,
		updated_at_ms=excluded.updated_at_ms`, databasemigration.BackendSQLite,
		watermark, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	runtime.cleanupCacheOnce(ctx)
	resumed, exists, err := repository.ActiveMaintenanceTask(ctx, databasemigration.TaskCleanup)
	if err != nil || !exists || resumed.Status != databasemigration.StatusRunning || resumed.Generation <= paused.Generation {
		t.Fatalf("resumed cleanup=%#v exists=%v err=%v", resumed, exists, err)
	}
}

func TestRuntimeCacheCleanupPolicyControlsDailySchedulingOnly(t *testing.T) {
	runtime, _, _, repository, state := openReadyCacheTaskRuntime(t, false)
	ctx := context.Background()
	policy, err := repository.CachePolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetCachePolicy(ctx, policy.Generation, false, 15,
		databasemigration.DefaultBatchSize); err != nil {
		t.Fatal(err)
	}
	next, err := runtime.control.Update(state.Generation, func(current *control.State) error {
		current.CachePolicy.Enabled = false
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.cleanupCacheOnce(ctx)
	if _, exists, err := repository.LatestMaintenanceTask(ctx, databasemigration.TaskCleanup); err != nil || exists {
		t.Fatalf("disabled policy scheduled task: exists=%v err=%v", exists, err)
	}
	policy, err = repository.CachePolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetCachePolicy(ctx, policy.Generation, true, 15,
		databasemigration.DefaultBatchSize); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.control.Update(next.Generation, func(current *control.State) error {
		current.CachePolicy.Enabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runtime.cleanupCacheOnce(ctx)
	task, exists, err := repository.LatestMaintenanceTask(ctx, databasemigration.TaskCleanup)
	if err != nil || !exists || !strings.HasPrefix(task.IdempotencyKey, "scheduled-sqlite-cleanup:") {
		t.Fatalf("enabled policy task=%#v exists=%v err=%v", task, exists, err)
	}
}

func TestRuntimeCacheCleanupPersistsFailedBatchInStatus(t *testing.T) {
	runtime, source, _, repository, state := openReadyCacheTaskRuntime(t, false)
	ctx := context.Background()
	if _, err := runtime.CleanupCache(ctx,
		confirmedCacheCleanupMutation(state, "failed-cleanup")); err != nil {
		t.Fatal(err)
	}
	triggers := databasemigration.JournalTriggerNames("usage_events")
	if len(triggers) == 0 {
		t.Fatal("usage_events has no journal trigger names")
	}
	if _, err := source.ExecContext(ctx, `DROP TRIGGER "`+triggers[0]+`"`); err != nil {
		t.Fatal(err)
	}
	runtime.cleanupCacheOnce(ctx)
	latest, exists, err := repository.LatestMaintenanceTask(ctx, databasemigration.TaskCleanup)
	if err != nil || !exists || latest.Status != databasemigration.StatusFailed || latest.LastError == "" {
		t.Fatalf("failed task=%#v exists=%v err=%v", latest, exists, err)
	}
	status, err := runtime.Status(ctx)
	if err != nil || status.Databases.SQLite.CleanupStatus != string(databasemigration.StatusFailed) ||
		status.Databases.SQLite.LastError == "" {
		t.Fatalf("failed cleanup status=%#v err=%v", status.Databases.SQLite, err)
	}
}

func openReadyCacheTaskRuntime(
	t *testing.T,
	withOldAction bool,
) (*Runtime, *sql.DB, *sql.DB, *databasemigration.SQLRepository, control.State) {
	t.Helper()
	source, repository := openCacheRuntimeDB(t)
	ctx := context.Background()
	if withOldAction {
		tx, err := outboxcontext.Begin(ctx, source, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_action_candidates
			(action_type,status,auth_file_name,first_seen_at_ms,last_seen_at_ms,created_at_ms,updated_at_ms)
			VALUES('disable','resolved','old-cleanup.json',1,1,1,1)`); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	watermark := markCacheRuntimeOutboxApplied(t, repository)
	target, err := sqliterepo.Open(filepath.Join(t.TempDir(), "mysql-fixture.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	targetRepository := databasemigration.NewSQLRepository(target, databasemigration.DialectSQLite)
	if err := targetRepository.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := target.ExecContext(ctx, `INSERT INTO database_inbox_sources
		(source_backend,accepted_epoch,watermark,updated_at_ms) VALUES(?,1,?,?)
		ON CONFLICT(source_backend) DO UPDATE SET watermark=excluded.watermark,
		updated_at_ms=excluded.updated_at_ms`, databasemigration.BackendSQLite,
		watermark, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	nowMS := time.Now().UnixMilli()
	validationToken := "cache-validation-token"
	validation := databasemigration.ValidationResult{MigrationID: "migration-cache",
		FinalOutboxWatermark: watermark, AppliedWatermark: watermark, DerivedDataReady: true,
		Passed: true, Token: validationToken, ValidatedAtMS: nowMS}
	persisted, err := json.Marshal(map[string]any{
		"format": 1, "validation": validation, "frozenPriceBook": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ExecContext(ctx, `INSERT INTO database_migrations
		(id,idempotency_key,generation,source_backend,target_backend,phase,status,batch_size,
		frozen_price_hash,final_outbox_watermark,validation_json,validation_token,
		created_at_ms,updated_at_ms,finished_at_ms,last_error)
		VALUES('migration-cache','migration-cache-key',1,?,?,?,?,1000,'prices',?,?,?, ?,?,?, '')`,
		databasemigration.BackendSQLite, databasemigration.BackendMySQL,
		databasemigration.PhaseCompleted, databasemigration.StatusSucceeded, watermark,
		string(persisted), validationToken, nowMS, nowMS, nowMS); err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	controlStore, err := control.NewStore(filepath.Join(t.TempDir(), "database-control.json.enc"), protector)
	if err != nil {
		t.Fatal(err)
	}
	state := control.DefaultState()
	state.MySQL = dbmysql.Config{Host: "mysql.test", Database: "cpamp", Username: "manager", TLSMode: dbmysql.TLSDisabled}
	state.ReplicationEnabled = true
	state.BusinessReadPrimary = database.BackendMySQL
	state.Migration = control.MigrationRef{ID: "migration-cache", Phase: string(databasemigration.PhaseCompleted),
		Status: string(databasemigration.StatusSucceeded), ValidationToken: validationToken}
	state, err = controlStore.Save(0, state)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(RuntimeOptions{Control: controlStore,
		SQLite:     database.NewSQLBackend(database.BackendSQLite, source),
		MySQL:      database.NewSQLBackend(database.BackendMySQL, target),
		SQLitePath: filepath.Join(t.TempDir(), "cache-runtime.sqlite"), DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return runtime, source, target, repository, state
}

func confirmedOperation(
	state control.State,
	target database.BackendKind,
) DangerousOperationConfirmation {
	return DangerousOperationConfirmation{Target: target, MigrationID: state.Migration.ID,
		ValidationToken: state.Migration.ValidationToken}
}

func confirmedCacheCleanupMutation(state control.State, idempotencyKey string) CacheCleanupMutation {
	return CacheCleanupMutation{
		MutationControl: MutationControl{ExpectedGeneration: state.Generation,
			IdempotencyKey: idempotencyKey},
		DangerousOperationConfirmation: confirmedOperation(state, database.BackendSQLite),
	}
}
