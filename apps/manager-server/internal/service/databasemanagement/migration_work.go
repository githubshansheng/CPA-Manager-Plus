package databasemanagement

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
)

const (
	historyMigrationBatchTimeout = 30 * time.Minute
	derivedRebuildTimeout        = 6 * time.Hour
)

var errMigrationWorkSuperseded = errors.New("database migration work was superseded")

type migrationWorkState struct {
	ID              string
	Phase           databasemigration.MigrationPhase
	Table           string
	StartedAtMS     int64
	CancelRequested bool
	cancel          context.CancelFunc
	sequence        uint64
}

type migrationWorkSnapshot struct {
	ID              string
	Phase           databasemigration.MigrationPhase
	Table           string
	StartedAtMS     int64
	CancelRequested bool
}

func (r *Runtime) beginMigrationWork(
	parent context.Context,
	migrationID string,
	phase databasemigration.MigrationPhase,
	table string,
	timeout time.Duration,
) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}
	var workCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		workCtx, cancel = context.WithTimeout(parent, timeout)
	} else {
		workCtx, cancel = context.WithCancel(parent)
	}

	r.migrationMu.Lock()
	r.migrationSeq++
	sequence := r.migrationSeq
	if r.migrationRun.cancel != nil {
		r.migrationRun.cancel()
	}
	r.migrationRun = migrationWorkState{
		ID: migrationID, Phase: phase, Table: table,
		StartedAtMS: time.Now().UnixMilli(), cancel: cancel, sequence: sequence,
	}
	r.migrationMu.Unlock()

	return workCtx, func() {
		cancel()
		r.migrationMu.Lock()
		if r.migrationRun.sequence == sequence {
			r.migrationRun = migrationWorkState{}
		}
		r.migrationMu.Unlock()
	}
}

func (r *Runtime) cancelMigrationWork(migrationID string) bool {
	r.migrationMu.Lock()
	if r.migrationRun.ID != migrationID || r.migrationRun.cancel == nil {
		r.migrationMu.Unlock()
		return false
	}
	r.migrationRun.CancelRequested = true
	cancel := r.migrationRun.cancel
	r.migrationMu.Unlock()
	cancel()
	return true
}

func (r *Runtime) migrationWorkSnapshot(migrationID string) (migrationWorkSnapshot, bool) {
	r.migrationMu.RLock()
	defer r.migrationMu.RUnlock()
	if r.migrationRun.ID == "" || r.migrationRun.ID != migrationID {
		return migrationWorkSnapshot{}, false
	}
	return migrationWorkSnapshot{
		ID: r.migrationRun.ID, Phase: r.migrationRun.Phase, Table: r.migrationRun.Table,
		StartedAtMS: r.migrationRun.StartedAtMS, CancelRequested: r.migrationRun.CancelRequested,
	}, true
}

func requireCurrentMigrationWork(
	ctx context.Context,
	repository *databasemigration.SQLRepository,
	migrationID string,
	phase databasemigration.MigrationPhase,
	expectedGeneration int64,
) error {
	current, err := repository.Migration(ctx, migrationID)
	if err != nil {
		return err
	}
	if current.Generation != expectedGeneration {
		return fmt.Errorf("%w: expected %d, actual %d",
			databasemigration.ErrGenerationConflict, expectedGeneration, current.Generation)
	}
	if current.Status != databasemigration.StatusRunning || current.Phase != phase {
		return fmt.Errorf("%w: migration is %s/%s", errMigrationWorkSuperseded, current.Phase, current.Status)
	}
	return nil
}

func isSupersededMigrationWorkError(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, databasemigration.ErrGenerationConflict) ||
		errors.Is(err, errMigrationWorkSuperseded)
}

func (r *Runtime) requireMigrationResumeTarget(ctx context.Context, migration databasemigration.Migration) error {
	if migration.Target != databasemigration.BackendMySQL {
		return nil
	}
	db, release := r.mysqlDB()
	defer release()
	if db == nil {
		return errors.Join(ErrUnavailable, errors.New("mysql is not connected; reconnect before resuming migration"))
	}
	pingCtx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return errors.Join(ErrUnavailable, fmt.Errorf("ping mysql before resuming migration: %w", err))
	}
	return nil
}

type trackedDerivedExecutor struct {
	runtime    *Runtime
	repository *databasemigration.SQLRepository
	migration  databasemigration.Migration
	delegate   derivedPhaseExecutor
}

func (e trackedDerivedExecutor) Tables() []string {
	return e.delegate.Tables()
}

func (e trackedDerivedExecutor) CaptureWatermark(ctx context.Context) (derivedRebuildWatermark, error) {
	return e.delegate.CaptureWatermark(ctx)
}

func (e trackedDerivedExecutor) RebuildTable(
	ctx context.Context,
	table string,
	watermark derivedRebuildWatermark,
) (int64, error) {
	workCtx, finish := e.runtime.beginMigrationWork(
		ctx, e.migration.ID, databasemigration.PhaseRebuildDerived, table, derivedRebuildTimeout,
	)
	defer finish()
	if err := requireCurrentMigrationWork(workCtx, e.repository, e.migration.ID,
		databasemigration.PhaseRebuildDerived, e.migration.Generation); err != nil {
		return 0, err
	}
	started := time.Now()
	log.Printf("[database-migration] rebuilding MySQL derived table %s", table)
	rows, err := e.delegate.RebuildTable(workCtx, table, watermark)
	if err != nil {
		log.Printf("[database-migration] MySQL derived table %s stopped after %s: %v",
			table, time.Since(started).Round(time.Second), err)
		return 0, err
	}
	log.Printf("[database-migration] rebuilt MySQL derived table %s rows=%d duration=%s",
		table, rows, time.Since(started).Round(time.Second))
	return rows, nil
}
