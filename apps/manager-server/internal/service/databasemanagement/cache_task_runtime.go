package databasemanagement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
)

const cacheCleanupPollInterval = time.Second

func (r *Runtime) cacheManager(repository *databasemigration.SQLRepository) databasemigration.CacheManager {
	return databasemigration.CacheManager{
		Store: repository,
		Data:  NewSQLiteCacheCleanupData(r.sqliteDB()),
	}
}

// cacheCleanupSafety reads every destructive-cleanup gate from its durable
// source. A zero backlog alone is insufficient: the MySQL Inbox watermark must
// equal the SQLite Outbox watermark and the successful cutover validation token
// must still match the completed migration referenced by the control file.
func (r *Runtime) cacheCleanupSafety(
	ctx context.Context,
	state control.State,
	repository *databasemigration.SQLRepository,
) (databasemigration.CleanupSafety, error) {
	safety := databasemigration.CleanupSafety{}
	if state.Migration.ID != "" {
		migration, err := repository.Migration(ctx, state.Migration.ID)
		if err != nil {
			return safety, err
		}
		safety.MigrationComplete = migration.Phase == databasemigration.PhaseCompleted &&
			migration.Status == databasemigration.StatusSucceeded &&
			state.BusinessReadPrimary == database.BackendMySQL
		if migration.Validation != nil {
			token := strings.TrimSpace(migration.ValidationToken)
			safety.ValidationPassed = migration.Validation.Passed && token != "" &&
				migration.Validation.Token == token && state.Migration.ValidationToken == token
			safety.ValidationToken = token
			safety.ValidationAtMS = migration.Validation.ValidatedAtMS
		}
	}

	pending, _, _, sourceWatermark, err := repository.OutboxBacklog(ctx)
	if err != nil {
		return safety, err
	}
	safety.SynchronizedWatermark = sourceWatermark
	mysqlDB, release := r.mysqlDB()
	defer release()
	if mysqlDB == nil {
		return safety, nil
	}
	pingCtx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	if err := mysqlDB.PingContext(pingCtx); err != nil {
		return safety, nil
	}
	safety.MySQLAvailable = true
	appliedWatermark, err := mysqlAppliedWatermark(pingCtx, mysqlDB)
	if err != nil {
		return safety, err
	}
	safety.ReplicationCaughtUp = state.ReplicationEnabled && pending == 0 &&
		appliedWatermark == sourceWatermark
	return safety, nil
}

func summarizeCacheCleanupPreview(preview databasemigration.CleanupPreview) CacheCleanupPreview {
	result := CacheCleanupPreview{Eligible: true, EstimatedRows: preview.EstimatedRows}
	for _, table := range preview.Tables {
		if table.Rows == 0 {
			continue
		}
		if table.FromMS > 0 && (result.CoverageFromMS == 0 || table.FromMS < result.CoverageFromMS) {
			result.CoverageFromMS = table.FromMS
		}
		if table.ToMS > result.CoverageToMS {
			result.CoverageToMS = table.ToMS
		}
	}
	return result
}

