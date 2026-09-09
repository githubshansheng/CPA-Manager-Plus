package databasemanagement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/outboxcontext"
)

// SQLiteCacheCleanupData is the fixed, non-injectable authoritative cache
// retention plan. Tables without a proven terminal predicate remain visible in
// Preview as protected and DeleteBatch treats them as completed no-ops.
type SQLiteCacheCleanupData struct {
	db *sql.DB
}

func NewSQLiteCacheCleanupData(db *sql.DB) *SQLiteCacheCleanupData {
	return &SQLiteCacheCleanupData{db: db}
}

var sqliteCacheCleanupOrder = []string{
	"usage_events",
	"account_action_candidates",
	"quota_cooldowns",
	"codex_inspection_results",
	"codex_inspection_logs",
	"codex_inspection_runs",
	"account_quota_snapshots",
	"account_quota_cycles",
	"account_quota_window_activations",
	"account_quota_windows",
	// These authoritative tables deliberately remain protected until their
	// lifecycle has an unambiguous compact tombstone or terminal predicate.
	"account_quota_observations",
	"dead_letter_events",
	"codex_inspection_leases",
	"codex_inspection_disable_ownership",
}

func (data *SQLiteCacheCleanupData) Preview(
	ctx context.Context,
	cutoffMS int64,
) ([]databasemigration.TableCleanupPreview, error) {
	if data == nil || data.db == nil {
		return nil, errors.New("sqlite cache cleanup requires a database")
	}
	if cutoffMS <= 0 {
		return nil, errors.New("sqlite cache cleanup requires a positive cutoff")
	}
	result := make([]databasemigration.TableCleanupPreview, 0, len(sqliteCacheCleanupOrder))
	for _, table := range sqliteCacheCleanupOrder {
		preview, err := data.previewTable(ctx, table, cutoffMS)
		if err != nil {
			return nil, fmt.Errorf("preview sqlite cache table %s: %w", table, err)
		}
		result = append(result, preview)
	}
	return result, nil
}

func (data *SQLiteCacheCleanupData) previewTable(
	ctx context.Context,
	table string,
	cutoffMS int64,
) (databasemigration.TableCleanupPreview, error) {
	preview := databasemigration.TableCleanupPreview{Table: table}
	oldQuery, oldArgs, eligibleQuery, eligibleArgs, rangeQuery, rangeArgs := cleanupPreviewQueries(table, cutoffMS)
	if oldQuery == "" {
		return preview, fmt.Errorf("table is not in the fixed cleanup plan")
	}
	var oldRows int64
	if err := data.db.QueryRowContext(ctx, oldQuery, oldArgs...).Scan(&oldRows); err != nil {
		return preview, err
	}
	if eligibleQuery != "" {
		if table == "usage_events" {
			stable, err := usageCleanupStateStable(ctx, data.db)
			if err != nil {
				return preview, err
			}
			if !stable {
				preview.ProtectedRows = oldRows
				return preview, nil
			}
		}
		if err := data.db.QueryRowContext(ctx, eligibleQuery, eligibleArgs...).Scan(&preview.Rows); err != nil {
			return preview, err
		}
	}
	preview.ProtectedRows = oldRows - preview.Rows
	if preview.ProtectedRows < 0 {
		return preview, errors.New("eligible cleanup rows exceed old rows")
	}
	if preview.Rows > 0 && rangeQuery != "" {
		if err := data.db.QueryRowContext(ctx, rangeQuery, rangeArgs...).Scan(&preview.FromMS, &preview.ToMS); err != nil {
			return preview, err
		}
	}
	return preview, nil
}

func usageCleanupStateStable(ctx context.Context, db *sql.DB) (bool, error) {
	var status string
	err := db.QueryRowContext(ctx, `SELECT status FROM usage_data_migrations
		WHERE name='usage_cache_accounting_v2'`).Scan(&status)
	if err != nil {
		return false, err
	}
	return status == "completed" || status == "clearing", nil
}

