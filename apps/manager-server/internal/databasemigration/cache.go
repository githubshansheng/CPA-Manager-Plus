package databasemigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type CreateMaintenanceTaskRequest struct {
	ID              string
	IdempotencyKey  string
	Kind            MaintenanceTaskKind
	RetentionDays   int
	ValidationToken string
	Preview         CleanupPreview
}

func (r *SQLRepository) CachePolicy(ctx context.Context) (CachePolicy, error) {
	var policy CachePolicy
	var enabled int
	err := r.db.QueryRowContext(ctx, `SELECT generation, enabled, retention_days,
		batch_size, updated_at_ms FROM database_cache_policy WHERE id = 1`).Scan(&policy.Generation,
		&enabled, &policy.RetentionDays, &policy.BatchSize, &policy.UpdatedAtMS)
	policy.Enabled = enabled != 0
	return policy, err
}

func (r *SQLRepository) SetCachePolicy(ctx context.Context, expectedGeneration int64, enabled bool, retentionDays, batchSize int) (CachePolicy, error) {
	if retentionDays < 1 {
		return CachePolicy{}, errors.New("sqlite cache retention must be at least one day")
	}
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	current, err := r.CachePolicy(ctx)
	if err != nil {
		return CachePolicy{}, err
	}
	if current.Generation != expectedGeneration {
		return CachePolicy{}, &ConflictError{Kind: ErrGenerationConflict, Expected: expectedGeneration, Actual: current.Generation}
	}
	nowMS := r.now().UnixMilli()
	result, err := r.db.ExecContext(ctx, `UPDATE database_cache_policy SET generation = ?,
		enabled = ?, retention_days = ?, batch_size = ?, updated_at_ms = ? WHERE id = 1 AND generation = ?`,
		current.Generation+1, boolInt(enabled), retentionDays, batchSize, nowMS, expectedGeneration)
	if err != nil {
		return CachePolicy{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return CachePolicy{}, ErrGenerationConflict
	}
	return CachePolicy{Generation: current.Generation + 1, Enabled: enabled, RetentionDays: retentionDays,
		BatchSize: batchSize, UpdatedAtMS: nowMS}, nil
}

func (r *SQLRepository) CacheCoverage(ctx context.Context) (CacheCoverage, error) {
	var coverage CacheCoverage
	var complete int
	err := r.db.QueryRowContext(ctx, `SELECT earliest_at_ms, latest_at_ms, earliest_id,
		latest_id, watermark, complete, updated_at_ms FROM database_cache_coverage WHERE id = 1`).Scan(
		&coverage.EarliestAtMS, &coverage.LatestAtMS, &coverage.EarliestID, &coverage.LatestID,
		&coverage.Watermark, &complete, &coverage.UpdatedAtMS)
	coverage.Complete = complete != 0
	return coverage, err
}

func (r *SQLRepository) SaveCacheCoverage(ctx context.Context, expectedUpdatedAtMS int64, coverage CacheCoverage) (CacheCoverage, error) {
	if coverage.EarliestAtMS < 0 || coverage.LatestAtMS < 0 || coverage.EarliestID < 0 ||
		coverage.LatestID < 0 || coverage.Watermark < 0 {
		return CacheCoverage{}, errors.New("cache coverage cannot contain negative values")
	}
	if coverage.EarliestAtMS > 0 && coverage.LatestAtMS > 0 && coverage.EarliestAtMS > coverage.LatestAtMS {
		return CacheCoverage{}, errors.New("cache coverage time range is inverted")
	}
	if coverage.EarliestID > 0 && coverage.LatestID > 0 && coverage.EarliestID > coverage.LatestID {
		return CacheCoverage{}, errors.New("cache coverage id range is inverted")
	}
	nowMS := max(r.now().UnixMilli(), expectedUpdatedAtMS+1)
	result, err := r.db.ExecContext(ctx, `UPDATE database_cache_coverage SET earliest_at_ms = ?,
		latest_at_ms = ?, earliest_id = ?, latest_id = ?, watermark = ?, complete = ?,
		updated_at_ms = ? WHERE id = 1 AND updated_at_ms = ?`, coverage.EarliestAtMS,
		coverage.LatestAtMS, coverage.EarliestID, coverage.LatestID, coverage.Watermark,
		boolInt(coverage.Complete), nowMS, expectedUpdatedAtMS)
	if err != nil {
		return CacheCoverage{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return CacheCoverage{}, ErrGenerationConflict
	}
	coverage.UpdatedAtMS = nowMS
	return coverage, nil
}

func (r *SQLRepository) CreateMaintenanceTask(ctx context.Context, request CreateMaintenanceTaskRequest) (MaintenanceTask, bool, error) {
	if request.IdempotencyKey == "" || request.Kind != TaskCleanup && request.Kind != TaskRebuild {
		return MaintenanceTask{}, false, errors.New("maintenance task requires idempotency key and valid kind")
	}
	if request.RetentionDays <= 0 {
		request.RetentionDays = DefaultRetentionDays
	}
	if request.ID == "" {
		request.ID = newID(string(request.Kind))
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return MaintenanceTask{}, false, err
	}
	defer tx.Rollback()
	existing, err := scanMaintenanceTask(tx.QueryRowContext(ctx, maintenanceSelect+` WHERE idempotency_key = ?`, request.IdempotencyKey))
	if err == nil {
		if existing.Kind != request.Kind || existing.RetentionDays != request.RetentionDays ||
			existing.ValidationToken != request.ValidationToken {
			return MaintenanceTask{}, false, ErrIdempotencyConflict
		}
		return existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MaintenanceTask{}, false, err
	}
	preview, err := json.Marshal(request.Preview)
	if err != nil {
		return MaintenanceTask{}, false, err
	}
	nowMS := r.now().UnixMilli()
	task := MaintenanceTask{ID: request.ID, IdempotencyKey: request.IdempotencyKey, Generation: 1,
		Kind: request.Kind, Status: StatusRunning, RetentionDays: request.RetentionDays,
		ValidationToken: request.ValidationToken, Preview: request.Preview, CreatedAtMS: nowMS, UpdatedAtMS: nowMS}
	_, err = tx.ExecContext(ctx, `INSERT INTO database_maintenance_tasks (id, idempotency_key,
		generation, kind, status, retention_days, validation_token, preview_json, current_table,
		processed_rows, created_at_ms, updated_at_ms, finished_at_ms, last_error)
		VALUES (?, ?, 1, ?, ?, ?, ?, ?, '', 0, ?, ?, 0, '')`, task.ID, task.IdempotencyKey,
		task.Kind, task.Status, task.RetentionDays, task.ValidationToken, string(preview), nowMS, nowMS)
	if err != nil {
		return MaintenanceTask{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return MaintenanceTask{}, false, err
	}
	return task, true, nil
}

const maintenanceSelect = `SELECT id, idempotency_key, generation, kind, status,
	retention_days, validation_token, preview_json, current_table, processed_rows,
	created_at_ms, updated_at_ms, finished_at_ms, last_error FROM database_maintenance_tasks`

func (r *SQLRepository) MaintenanceTask(ctx context.Context, id string) (MaintenanceTask, error) {
	task, err := scanMaintenanceTask(r.db.QueryRowContext(ctx, maintenanceSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MaintenanceTask{}, ErrNotFound
	}
	return task, err
}

// LatestMaintenanceTask returns the newest durable task for one maintenance
// kind. It is used by runtime status and restart recovery; callers must not
// infer that a terminal task is still active.
func (r *SQLRepository) LatestMaintenanceTask(
	ctx context.Context,
	kind MaintenanceTaskKind,
) (MaintenanceTask, bool, error) {
	if kind != TaskCleanup && kind != TaskRebuild {
		return MaintenanceTask{}, false, errors.New("latest maintenance task requires a valid kind")
	}
	task, err := scanMaintenanceTask(r.db.QueryRowContext(ctx, maintenanceSelect+
		` WHERE kind = ? ORDER BY created_at_ms DESC, updated_at_ms DESC, id DESC LIMIT 1`, kind))
	if errors.Is(err, sql.ErrNoRows) {
		return MaintenanceTask{}, false, nil
	}
	return task, err == nil, err
}

// ActiveMaintenanceTask returns the newest running or paused task. Failed and
// terminal tasks remain observable through LatestMaintenanceTask but do not
// prevent a new idempotent task from being created.
func (r *SQLRepository) ActiveMaintenanceTask(
	ctx context.Context,
	kind MaintenanceTaskKind,
) (MaintenanceTask, bool, error) {
	if kind != TaskCleanup && kind != TaskRebuild {
		return MaintenanceTask{}, false, errors.New("active maintenance task requires a valid kind")
	}
	task, err := scanMaintenanceTask(r.db.QueryRowContext(ctx, maintenanceSelect+
		` WHERE kind = ? AND status IN (?, ?)
		ORDER BY created_at_ms DESC, updated_at_ms DESC, id DESC LIMIT 1`,
		kind, StatusRunning, StatusPaused))
	if errors.Is(err, sql.ErrNoRows) {
		return MaintenanceTask{}, false, nil
	}
	return task, err == nil, err
}

func (r *SQLRepository) PauseMaintenanceTask(ctx context.Context, id string, expectedGeneration int64) (MaintenanceTask, error) {
	return r.updateMaintenanceTask(ctx, id, expectedGeneration, func(task *MaintenanceTask) error {
		if task.Status != StatusRunning {
			return fmt.Errorf("%w: only a running task can be paused", ErrInvalidTransition)
		}
		task.Status = StatusPaused
		return nil
	})
}

func (r *SQLRepository) ResumeMaintenanceTask(ctx context.Context, id string, expectedGeneration int64) (MaintenanceTask, error) {
	return r.updateMaintenanceTask(ctx, id, expectedGeneration, func(task *MaintenanceTask) error {
		if task.Status != StatusPaused && task.Status != StatusFailed {
			return fmt.Errorf("%w: only a paused or failed task can resume", ErrInvalidTransition)
		}
		task.Status = StatusRunning
		task.LastError = ""
		return nil
	})
}

func (r *SQLRepository) CancelMaintenanceTask(ctx context.Context, id string, expectedGeneration int64) (MaintenanceTask, error) {
	return r.updateMaintenanceTask(ctx, id, expectedGeneration, func(task *MaintenanceTask) error {
		if task.Status != StatusRunning && task.Status != StatusPaused && task.Status != StatusFailed {
			return fmt.Errorf("%w: task is already terminal", ErrInvalidTransition)
		}
		task.Status = StatusCanceled
		task.FinishedAtMS = r.now().UnixMilli()
		return nil
	})
}

func (r *SQLRepository) SaveMaintenanceProgress(ctx context.Context, id string, expectedGeneration int64, currentTable string, processedRows int64, done bool, taskError error) (MaintenanceTask, error) {
	return r.updateMaintenanceTask(ctx, id, expectedGeneration, func(task *MaintenanceTask) error {
		if task.Status != StatusRunning {
			return fmt.Errorf("%w: task is not running", ErrInvalidTransition)
		}
		if processedRows < task.ProcessedRows {
			return errors.New("maintenance task progress cannot regress")
		}
		task.CurrentTable = currentTable
		task.ProcessedRows = processedRows
		if taskError != nil {
			task.Status = StatusFailed
			task.LastError = taskError.Error()
		} else if done {
			task.Status = StatusSucceeded
			task.FinishedAtMS = r.now().UnixMilli()
		}
		return nil
	})
}

func (r *SQLRepository) updateMaintenanceTask(ctx context.Context, id string, expectedGeneration int64, mutate func(*MaintenanceTask) error) (MaintenanceTask, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return MaintenanceTask{}, err
	}
	defer tx.Rollback()
	task, err := scanMaintenanceTask(tx.QueryRowContext(ctx, maintenanceSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MaintenanceTask{}, ErrNotFound
	}
	if err != nil {
		return MaintenanceTask{}, err
	}
	if task.Generation != expectedGeneration {
		return MaintenanceTask{}, &ConflictError{Kind: ErrGenerationConflict, Expected: expectedGeneration, Actual: task.Generation}
	}
	if err := mutate(&task); err != nil {
		return MaintenanceTask{}, err
	}
	task.Generation++
	task.UpdatedAtMS = r.now().UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE database_maintenance_tasks SET generation = ?,
		status = ?, current_table = ?, processed_rows = ?, updated_at_ms = ?, finished_at_ms = ?,
		last_error = ? WHERE id = ? AND generation = ?`, task.Generation, task.Status,
		task.CurrentTable, task.ProcessedRows, task.UpdatedAtMS, task.FinishedAtMS, task.LastError,
		task.ID, expectedGeneration)
	if err != nil {
		return MaintenanceTask{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return MaintenanceTask{}, ErrGenerationConflict
	}
	if err := tx.Commit(); err != nil {
		return MaintenanceTask{}, err
	}
	return task, nil
}

func scanMaintenanceTask(row interface{ Scan(...any) error }) (MaintenanceTask, error) {
	var task MaintenanceTask
	var preview sql.NullString
	err := row.Scan(&task.ID, &task.IdempotencyKey, &task.Generation, &task.Kind, &task.Status,
		&task.RetentionDays, &task.ValidationToken, &preview, &task.CurrentTable,
		&task.ProcessedRows, &task.CreatedAtMS, &task.UpdatedAtMS, &task.FinishedAtMS, &task.LastError)
	if err != nil {
		return MaintenanceTask{}, err
	}
	if preview.Valid && preview.String != "" {
		if err := json.Unmarshal([]byte(preview.String), &task.Preview); err != nil {
			return MaintenanceTask{}, err
		}
	}
	return task, nil
}

type CleanupConstraints struct {
	CutoffMS                   int64
	SynchronizedWatermark      int64
	ProtectActiveAndIncomplete bool
	Limit                      int
}

type CleanupBatchResult struct {
	Table    string
	Deleted  int64
	Done     bool
	Coverage CacheCoverage
}

type CacheCleanupData interface {
	Preview(ctx context.Context, cutoffMS int64) ([]TableCleanupPreview, error)
	// DeleteBatch must delete and update cache coverage in one SQLite
	// transaction. It must obey ProtectActiveAndIncomplete and may only delete
	// rows at or below SynchronizedWatermark.
	DeleteBatch(ctx context.Context, table string, constraints CleanupConstraints) (CleanupBatchResult, error)
}

type CleanupStore interface {
	CachePolicy(ctx context.Context) (CachePolicy, error)
	CreateMaintenanceTask(ctx context.Context, request CreateMaintenanceTaskRequest) (MaintenanceTask, bool, error)
	MaintenanceTask(ctx context.Context, id string) (MaintenanceTask, error)
	PauseMaintenanceTask(ctx context.Context, id string, expectedGeneration int64) (MaintenanceTask, error)
	SaveMaintenanceProgress(ctx context.Context, id string, expectedGeneration int64, currentTable string, processedRows int64, done bool, taskError error) (MaintenanceTask, error)
}

type CacheManager struct {
	Store CleanupStore
	Data  CacheCleanupData
	Now   Clock
}

func (m CacheManager) PreviewCleanup(ctx context.Context, retentionDays int) (CleanupPreview, error) {
	if m.Data == nil {
		return CleanupPreview{}, errors.New("cache cleanup data source is required")
	}
	if retentionDays <= 0 {
		policy, err := m.Store.CachePolicy(ctx)
		if err != nil {
			return CleanupPreview{}, err
		}
		retentionDays = policy.RetentionDays
	}
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	cutoff := now.Add(-time.Duration(retentionDays) * 24 * time.Hour).UnixMilli()
	tables, err := m.Data.Preview(ctx, cutoff)
	if err != nil {
		return CleanupPreview{}, err
	}
	preview := CleanupPreview{CutoffMS: cutoff, Tables: tables, CreatedAtMS: now.UnixMilli()}
	for _, table := range tables {
		if table.Rows < 0 || table.ProtectedRows < 0 {
			return CleanupPreview{}, errors.New("cleanup preview contains a negative row count")
		}
		preview.EstimatedRows += table.Rows
	}
	return preview, nil
}

func (m CacheManager) StartCleanup(ctx context.Context, idempotencyKey string, preview CleanupPreview, retentionDays int, safety CleanupSafety) (MaintenanceTask, bool, error) {
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	if err := EvaluateCleanupSafety(safety, now); err != nil {
		return MaintenanceTask{}, false, err
	}
	if preview.CreatedAtMS == 0 || preview.CutoffMS == 0 {
		return MaintenanceTask{}, false, errors.New("cleanup requires a previously generated preview")
	}
	if retentionDays <= 0 {
		retentionDays = DefaultRetentionDays
	}
	return m.Store.CreateMaintenanceTask(ctx, CreateMaintenanceTaskRequest{IdempotencyKey: idempotencyKey,
		Kind: TaskCleanup, RetentionDays: retentionDays, ValidationToken: safety.ValidationToken, Preview: preview})
}

func (m CacheManager) RunCleanupBatch(ctx context.Context, taskID string, expectedGeneration int64, safety CleanupSafety) (MaintenanceTask, error) {
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	task, err := m.Store.MaintenanceTask(ctx, taskID)
	if err != nil {
		return MaintenanceTask{}, err
	}
	if task.Generation != expectedGeneration {
		return MaintenanceTask{}, &ConflictError{Kind: ErrGenerationConflict, Expected: expectedGeneration, Actual: task.Generation}
	}
	if safetyErr := EvaluateCleanupSafety(safety, now); safetyErr != nil {
		if task.Status == StatusRunning {
			paused, pauseErr := m.Store.PauseMaintenanceTask(ctx, task.ID, expectedGeneration)
			if pauseErr != nil {
				return MaintenanceTask{}, errors.Join(safetyErr, pauseErr)
			}
			return paused, safetyErr
		}
		return task, safetyErr
	}
	if task.Kind != TaskCleanup || task.Status != StatusRunning || task.ValidationToken != safety.ValidationToken {
		return MaintenanceTask{}, fmt.Errorf("%w: cleanup task state or validation token changed", ErrCleanupUnsafe)
	}
	policy, err := m.Store.CachePolicy(ctx)
	if err != nil {
		return MaintenanceTask{}, err
	}
	currentIndex := 0
	if task.CurrentTable != "" {
		for index, table := range task.Preview.Tables {
			if table.Table == task.CurrentTable {
				currentIndex = index
				break
			}
		}
	}
	if currentIndex >= len(task.Preview.Tables) {
		return m.Store.SaveMaintenanceProgress(ctx, task.ID, expectedGeneration, "", task.ProcessedRows, true, nil)
	}
	current := task.Preview.Tables[currentIndex]
	result, err := m.Data.DeleteBatch(ctx, current.Table, CleanupConstraints{CutoffMS: task.Preview.CutoffMS,
		SynchronizedWatermark: safety.SynchronizedWatermark, ProtectActiveAndIncomplete: true, Limit: policy.BatchSize})
	if err != nil {
		return m.Store.SaveMaintenanceProgress(ctx, task.ID, expectedGeneration, current.Table, task.ProcessedRows, false, err)
	}
	nextTable := current.Table
	done := false
	if result.Done {
		currentIndex++
		if currentIndex >= len(task.Preview.Tables) {
			nextTable = ""
			done = true
		} else {
			nextTable = task.Preview.Tables[currentIndex].Table
		}
	}
	return m.Store.SaveMaintenanceProgress(ctx, task.ID, expectedGeneration, nextTable,
		task.ProcessedRows+result.Deleted, done, nil)
}

func (m CacheManager) StartRebuild(ctx context.Context, idempotencyKey string, retentionDays int, safety CleanupSafety) (MaintenanceTask, bool, error) {
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	if err := EvaluateCleanupSafety(safety, now); err != nil {
		return MaintenanceTask{}, false, err
	}
	if retentionDays <= 0 {
		retentionDays = DefaultRetentionDays
	}
	return m.Store.CreateMaintenanceTask(ctx, CreateMaintenanceTaskRequest{IdempotencyKey: idempotencyKey,
		Kind: TaskRebuild, RetentionDays: retentionDays, ValidationToken: safety.ValidationToken})
}

func (m CacheManager) RunRebuild(ctx context.Context, taskID string, expectedGeneration int64, safety CleanupSafety, rebuilder SQLiteCacheRebuilder) (MaintenanceTask, error) {
	if rebuilder == nil {
		return MaintenanceTask{}, errors.New("sqlite cache rebuilder is required")
	}
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	task, err := m.Store.MaintenanceTask(ctx, taskID)
	if err != nil {
		return MaintenanceTask{}, err
	}
	if task.Generation != expectedGeneration {
		return MaintenanceTask{}, &ConflictError{Kind: ErrGenerationConflict, Expected: expectedGeneration, Actual: task.Generation}
	}
	if safetyErr := EvaluateCleanupSafety(safety, now); safetyErr != nil {
		if task.Status == StatusRunning {
			paused, pauseErr := m.Store.PauseMaintenanceTask(ctx, task.ID, expectedGeneration)
			if pauseErr != nil {
				return MaintenanceTask{}, errors.Join(safetyErr, pauseErr)
			}
			return paused, safetyErr
		}
		return task, safetyErr
	}
	if task.Kind != TaskRebuild || task.Status != StatusRunning || task.ValidationToken != safety.ValidationToken {
		return MaintenanceTask{}, fmt.Errorf("%w: rebuild task state or validation token changed", ErrCleanupUnsafe)
	}
	temporaryPath, coverage, rebuildErr := rebuilder.RebuildTemporary(ctx, task.RetentionDays, safety.SynchronizedWatermark)
	if rebuildErr == nil {
		rebuildErr = rebuilder.ValidateTemporary(ctx, temporaryPath, coverage)
	}
	if rebuildErr != nil {
		if errors.Is(rebuildErr, ErrCleanupUnsafe) {
			paused, pauseErr := m.Store.PauseMaintenanceTask(ctx, task.ID, expectedGeneration)
			return paused, errors.Join(rebuildErr, pauseErr)
		}
		failed, persistErr := m.Store.SaveMaintenanceProgress(ctx, task.ID, expectedGeneration, "",
			task.ProcessedRows, false, rebuildErr)
		return failed, errors.Join(rebuildErr, persistErr)
	}
	// The live SQLite handle is intentionally invalid after a successful
	// replacement. The rebuilder therefore finalizes the durable task in the
	// temporary database before the atomic swap and returns that exact row.
	// On failure the callback contract guarantees that the old database remains
	// live, so recording the failure through m.Store is still safe.
	replaced, rebuildErr := rebuilder.AtomicReplaceUnderWriteFence(ctx, temporaryPath, task)
	if rebuildErr != nil {
		if errors.Is(rebuildErr, ErrCleanupUnsafe) {
			paused, pauseErr := m.Store.PauseMaintenanceTask(ctx, task.ID, expectedGeneration)
			return paused, errors.Join(rebuildErr, pauseErr)
		}
		failed, persistErr := m.Store.SaveMaintenanceProgress(ctx, task.ID, expectedGeneration, "",
			task.ProcessedRows, false, rebuildErr)
		return failed, errors.Join(rebuildErr, persistErr)
	}
	return replaced, nil
}

// SQLiteCacheRebuilder owns the destructive boundary of cache recovery. Its
// implementation must build a separate temporary SQLite file, copy all config
// plus the requested recent window, rebuild derived data, validate it, and
// only then replace the live cache under a write fence.
type SQLiteCacheRebuilder interface {
	RebuildTemporary(ctx context.Context, retentionDays int, synchronizedWatermark int64) (temporaryPath string, coverage CacheCoverage, err error)
	ValidateTemporary(ctx context.Context, temporaryPath string, coverage CacheCoverage) error
	AtomicReplaceUnderWriteFence(ctx context.Context, temporaryPath string, task MaintenanceTask) (MaintenanceTask, error)
}
