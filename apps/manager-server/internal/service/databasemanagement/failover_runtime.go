package databasemanagement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/outboxcontext"
)

func (r *Runtime) failover(ctx context.Context, input FailoverMutation) (Status, error) {
	if err := input.MutationControl.Validate(); err != nil {
		return Status{}, err
	}
	if err := input.DangerousOperationConfirmation.Validate(
		database.BackendSQLite, database.BackendMySQL); err != nil {
		return Status{}, err
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	state, err := r.requireGeneration(input.ExpectedGeneration)
	if err != nil {
		return Status{}, err
	}
	if err := requireCurrentValidationConfirmation(state,
		input.DangerousOperationConfirmation); err != nil {
		return Status{}, err
	}
	if r.writeSwitchOK == nil || !r.writeSwitchOK(input.Target) {
		return Status{}, errors.Join(ErrUnavailable,
			errors.New("target write repository and epoch fence are not ready"))
	}
	repository, err := r.metadataRepository(ctx, true)
	if err != nil {
		return Status{}, err
	}
	if replay, ok, replayErr := replayStatus(ctx, repository, "routing_failover", input,
		input.IdempotencyKey); replayErr != nil || ok {
		return replay, replayErr
	}
	if state.WritePrimary == input.Target && state.Failover.Status != "switching" {
		status, statusErr := r.Status(ctx)
		if statusErr != nil {
			return Status{}, statusErr
		}
		if err := storeStatusReplay(ctx, repository, "routing_failover", input,
			input.IdempotencyKey, status); err != nil {
			return Status{}, err
		}
		return status, nil
	}
	if state.Failover.Status == "switching" && state.Failover.To != input.Target {
		return Status{}, errors.Join(ErrUnsafeOperation, fmt.Errorf(
			"write-primary switch from %s to %s is already in progress",
			state.Failover.From, state.Failover.To))
	}
	if input.Target == database.BackendMySQL && !state.ReplicationEnabled {
		return Status{}, errors.Join(ErrUnsafeOperation,
			errors.New("mysql cannot take writes before reliable replication is enabled"))
	}

	mysqlDB, releaseMySQL := r.mysqlDB()
	defer releaseMySQL()
	sqliteDB := r.sqliteDB()
	if input.Target == database.BackendMySQL && mysqlDB == nil {
		return Status{}, errors.Join(ErrUnavailable, errors.New("mysql is not connected"))
	}
	if input.Target == database.BackendSQLite && (sqliteDB == nil || mysqlDB == nil) {
		return Status{}, errors.Join(ErrUnavailable,
			errors.New("switching writes back to sqlite requires both databases"))
	}
	if mysqlDB != nil {
		if err := outboxcontext.AuditMySQLJournal(ctx, mysqlDB); err != nil {
			return Status{}, errors.Join(ErrUnsafeOperation,
				fmt.Errorf("mysql reverse outbox contract is not ready: %w", err))
		}
	}
	if sqliteDB != nil {
		if err := outboxcontext.Audit(ctx, sqliteDB); err != nil {
			return Status{}, errors.Join(ErrUnsafeOperation,
				fmt.Errorf("sqlite outbox contract is not ready: %w", err))
		}
	}

	releaseFences, err := acquireFailoverFences(ctx, sqliteDB, mysqlDB)
	if err != nil {
		return Status{}, err
	}
	defer releaseFences()

	sqliteRepository := sqlRepository(sqliteDB, databasemigration.DialectSQLite)
	mysqlRepository := sqlRepository(mysqlDB, databasemigration.DialectMySQL)
	if err := requireFailoverReplicationBoundary(ctx, state.WritePrimary,
		sqliteRepository, mysqlRepository); err != nil {
		return Status{}, errors.Join(ErrUnsafeOperation, err)
	}

	desiredEpoch, err := failoverDesiredEpoch(state)
	if err != nil {
		return Status{}, err
	}
	if state.Failover.Status != "switching" {
		next, updateErr := r.control.Update(input.ExpectedGeneration, func(current *control.State) error {
			if current.WritePrimary != state.WritePrimary || current.RoutingEpoch != state.RoutingEpoch {
				return control.ErrGenerationConflict
			}
			current.Failover = control.FailoverState{
				Status: "switching", From: state.WritePrimary, To: input.Target,
				Epoch: desiredEpoch, RequestedAtMS: time.Now().UnixMilli(),
			}
			return nil
		})
		if updateErr != nil {
			return Status{}, mapControlError(updateErr)
		}
		state = next
	} else if state.Failover.Epoch != desiredEpoch {
		return Status{}, errors.Join(ErrUnsafeOperation,
			errors.New("persisted failover epoch is inconsistent"))
	}

	if err := r.coordinateWritePrimary(ctx, input.Target, int64(desiredEpoch),
		sqliteDB, mysqlDB, sqliteRepository, mysqlRepository); err != nil {
		return Status{}, err
	}
	completedAtMS := time.Now().UnixMilli()
	next, err := r.control.Update(state.Generation, func(current *control.State) error {
		if current.Failover.Status != "switching" || current.Failover.To != input.Target ||
			current.Failover.Epoch != desiredEpoch {
			return control.ErrGenerationConflict
		}
		current.WritePrimary = input.Target
		current.RoutingEpoch = desiredEpoch
		current.Failover.Status = "completed"
		current.Failover.CompletedAtMS = completedAtMS
		current.Failover.Reason = ""
		return nil
	})
	if err != nil {
		return Status{}, mapControlError(err)
	}
	status, err := r.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	status.Generation = next.Generation
	if err := storeStatusReplay(ctx, repository, "routing_failover", input,
		input.IdempotencyKey, status); err != nil {
		return Status{}, err
	}
	return status, nil
}

func (r *Runtime) recoverWriteFailover(ctx context.Context, state control.State) error {
	if state.Failover.Status != "switching" {
		return nil
	}
	if state.Failover.To != database.BackendSQLite && state.Failover.To != database.BackendMySQL {
		return errors.New("persisted failover has an invalid target")
	}
	if r.writeSwitchOK == nil || !r.writeSwitchOK(state.Failover.To) {
		return errors.New("persisted failover cannot resume before the target repository gate is ready")
	}
	desiredEpoch, err := failoverDesiredEpoch(state)
	if err != nil {
		return err
	}
	mysqlDB, releaseMySQL := r.mysqlDB()
	defer releaseMySQL()
	sqliteDB := r.sqliteDB()
	if state.Failover.To == database.BackendMySQL && mysqlDB == nil {
		return errors.New("cannot recover mysql write switch while mysql is unavailable")
	}
	if state.Failover.To == database.BackendSQLite && (sqliteDB == nil || mysqlDB == nil) {
		return errors.New("cannot recover sqlite write switch without both databases")
	}
	releaseFences, err := acquireFailoverFences(ctx, sqliteDB, mysqlDB)
	if err != nil {
		return err
	}
	defer releaseFences()
	if err := r.coordinateWritePrimary(ctx, state.Failover.To, int64(desiredEpoch),
		sqliteDB, mysqlDB,
		sqlRepository(sqliteDB, databasemigration.DialectSQLite),
		sqlRepository(mysqlDB, databasemigration.DialectMySQL)); err != nil {
		return fmt.Errorf("recover persisted write-primary switch: %w", err)
	}
	_, err = r.control.Update(state.Generation, func(current *control.State) error {
		if current.Failover.Status != "switching" || current.Failover.To != state.Failover.To ||
			current.Failover.Epoch != desiredEpoch {
			return control.ErrGenerationConflict
		}
		current.WritePrimary = state.Failover.To
		current.RoutingEpoch = desiredEpoch
		current.Failover.Status = "completed"
		current.Failover.CompletedAtMS = time.Now().UnixMilli()
		current.Failover.Reason = ""
		return nil
	})
	return mapControlError(err)
}

func sqlRepository(db *sql.DB, dialect databasemigration.Dialect) *databasemigration.SQLRepository {
	if db == nil {
		return nil
	}
	return databasemigration.NewSQLRepository(db, dialect)
}

func acquireFailoverFences(ctx context.Context, sqliteDB, mysqlDB *sql.DB) (func(), error) {
	releases := make([]func(), 0, 2)
	releaseAll := func() {
		for index := len(releases) - 1; index >= 0; index-- {
			releases[index]()
		}
	}
	for _, db := range []*sql.DB{sqliteDB, mysqlDB} {
		if db == nil {
			continue
		}
		release, err := outboxcontext.AcquireWriteFence(ctx, db)
		if err != nil {
			releaseAll()
			return nil, err
		}
		releases = append(releases, release)
	}
	return releaseAll, nil
}

func failoverDesiredEpoch(state control.State) (uint64, error) {
	if state.Failover.Status == "switching" {
		if state.Failover.Epoch == 0 || state.Failover.Epoch > math.MaxInt64 {
			return 0, errors.New("persisted failover has an invalid fencing epoch")
		}
		return state.Failover.Epoch, nil
	}
	if state.RoutingEpoch == 0 || state.RoutingEpoch >= math.MaxInt64 {
		return 0, errors.New("database routing fencing epoch is invalid or exhausted")
	}
	return state.RoutingEpoch + 1, nil
}

func requireFailoverReplicationBoundary(
	ctx context.Context,
	current database.BackendKind,
	sqliteRepository, mysqlRepository *databasemigration.SQLRepository,
) error {
	var source *databasemigration.SQLRepository
	switch current {
	case database.BackendSQLite:
		// An unavailable SQLite is the explicit emergency takeover case. Its
		// unsent tail cannot be inspected; the administrator's manual action is
		// still fenced, while the UI continues to expose the last known backlog.
		if sqliteRepository == nil {
			return nil
		}
		source = sqliteRepository
	case database.BackendMySQL:
		if mysqlRepository == nil || sqliteRepository == nil {
			return errors.New("both databases are required to return writes to sqlite")
		}
		source = mysqlRepository
	default:
		return fmt.Errorf("invalid current write primary %q", current)
	}
	pending, _, _, sourceWatermark, err := source.OutboxBacklog(ctx)
	if err != nil {
		return fmt.Errorf("read %s outbox boundary: %w", current, err)
	}
	if pending != 0 {
		return fmt.Errorf("%s outbox still has %d pending mutations", current, pending)
	}
	if current == database.BackendMySQL {
		targetWatermark, err := sqliteRepository.InboxWatermark(ctx,
			databasemigration.BackendMySQL)
		if err != nil {
			return fmt.Errorf("read sqlite reverse Inbox watermark: %w", err)
		}
		if targetWatermark != sourceWatermark {
			return fmt.Errorf("mysql reverse replication is not caught up: source=%d target=%d",
				sourceWatermark, targetWatermark)
		}
	}
	return nil
}

func (r *Runtime) coordinateWritePrimary(
	ctx context.Context,
	target database.BackendKind,
	desiredEpoch int64,
	sqliteDB, mysqlDB *sql.DB,
	sqliteRepository, mysqlRepository *databasemigration.SQLRepository,
) error {
	switch target {
	case database.BackendMySQL:
		if mysqlDB == nil || mysqlRepository == nil {
			return errors.Join(ErrUnavailable, errors.New("mysql is unavailable"))
		}
		if err := outboxcontext.AuditMySQLJournal(ctx, mysqlDB); err != nil {
			return err
		}
		if sqliteDB != nil {
			if err := outboxcontext.DisableSQLite(ctx, sqliteDB); err != nil {
				return err
			}
			if err := synchronizeWriteRouting(ctx, sqliteRepository,
				databasemigration.BackendMySQL, desiredEpoch); err != nil {
				return fmt.Errorf("fence sqlite write primary: %w", err)
			}
		}
		if err := synchronizeWriteRouting(ctx, mysqlRepository,
			databasemigration.BackendMySQL, desiredEpoch); err != nil {
			return fmt.Errorf("activate mysql routing fence: %w", err)
		}
		if err := outboxcontext.EnableMySQL(ctx, mysqlDB, desiredEpoch); err != nil {
			return err
		}
	case database.BackendSQLite:
		if sqliteDB == nil || sqliteRepository == nil || mysqlDB == nil || mysqlRepository == nil {
			return errors.Join(ErrUnavailable, errors.New("both databases are required"))
		}
		if err := outboxcontext.Audit(ctx, sqliteDB); err != nil {
			return err
		}
		if err := synchronizeWriteRouting(ctx, mysqlRepository,
			databasemigration.BackendSQLite, desiredEpoch); err != nil {
			return fmt.Errorf("fence mysql write primary: %w", err)
		}
		outboxcontext.DisableMySQL(mysqlDB)
		if err := synchronizeWriteRouting(ctx, sqliteRepository,
			databasemigration.BackendSQLite, desiredEpoch); err != nil {
			return fmt.Errorf("activate sqlite routing fence: %w", err)
		}
		if err := outboxcontext.Enable(ctx, sqliteDB, desiredEpoch); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid write-primary target %q", target)
	}
	return nil
}

func synchronizeWriteRouting(
	ctx context.Context,
	repository *databasemigration.SQLRepository,
	target databasemigration.Backend,
	desiredEpoch int64,
) error {
	if repository == nil {
		return errors.New("routing repository is unavailable")
	}
	routing, err := repository.Routing(ctx)
	if err != nil {
		return err
	}
	if routing.WritePrimary == target {
		if routing.Epoch != desiredEpoch {
			return fmt.Errorf("routing already points to %s at epoch %d, want %d",
				target, routing.Epoch, desiredEpoch)
		}
		return nil
	}
	if routing.Epoch+1 != desiredEpoch {
		return fmt.Errorf("routing epoch %d cannot advance atomically to %d",
			routing.Epoch, desiredEpoch)
	}
	next, err := repository.CompareAndSwapRouting(ctx, routing.Generation, routing.Epoch,
		databasemigration.RoutingChange{WritePrimary: target,
			BusinessRead: routing.BusinessRead, SystemRead: routing.SystemRead})
	if err != nil {
		return err
	}
	if next.WritePrimary != target || next.Epoch != desiredEpoch {
		return fmt.Errorf("routing CAS returned %s epoch %d, want %s epoch %d",
			next.WritePrimary, next.Epoch, target, desiredEpoch)
	}
	return nil
}