func (data *SQLiteCacheCleanupData) DeleteBatch(
	ctx context.Context,
	table string,
	constraints databasemigration.CleanupConstraints,
) (databasemigration.CleanupBatchResult, error) {
	result := databasemigration.CleanupBatchResult{Table: table}
	if data == nil || data.db == nil {
		return result, errors.New("sqlite cache cleanup requires a database")
	}
	if constraints.CutoffMS <= 0 || constraints.Limit <= 0 {
		return result, errors.New("sqlite cache cleanup requires positive cutoff and limit")
	}
	if !constraints.ProtectActiveAndIncomplete {
		return result, errors.New("sqlite cache cleanup cannot disable active/incomplete protection")
	}
	if !knownCleanupTable(table) {
		return result, fmt.Errorf("sqlite cache cleanup table %q is not in the fixed plan", table)
	}

	maintenance, err := outboxcontext.BeginLocalCacheMaintenance(ctx, data.db)
	if err != nil {
		return result, err
	}
	defer func() { _ = maintenance.Rollback() }()
	if err := maintenance.AssertReplicationWatermark(ctx, constraints.SynchronizedWatermark); err != nil {
		return result, err
	}

	if protectedCleanupTable(table) {
		coverage, err := maintenance.RefreshCacheCoverage(ctx, constraints.SynchronizedWatermark, false)
		if err != nil {
			return result, err
		}
		result.Coverage = coverage
		result.Done = true
		if err := maintenance.Commit(); err != nil {
			return result, err
		}
		return result, nil
	}

	if table == "usage_events" {
		return data.deleteUsageBatch(ctx, maintenance, constraints)
	}
	predicate, args := cleanupDeletePredicate(table, constraints.CutoffMS, constraints.Limit)
	if predicate == "" {
		return result, fmt.Errorf("sqlite cache cleanup table %q has no safe predicate", table)
	}
	deletedResult, err := maintenance.DeleteWhere(ctx, table, predicate, args...)
	if err != nil {
		return result, err
	}
	result.Deleted, err = deletedResult.RowsAffected()
	if err != nil {
		return result, err
	}
	// A zero-row pass is the decisive completion check. This intentionally
	// costs one extra call after a non-empty batch, and handles quota parents
	// which become eligible only after their last child was deleted.
	result.Done = result.Deleted == 0
	result.Coverage, err = maintenance.RefreshCacheCoverage(ctx, constraints.SynchronizedWatermark, false)
	if err != nil {
		return result, err
	}
	if err := maintenance.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func (data *SQLiteCacheCleanupData) deleteUsageBatch(
	ctx context.Context,
	maintenance *outboxcontext.LocalCacheMaintenance,
	constraints databasemigration.CleanupConstraints,
) (databasemigration.CleanupBatchResult, error) {
	result := databasemigration.CleanupBatchResult{Table: "usage_events"}
	preparation, err := maintenance.PrepareUsageEventCleanup(ctx, constraints.CutoffMS, constraints.Limit)
	if err != nil {
		return result, err
	}
	if !preparation.HasCandidates {
		result.Done = true
		result.Coverage, err = maintenance.RefreshCacheCoverage(ctx, constraints.SynchronizedWatermark, false)
		if err != nil {
			return result, err
		}
		if err := maintenance.Commit(); err != nil {
			return result, err
		}
		return result, nil
	}
	if !preparation.Ready {
		result.Coverage, err = maintenance.RefreshCacheCoverage(ctx, constraints.SynchronizedWatermark, false)
		if err != nil {
			return result, err
		}
		if err := maintenance.Commit(); err != nil {
			return result, err
		}
		return result, nil
	}

	candidate := `SELECT id FROM usage_events WHERE timestamp_ms < ?
		ORDER BY timestamp_ms, id LIMIT ?`
	if _, err := maintenance.DeleteDerivedWhere(ctx, "usage_monitoring_event_projection_v1",
		`event_id IN (`+candidate+`)`, constraints.CutoffMS, constraints.Limit); err != nil {
		return result, err
	}
	if _, err := maintenance.DeleteDerivedWhere(ctx, "usage_monitoring_header_latest_v1",
		`event_id IN (`+candidate+`)`, constraints.CutoffMS, constraints.Limit); err != nil {
		return result, err
	}
	deletedResult, err := maintenance.DeleteWhere(ctx, "usage_events",
		`id IN (`+candidate+`)`, constraints.CutoffMS, constraints.Limit)
	if err != nil {
		return result, err
	}
	result.Deleted, err = deletedResult.RowsAffected()
	if err != nil {
		return result, err
	}
	result.Done, err = maintenance.MarkUsageDerivedDirty(ctx, constraints.CutoffMS)
	if err != nil {
		return result, err
	}
	result.Coverage, err = maintenance.RefreshCacheCoverage(ctx, constraints.SynchronizedWatermark, false)
	if err != nil {
		return result, err
	}
	if err := maintenance.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func knownCleanupTable(table string) bool {
	for _, candidate := range sqliteCacheCleanupOrder {
		if candidate == table {
			return true
		}
	}
	return false
}

func protectedCleanupTable(table string) bool {
	switch table {
	case "account_quota_observations", "dead_letter_events", "codex_inspection_leases",
		"codex_inspection_disable_ownership":
		return true
	default:
		return false
	}
}

func cleanupPreviewQueries(table string, cutoffMS int64) (
	oldQuery string,
	oldArgs []any,
	eligibleQuery string,
	eligibleArgs []any,
	rangeQuery string,
	rangeArgs []any,
) {
	var oldWhere, eligibleWhere, anchor string
	switch table {
	case "usage_events":
		anchor = "timestamp_ms"
		oldWhere = `timestamp_ms < ?`
		eligibleWhere = oldWhere
	case "account_action_candidates":
		anchor = "updated_at_ms"
		oldWhere = `updated_at_ms < ?`
		eligibleWhere = `updated_at_ms < ? AND status IN ('ignored','resolved','deleted')`
	case "quota_cooldowns":
		anchor = `COALESCE(recovered_at_ms,updated_at_ms)`
		oldWhere = anchor + ` < ?`
		eligibleWhere = oldWhere + ` AND status IN ('recovered','skipped')`
	case "codex_inspection_results":
		anchor = `r.finished_at_ms`
		oldQuery = `SELECT COUNT(*) FROM codex_inspection_results item
			JOIN codex_inspection_runs r ON r.id=item.run_id
			WHERE COALESCE(r.finished_at_ms,r.started_at_ms) < ?`
		eligibleWhere = inspectionRunEligibleWhere("r")
		eligibleQuery = `SELECT COUNT(*) FROM codex_inspection_results item
			JOIN codex_inspection_runs r ON r.id=item.run_id WHERE ` + eligibleWhere
		rangeQuery = `SELECT COALESCE(MIN(r.finished_at_ms),0),COALESCE(MAX(r.finished_at_ms),0)
			FROM codex_inspection_results item JOIN codex_inspection_runs r ON r.id=item.run_id
			WHERE ` + eligibleWhere
		return oldQuery, []any{cutoffMS}, eligibleQuery, []any{cutoffMS}, rangeQuery, []any{cutoffMS}
	case "codex_inspection_logs":
		anchor = `r.finished_at_ms`
		oldQuery = `SELECT COUNT(*) FROM codex_inspection_logs item
			JOIN codex_inspection_runs r ON r.id=item.run_id
			WHERE COALESCE(r.finished_at_ms,r.started_at_ms) < ?`
		eligibleWhere = inspectionRunEligibleWhere("r")
		eligibleQuery = `SELECT COUNT(*) FROM codex_inspection_logs item
			JOIN codex_inspection_runs r ON r.id=item.run_id WHERE ` + eligibleWhere
		rangeQuery = `SELECT COALESCE(MIN(r.finished_at_ms),0),COALESCE(MAX(r.finished_at_ms),0)
			FROM codex_inspection_logs item JOIN codex_inspection_runs r ON r.id=item.run_id
			WHERE ` + eligibleWhere
		return oldQuery, []any{cutoffMS}, eligibleQuery, []any{cutoffMS}, rangeQuery, []any{cutoffMS}
	case "codex_inspection_runs":
		anchor = `finished_at_ms`
		oldWhere = `COALESCE(finished_at_ms,started_at_ms) < ?`
		eligibleWhere = inspectionRunEligibleWhere("codex_inspection_runs")
	case "account_quota_snapshots":
		anchor = `observed_at_ms`
		oldWhere = `observed_at_ms < ?`
		eligibleWhere = quotaSnapshotEligibleWhere()
	case "account_quota_cycles":
		anchor = `actual_end_ms`
		oldWhere = `COALESCE(actual_end_ms,actual_start_ms) < ?`
		eligibleWhere = quotaCycleEligibleWhere()
	case "account_quota_window_activations":
		anchor = `deactivated_at_ms`
		oldWhere = `COALESCE(deactivated_at_ms,activated_at_ms) < ?`
		eligibleWhere = quotaActivationEligibleWhere()
	case "account_quota_windows":
		anchor = `deactivated_at_ms`
		oldWhere = `COALESCE(deactivated_at_ms,last_seen_at_ms) < ?`
		eligibleWhere = quotaWindowEligibleWhere()
	case "account_quota_observations":
		anchor = `observed_at_ms`
		oldWhere = `observed_at_ms < ?`
	case "dead_letter_events":
		anchor = `created_at_ms`
		oldWhere = `created_at_ms < ?`
	case "codex_inspection_leases":
		anchor = `heartbeat_at_ms`
		oldWhere = `heartbeat_at_ms < ?`
	case "codex_inspection_disable_ownership":
		anchor = `updated_at_ms`
		oldWhere = `updated_at_ms < ?`
	default:
		return "", nil, "", nil, "", nil
	}
	oldQuery = `SELECT COUNT(*) FROM "` + table + `" WHERE ` + oldWhere
	oldArgs = []any{cutoffMS}
	if eligibleWhere == "" {
		return oldQuery, oldArgs, "", nil, "", nil
	}
	eligibleQuery = `SELECT COUNT(*) FROM "` + table + `" WHERE ` + eligibleWhere
	eligibleArgs = cleanupPredicateArgs(table, cutoffMS)
	rangeQuery = `SELECT COALESCE(MIN(` + anchor + `),0),COALESCE(MAX(` + anchor + `),0)
		FROM "` + table + `" WHERE ` + eligibleWhere
	rangeArgs = cleanupPredicateArgs(table, cutoffMS)
	return
}

func cleanupPredicateArgs(table string, cutoffMS int64) []any {
	if table == "account_quota_cycles" {
		return []any{cutoffMS, cutoffMS}
	}
	return []any{cutoffMS}
}

func cleanupDeletePredicate(table string, cutoffMS int64, limit int) (string, []any) {
	var query string
	switch table {
	case "account_action_candidates":
		query = `SELECT id FROM account_action_candidates WHERE updated_at_ms < ?
			AND status IN ('ignored','resolved','deleted') ORDER BY updated_at_ms,id LIMIT ?`
	case "quota_cooldowns":
		query = `SELECT id FROM quota_cooldowns WHERE COALESCE(recovered_at_ms,updated_at_ms) < ?
			AND status IN ('recovered','skipped') ORDER BY COALESCE(recovered_at_ms,updated_at_ms),id LIMIT ?`
	case "codex_inspection_results":
		query = `SELECT item.id FROM codex_inspection_results item
			JOIN codex_inspection_runs r ON r.id=item.run_id
			WHERE ` + inspectionRunEligibleWhere("r") + ` ORDER BY item.id LIMIT ?`
	case "codex_inspection_logs":
		query = `SELECT item.id FROM codex_inspection_logs item
			JOIN codex_inspection_runs r ON r.id=item.run_id
			WHERE ` + inspectionRunEligibleWhere("r") + ` ORDER BY item.id LIMIT ?`
	case "codex_inspection_runs":
		query = `SELECT id FROM codex_inspection_runs WHERE ` + inspectionRunDeleteWhere() + ` ORDER BY finished_at_ms,id LIMIT ?`
	case "account_quota_snapshots":
		query = `SELECT id FROM account_quota_snapshots WHERE ` + quotaSnapshotEligibleWhere() + ` ORDER BY observed_at_ms,id LIMIT ?`
	case "account_quota_cycles":
		query = `SELECT id FROM account_quota_cycles WHERE ` + quotaCycleEligibleWhere() + ` ORDER BY actual_end_ms,id LIMIT ?`
	case "account_quota_window_activations":
		query = `SELECT id FROM account_quota_window_activations WHERE ` + quotaActivationEligibleWhere() + ` ORDER BY deactivated_at_ms,id LIMIT ?`
	case "account_quota_windows":
		query = `SELECT id FROM account_quota_windows WHERE ` + quotaWindowEligibleWhere() + ` ORDER BY deactivated_at_ms,id LIMIT ?`
	default:
		return "", nil
	}
	args := cleanupPredicateArgs(table, cutoffMS)
	args = append(args, limit)
	return `id IN (` + query + `)`, args
}

func inspectionRunEligibleWhere(alias string) string {
	return alias + `.status IN ('completed','failed','cancelled','interrupted')
		AND ` + alias + `.finished_at_ms IS NOT NULL AND ` + alias + `.finished_at_ms < ?
		AND NOT EXISTS (SELECT 1 FROM codex_inspection_results pending
			WHERE pending.run_id=` + alias + `.id
			AND COALESCE(pending.action_status,'') IN ('pending','needs_review'))
		AND NOT EXISTS (SELECT 1 FROM codex_inspection_leases lease WHERE lease.run_id=` + alias + `.id)`
}

func inspectionRunDeleteWhere() string {
	return inspectionRunEligibleWhere("codex_inspection_runs") + `
		AND NOT EXISTS (SELECT 1 FROM codex_inspection_results child
			WHERE child.run_id=codex_inspection_runs.id)
		AND NOT EXISTS (SELECT 1 FROM codex_inspection_logs child
			WHERE child.run_id=codex_inspection_runs.id)`
}

func quotaSnapshotEligibleWhere() string {
	return `observed_at_ms < ? AND observation_id IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM account_quota_observations observation
			WHERE observation.id=account_quota_snapshots.observation_id AND (
				observation.lifecycle_applied=0 OR observation.observed_at_ms=(
					SELECT MAX(current.observed_at_ms) FROM account_quota_observations current
					WHERE current.account_key=observation.account_key
						AND current.provider=observation.provider
						AND current.inventory_scope_key=observation.inventory_scope_key
						AND current.lifecycle_applied=1)))
		AND NOT EXISTS (SELECT 1 FROM account_quota_windows window
			WHERE window.id=account_quota_snapshots.logical_window_id
				AND (window.availability<>'inactive' OR window.deactivated_at_ms IS NULL))
		AND NOT EXISTS (SELECT 1 FROM account_quota_window_activations activation
			WHERE activation.id=account_quota_snapshots.activation_id
				AND activation.deactivated_at_ms IS NULL)
		AND NOT EXISTS (SELECT 1 FROM account_quota_cycles cycle
			WHERE cycle.id=account_quota_snapshots.cycle_id AND cycle.actual_end_ms IS NULL)`
}

func quotaCycleEligibleWhere() string {
	return `actual_end_ms IS NOT NULL AND actual_end_ms < ?
		AND EXISTS (SELECT 1 FROM account_quota_window_activations activation
			WHERE activation.id=account_quota_cycles.activation_id
				AND activation.deactivated_at_ms IS NOT NULL
				AND activation.deactivated_at_ms < ?)
		AND NOT EXISTS (SELECT 1 FROM account_quota_snapshots snapshot
			WHERE snapshot.cycle_id=account_quota_cycles.id)
		AND NOT EXISTS (SELECT 1 FROM account_quota_cycles child
			WHERE child.parent_cycle_id=account_quota_cycles.id)`
}

func quotaActivationEligibleWhere() string {
	return `status='inactive' AND deactivated_at_ms IS NOT NULL AND deactivated_at_ms < ?
		AND NOT EXISTS (SELECT 1 FROM account_quota_cycles cycle
			WHERE cycle.activation_id=account_quota_window_activations.id)
		AND NOT EXISTS (SELECT 1 FROM account_quota_snapshots snapshot
			WHERE snapshot.activation_id=account_quota_window_activations.id)`
}

func quotaWindowEligibleWhere() string {
	return `availability='inactive' AND deactivated_at_ms IS NOT NULL AND deactivated_at_ms < ?
		AND NOT EXISTS (SELECT 1 FROM account_quota_window_activations activation
			WHERE activation.window_id=account_quota_windows.id)
		AND NOT EXISTS (SELECT 1 FROM account_quota_snapshots snapshot
			WHERE snapshot.logical_window_id=account_quota_windows.id)`
}