func (r *Runtime) runCacheCleanup(ctx context.Context) {
	ticker := time.NewTicker(cacheCleanupPollInterval)
	defer ticker.Stop()
	for {
		r.cleanupCacheOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// cleanupCacheOnce performs at most one bounded table batch. Running and
// paused tasks are recovered from SQLite after process restart. A paused task
// resumes only after every safety gate recovers and its validation token still
// matches; failed tasks remain terminal and visible to operators.
func (r *Runtime) cleanupCacheOnce(ctx context.Context) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if err := ctx.Err(); err != nil || r.sqliteDB() == nil {
		return
	}
	repository, err := r.metadataRepository(ctx, true)
	if err != nil {
		return
	}
	if _, rebuildActive, rebuildErr := repository.ActiveMaintenanceTask(ctx,
		databasemigration.TaskRebuild); rebuildErr != nil || rebuildActive {
		return
	}
	state, err := r.loadState()
	if err != nil {
		return
	}
	active, exists, err := repository.ActiveMaintenanceTask(ctx, databasemigration.TaskCleanup)
	if err != nil {
		return
	}
	safety, safetyErr := r.cacheCleanupSafety(ctx, state, repository)
	if safetyErr != nil {
		if exists && active.Status == databasemigration.StatusRunning {
			_, _ = repository.PauseMaintenanceTask(ctx, active.ID, active.Generation)
		}
		return
	}
	manager := r.cacheManager(repository)
	if exists {
		if active.ValidationToken != safety.ValidationToken {
			if active.Status == databasemigration.StatusRunning {
				_, _ = repository.PauseMaintenanceTask(ctx, active.ID, active.Generation)
			}
			return
		}
		if active.Status == databasemigration.StatusPaused {
			if databasemigration.EvaluateCleanupSafety(safety, time.Now()) != nil {
				return
			}
			active, err = repository.ResumeMaintenanceTask(ctx, active.ID, active.Generation)
			if err != nil {
				return
			}
		}
		_, _ = manager.RunCleanupBatch(ctx, active.ID, active.Generation, safety)
		return
	}

	// The policy switch controls daily scheduling only. A manually-created task
	// continues to run even when periodic scheduling is disabled.
	if !state.CachePolicy.Enabled || databasemigration.EvaluateCleanupSafety(safety, time.Now()) != nil {
		return
	}
	policy, err := repository.CachePolicy(ctx)
	if err != nil || !policy.Enabled {
		return
	}
	now := time.Now()
	dayStart := now.UTC().Truncate(24 * time.Hour).UnixMilli()
	latest, hasLatest, err := repository.LatestMaintenanceTask(ctx, databasemigration.TaskCleanup)
	if err != nil {
		return
	}
	if hasLatest && latest.Status == databasemigration.StatusSucceeded &&
		latest.FinishedAtMS >= dayStart && latest.RetentionDays == policy.RetentionDays {
		return
	}
	preview, err := manager.PreviewCleanup(ctx, policy.RetentionDays)
	if err != nil {
		return
	}
	key := scheduledCleanupKey(now, policy.RetentionDays, safety.ValidationToken)
	task, _, err := manager.StartCleanup(ctx, key, preview, policy.RetentionDays, safety)
	if err != nil || task.Status != databasemigration.StatusRunning {
		return
	}
	_, _ = manager.RunCleanupBatch(ctx, task.ID, task.Generation, safety)
}

func scheduledCleanupKey(now time.Time, retentionDays int, validationToken string) string {
	digest := sha256.Sum256([]byte(validationToken))
	return fmt.Sprintf("scheduled-sqlite-cleanup:%s:%d:%s", now.UTC().Format("2006-01-02"),
		retentionDays, hex.EncodeToString(digest[:8]))
}

func (r *Runtime) enrichCacheMaintenanceStatus(
	ctx context.Context,
	repository *databasemigration.SQLRepository,
	status *Status,
) {
	task, exists, err := repository.LatestMaintenanceTask(ctx, databasemigration.TaskCleanup)
	if err == nil && exists {
		status.Databases.SQLite.CleanupStatus = string(task.Status)
		switch task.Status {
		case databasemigration.StatusSucceeded:
			status.Databases.SQLite.LastCleanupAtMS = task.FinishedAtMS
		case databasemigration.StatusPaused:
			status.CacheCoverage.CleanupPaused = true
			status.CacheCoverage.PauseReason = "cleanup safety gate is closed; the task will resume after recovery"
		case databasemigration.StatusFailed:
			status.Databases.SQLite.LastError = task.LastError
		}
	}
	rebuild, rebuildExists, rebuildErr := repository.LatestMaintenanceTask(ctx,
		databasemigration.TaskRebuild)
	if rebuildErr != nil || !rebuildExists {
		return
	}
	status.Databases.SQLite.RebuildStatus = string(rebuild.Status)
	switch rebuild.Status {
	case databasemigration.StatusSucceeded:
		status.Databases.SQLite.LastRebuildAtMS = rebuild.FinishedAtMS
	case databasemigration.StatusPaused:
		status.CacheCoverage.CleanupPaused = true
		status.CacheCoverage.PauseReason = "cache rebuild safety gate is closed; the task will resume after recovery"
	case databasemigration.StatusFailed:
		status.Databases.SQLite.LastError = rebuild.LastError
	}
}
