package databasemanagement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/outboxcontext"
)

// SQLiteOutboxEnabler returns the process-owned installer used by Runtime.
// Keeping installation here makes the ordering explicit and testable: the
// migration metadata and routing fence exist before triggers are installed,
// and repository journaling is activated only after a complete audit.
func SQLiteOutboxEnabler(db *sql.DB) func(context.Context, uint64) error {
	return func(ctx context.Context, epoch uint64) error {
		return EnableSQLiteOutbox(ctx, db, epoch)
	}
}

// EnableSQLiteOutbox installs and activates journaling for every canonical
// authoritative SQLite table. It is idempotent for an already-installed
// contract and deliberately refuses to rewrite an inconsistent routing fence.
func EnableSQLiteOutbox(ctx context.Context, db *sql.DB, epoch uint64) error {
	if db == nil {
		return errors.New("enable sqlite outbox: database is required")
	}
	if epoch == 0 || epoch > math.MaxInt64 {
		return fmt.Errorf("enable sqlite outbox: invalid epoch %d", epoch)
	}
	repository := databasemigration.NewSQLRepository(db, databasemigration.DialectSQLite)
	if err := repository.EnsureSchema(ctx); err != nil {
		return fmt.Errorf("ensure sqlite migration metadata: %w", err)
	}
	routing, err := repository.Routing(ctx)
	if err != nil {
		return fmt.Errorf("read sqlite routing fence: %w", err)
	}
	if routing.WritePrimary != databasemigration.BackendSQLite || routing.Epoch != int64(epoch) {
		return fmt.Errorf("sqlite routing fence is %s epoch %d, want sqlite epoch %d",
			routing.WritePrimary, routing.Epoch, epoch)
	}
	if err := outboxcontext.Ensure(ctx, db); err != nil && !errors.Is(err, outboxcontext.ErrJournalNotReady) {
		return fmt.Errorf("ensure sqlite outbox context: %w", err)
	}
	provider := &outboxcontext.SQLiteProvider{SchemaVersion: schema.Current().Version}
	installer := databasemigration.SQLiteJournalInstaller{
		DB: db, Manifest: migrationManifest(), Provider: provider,
		SchemaVersion: schema.Current().Version,
	}
	if err := installer.Install(ctx); err != nil {
		return fmt.Errorf("install sqlite authoritative journal: %w", err)
	}
	if err := outboxcontext.Enable(ctx, db, int64(epoch)); err != nil {
		return fmt.Errorf("activate sqlite authoritative journal: %w", err)
	}
	return nil
}

// Initialize restores durable replication invariants after a process restart.
// A configured dual-write deployment must never resume business workers with
// missing or partially-installed authoritative journaling.
func (r *Runtime) Initialize(ctx context.Context) error {
	state, err := r.loadState()
	if err != nil {
		return err
	}
	if !state.ReplicationEnabled {
		return nil
	}
	if state.RoutingEpoch == 0 {
		return errors.New("replication is enabled with an invalid zero routing epoch")
	}
	if state.Failover.Status == "switching" {
		return r.recoverWriteFailover(ctx, state)
	}
	switch state.WritePrimary {
	case "sqlite":
		if r.sqliteDB() == nil {
			return errors.Join(ErrUnavailable, errors.New("sqlite write primary is unavailable"))
		}
		if r.enableOutbox == nil {
			return errors.Join(ErrUnavailable, errors.New("authoritative outbox instrumentation is not installed"))
		}
		return r.enableOutbox(ctx, state.RoutingEpoch)
	case "mysql":
		mysqlDB, release := r.mysqlDB()
		defer release()
		if mysqlDB == nil {
			// Start in an explicitly read-only/degraded state. The durable MySQL
			// triggers still fence stale epochs when connectivity returns.
			return nil
		}
		return outboxcontext.EnableMySQL(ctx, mysqlDB, int64(state.RoutingEpoch))
	default:
		return errors.New("database control contains an invalid write primary")
	}
}
