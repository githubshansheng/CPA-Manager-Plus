package databasemanagement

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	_ "modernc.org/sqlite"
)

func TestTrackedDerivedExecutorRejectsWorkSupersededByPause(t *testing.T) {
	repository, migration := openDerivedMigrationRepository(t)
	stale := migration
	if _, err := repository.PauseMigration(t.Context(), migration.ID, migration.Generation); err != nil {
		t.Fatal(err)
	}
	delegate := &fakeDerivedExecutor{tables: []string{"derived_a"}}
	executor := trackedDerivedExecutor{
		runtime: &Runtime{}, repository: repository, migration: stale, delegate: delegate,
	}

	_, err := executor.RebuildTable(t.Context(), "derived_a", derivedRebuildWatermark{})
	if !errors.Is(err, databasemigration.ErrGenerationConflict) {
		t.Fatalf("stale derived work error = %v, want generation conflict", err)
	}
	if len(delegate.rebuilt) != 0 {
		t.Fatalf("superseded work reached derived executor: %v", delegate.rebuilt)
	}
	if !isSupersededMigrationWorkError(err) {
		t.Fatalf("generation conflict must be treated as superseded work: %v", err)
	}
}

func TestCancelMigrationWorkInterruptsInFlightDerivedSQL(t *testing.T) {
	repository, migration := openDerivedMigrationRepository(t)
	runtime := &Runtime{}
	delegate := &blockingDerivedExecutor{started: make(chan struct{})}
	executor := trackedDerivedExecutor{
		runtime: runtime, repository: repository, migration: migration, delegate: delegate,
	}
	result := make(chan error, 1)
	go func() {
		_, err := executor.RebuildTable(context.Background(), "derived_a", derivedRebuildWatermark{})
		result <- err
	}()

	select {
	case <-delegate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("derived executor did not start")
	}
	if !runtime.cancelMigrationWork(migration.ID) {
		t.Fatal("active migration work was not canceled")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || !isSupersededMigrationWorkError(err) {
			t.Fatalf("canceled derived work error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("derived executor did not stop after cancellation")
	}
	if _, active := runtime.migrationWorkSnapshot(migration.ID); active {
		t.Fatal("completed cancellation left stale active-work status")
	}
}

func TestDerivedMigrationStatusReportsPhaseProgressAndCurrentTable(t *testing.T) {
	repository, migration := openDerivedMigrationRepository(t)
	tables := newMySQLDerivedExecutor(nil).Tables()
	encodedWatermark := json.RawMessage(`{"usageEventId":42,"pricingRevision":"revision"}`)
	var progress databasemigration.TableProgress
	var err error
	migration, progress, err = repository.InitializeTableProgress(
		t.Context(), migration.ID, tables[0], migration.Generation, encodedWatermark, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	progress.Completed = true
	progress.RowsCopied = 7
	progress.Requests = 1
	migration, err = repository.SaveTableProgress(t.Context(), migration.ID, migration.Generation, progress)
	if err != nil {
		t.Fatal(err)
	}

	result := database.MigrationStatus{}
	percent := (&Runtime{}).enrichDerivedMigrationProgress(t.Context(), repository, migration, &result)
	if result.CompletedSteps != 1 || result.TotalSteps != len(tables) {
		t.Fatalf("derived steps = %d/%d, want 1/%d", result.CompletedSteps, result.TotalSteps, len(tables))
	}
	if result.CurrentTable != tables[1] {
		t.Fatalf("current derived table = %q, want %q", result.CurrentTable, tables[1])
	}
	wantPercent := 100 / float64(len(tables))
	if percent != wantPercent {
		t.Fatalf("derived progress = %v, want %v", percent, wantPercent)
	}
	if len(result.DerivedTables) != len(tables) || !result.DerivedTables[0].Completed {
		t.Fatalf("derived table status = %#v", result.DerivedTables)
	}
}

func TestMigrationProgressReportsCompletedWorkWhileAwaitingValidation(t *testing.T) {
	if got := migrationProgressForPhase(databasemigration.PhaseValidate, 100, 100); got != 100 {
		t.Fatalf("validate phase progress = %v, want 100", got)
	}
	if got := migrationProgressForPhase(databasemigration.PhaseCopyHistory, 42, 0); got != 42 {
		t.Fatalf("copy-history progress = %v, want 42", got)
	}
	if got := migrationProgressForPhase(databasemigration.PhaseRebuildDerived, 100, 37.5); got != 37.5 {
		t.Fatalf("derived progress = %v, want 37.5", got)
	}
}

func TestMigrationValidationBecomesStaleWhenReplicationWatermarkAdvances(t *testing.T) {
	migration := databasemigration.Migration{
		FinalOutboxWatermark: 8,
		ValidationToken:      "validated",
		Validation:           &databasemigration.ValidationResult{Passed: true},
	}
	replication := database.ReplicationStatus{SourceWatermark: 8, TargetWatermark: 8}
	if !migrationValidationIsCurrent(migration, replication, true) {
		t.Fatal("matching validation and replication watermarks were marked stale")
	}
	replication.SourceWatermark = 9
	if migrationValidationIsCurrent(migration, replication, true) {
		t.Fatal("advanced source watermark did not invalidate validation")
	}
}

func TestResumeMigrationRequiresConnectedTarget(t *testing.T) {
	runtime := &Runtime{}
	err := runtime.requireMigrationResumeTarget(t.Context(), databasemigration.Migration{
		Target: databasemigration.BackendMySQL,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing MySQL resume target error = %v", err)
	}
	if err := runtime.requireMigrationResumeTarget(t.Context(), databasemigration.Migration{
		Target: databasemigration.BackendSQLite,
	}); err != nil {
		t.Fatalf("SQLite migration target unexpectedly required MySQL: %v", err)
	}
}

type blockingDerivedExecutor struct {
	started chan struct{}
}

func (e *blockingDerivedExecutor) Tables() []string { return []string{"derived_a"} }

func (e *blockingDerivedExecutor) CaptureWatermark(context.Context) (derivedRebuildWatermark, error) {
	return derivedRebuildWatermark{}, nil
}

func (e *blockingDerivedExecutor) RebuildTable(
	ctx context.Context,
	_ string,
	_ derivedRebuildWatermark,
) (int64, error) {
	close(e.started)
	<-ctx.Done()
	return 0, ctx.Err()
}

func openDerivedMigrationRepository(t *testing.T) (*databasemigration.SQLRepository, databasemigration.Migration) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	repository := databasemigration.NewSQLRepository(db, databasemigration.DialectSQLite)
	if err := repository.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	migration, _, err := repository.CreateMigration(t.Context(), databasemigration.CreateMigrationRequest{
		IdempotencyKey: "migration-work-test", ExpectedGeneration: 1,
		Source: databasemigration.BackendSQLite, Target: databasemigration.BackendMySQL,
		FrozenPriceHash: "prices-v1", FrozenPriceBook: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	migration, err = repository.AdvanceMigration(
		t.Context(), migration.ID, migration.Generation, databasemigration.PhaseCopyHistory,
	)
	if err != nil {
		t.Fatal(err)
	}
	migration, err = repository.AdvanceMigration(
		t.Context(), migration.ID, migration.Generation, databasemigration.PhaseRebuildDerived,
	)
	if err != nil {
		t.Fatal(err)
	}
	return repository, migration
}
