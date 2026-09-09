package databasemanagement

import (
	"context"
	"errors"
	"fmt"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
)

// RouteSnapshot returns one coherent, immutable routing token from the
// encrypted control file. Application stores compare this token immediately
// before every authoritative write.
func (r *Runtime) RouteSnapshot(context.Context) (database.RouteSnapshot, error) {
	state, err := r.control.LoadCached()
	if errors.Is(err, control.ErrNotFound) {
		state = control.DefaultState()
		err = nil
	}
	if err != nil {
		return database.RouteSnapshot{}, err
	}
	return database.RouteSnapshot{
		Generation:          state.Generation,
		Epoch:               state.RoutingEpoch,
		WritePrimary:        state.WritePrimary,
		BusinessReadPrimary: state.BusinessReadPrimary,
		SystemReadPrimary:   state.SystemReadPrimary,
	}, nil
}

// AcquireBackend keeps the backend generation pinned until release. This is
// also the synchronization boundary used by MySQL reconnect and atomic SQLite
// cache replacement.
func (r *Runtime) AcquireBackend(
	_ context.Context,
	kind database.BackendKind,
) (database.Backend, func(), error) {
	r.backendMu.RLock()
	var backend database.Backend
	switch kind {
	case database.BackendSQLite:
		backend = r.sqlite
	case database.BackendMySQL:
		backend = r.mysql
	default:
		r.backendMu.RUnlock()
		return nil, func() {}, fmt.Errorf("unsupported database backend %q", kind)
	}
	if backend == nil || backend.DB() == nil {
		r.backendMu.RUnlock()
		return nil, func() {}, fmt.Errorf("%s backend is not connected", kind)
	}
	return backend, r.backendMu.RUnlock, nil
}

// CacheCoverage exposes the verified SQLite cache range used when a MySQL
// business read encounters a classified availability failure.
func (r *Runtime) CacheCoverage(ctx context.Context) (database.CacheCoverageStatus, error) {
	state, err := r.loadState()
	if err != nil {
		return database.CacheCoverageStatus{}, err
	}
	result := database.CacheCoverageStatus{
		RetentionDays:  state.CachePolicy.RetentionDays,
		CleanupEnabled: state.CachePolicy.Enabled,
	}
	backend, release, err := r.AcquireBackend(ctx, database.BackendSQLite)
	if err != nil {
		return result, err
	}
	defer release()
	repository := databasemigration.NewSQLRepository(backend.DB(), databasemigration.DialectSQLite)
	coverage, err := repository.CacheCoverage(ctx)
	if err != nil {
		if errors.Is(err, databasemigration.ErrNotFound) {
			return result, nil
		}
		return result, err
	}
	result.Complete = coverage.Complete
	result.FromMS = coverage.EarliestAtMS
	result.ToMS = coverage.LatestAtMS
	result.MinimumEventID = coverage.EarliestID
	result.MaximumEventID = coverage.LatestID
	result.SyncedWatermark = coverage.Watermark
	return result, nil
}
