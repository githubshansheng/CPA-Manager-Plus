package databasemanagement

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
)

// AcquireSQLiteSourceSwitch serializes a source-selection preflight or
// mutation with every database topology operation. The returned release
// function must be called by the HTTP boundary after the source process lock
// has been released and any pending selection has been durably written.
func (r *Runtime) AcquireSQLiteSourceSwitch(ctx context.Context, expectedGeneration uint64) (func(), error) {
	if expectedGeneration == 0 {
		return nil, errors.Join(ErrInvalidRequest, errors.New("expectedGeneration is required"))
	}
	r.operationMu.Lock()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(r.operationMu.Unlock) }
	fail := func(err error) (func(), error) {
		release()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	state, err := r.requireGeneration(expectedGeneration)
	if err != nil {
		return fail(err)
	}
	if state.WritePrimary != database.BackendSQLite ||
		state.BusinessReadPrimary != database.BackendSQLite ||
		state.SystemReadPrimary != database.BackendSQLite ||
		state.ReplicationEnabled || strings.TrimSpace(state.MySQL.Host) != "" ||
		strings.TrimSpace(state.Migration.ID) != "" ||
		strings.EqualFold(strings.TrimSpace(state.Failover.Status), "switching") {
		return fail(errors.Join(
			ErrSQLiteSourceUnsafe,
			fmt.Errorf(
				"write=%s businessRead=%s systemRead=%s replication=%t mysqlConfigured=%t migration=%q failover=%q",
				state.WritePrimary,
				state.BusinessReadPrimary,
				state.SystemReadPrimary,
				state.ReplicationEnabled,
				strings.TrimSpace(state.MySQL.Host) != "",
				state.Migration.ID,
				state.Failover.Status,
			),
		))
	}
	status, err := r.Status(ctx)
	if err != nil {
		return fail(fmt.Errorf("inspect SQLite maintenance state: %w", err))
	}
	if sqliteSourceTaskActive(status.Databases.SQLite.CleanupStatus) ||
		sqliteSourceTaskActive(status.Databases.SQLite.RebuildStatus) {
		return fail(errors.Join(
			ErrSQLiteSourceUnsafe,
			fmt.Errorf(
				"SQLite cleanup=%q rebuild=%q; wait for all cache tasks to finish",
				status.Databases.SQLite.CleanupStatus,
				status.Databases.SQLite.RebuildStatus,
			),
		))
	}
	return release, nil
}

func sqliteSourceTaskActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "idle", "disabled", "succeeded", "failed", "canceled", "cancelled", "completed":
		return false
	default:
		return true
	}
}
