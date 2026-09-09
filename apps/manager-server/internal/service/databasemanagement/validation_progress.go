package databasemanagement

import (
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
)

// validationProgressState is intentionally process-local. A validation run
// holds the global write fence and cannot safely resume after a process
// restart; the durable migration row remains the source of truth and the
// administrator can retry validation. Keeping this heartbeat in memory lets
// status requests observe the long-running scan without adding another schema
// table or pretending an incomplete scan is a checkpoint.
type validationProgressState struct {
	ID             string
	Stage          string
	CurrentTable   string
	Side           string
	CompletedSteps int
	TotalSteps     int
	ProcessedRows  int64
	TotalRows      int64
	StartedAtMS    int64
	UpdatedAtMS    int64
	TableStartedMS int64
}

type validationProgressSnapshot = validationProgressState

func (r *Runtime) beginValidationProgress(migrationID string, totalSteps int) func() {
	now := time.Now().UnixMilli()
	r.validationMu.Lock()
	r.validationRun = validationProgressState{
		ID: migrationID, Stage: databasemigration.ValidationStagePreparing,
		TotalSteps: totalSteps, StartedAtMS: now, UpdatedAtMS: now,
		TableStartedMS: now,
	}
	r.validationMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.validationMu.Lock()
			if r.validationRun.ID == migrationID {
				r.validationRun = validationProgressState{}
			}
			r.validationMu.Unlock()
		})
	}
}

func (r *Runtime) updateValidationProgress(
	migrationID string,
	progress databasemigration.ValidationProgress,
) {
	r.validationMu.Lock()
	defer r.validationMu.Unlock()
	if r.validationRun.ID == "" || r.validationRun.ID != migrationID {
		return
	}
	now := time.Now().UnixMilli()
	if progress.Table != r.validationRun.CurrentTable || string(progress.Side) != r.validationRun.Side {
		r.validationRun.TableStartedMS = now
	}
	// Validator-level reports carry TotalSteps and define a new logical step;
	// row-level reports (from the SQL reader) intentionally omit it so they do
	// not reset the overall step counters while a large table is scanning.
	if progress.TotalSteps > 0 {
		r.validationRun.Stage = progress.Stage
		r.validationRun.CurrentTable = progress.Table
		r.validationRun.Side = string(progress.Side)
		r.validationRun.CompletedSteps = max(progress.CompletedSteps, 0)
		r.validationRun.TotalSteps = max(progress.TotalSteps, 0)
	} else {
		if progress.Stage != "" {
			r.validationRun.Stage = progress.Stage
		}
		if progress.Table != "" {
			r.validationRun.CurrentTable = progress.Table
		}
		if progress.Side != "" {
			r.validationRun.Side = string(progress.Side)
		}
	}
	r.validationRun.ProcessedRows = max(progress.ProcessedRows, 0)
	r.validationRun.TotalRows = max(progress.TotalRows, 0)
	r.validationRun.UpdatedAtMS = now
}

func (r *Runtime) validationProgressSnapshot(migrationID string) (validationProgressSnapshot, bool) {
	r.validationMu.RLock()
	defer r.validationMu.RUnlock()
	if r.validationRun.ID == "" || r.validationRun.ID != migrationID {
		return validationProgressSnapshot{}, false
	}
	return r.validationRun, true
}

func validationProgressStatus(snapshot validationProgressSnapshot) *database.MigrationValidationProgress {
	if snapshot.ID == "" {
		return nil
	}
	percent := float64(snapshot.CompletedSteps) * 100
	if snapshot.TotalSteps > 0 {
		percent /= float64(snapshot.TotalSteps)
	}
	if snapshot.TotalRows > 0 && snapshot.ProcessedRows < snapshot.TotalRows && snapshot.TotalSteps > 0 {
		rowFraction := float64(snapshot.ProcessedRows) / float64(snapshot.TotalRows)
		if rowFraction < 0 {
			rowFraction = 0
		}
		if rowFraction > 1 {
			rowFraction = 1
		}
		// The current table/aggregate scan occupies the next logical step.
		percent = (float64(snapshot.CompletedSteps) + rowFraction) * 100 / float64(snapshot.TotalSteps)
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return &database.MigrationValidationProgress{
		Running: true, Stage: snapshot.Stage, CurrentTable: snapshot.CurrentTable,
		CurrentTableSinceMS: snapshot.TableStartedMS,
		Side:                snapshot.Side, CompletedSteps: snapshot.CompletedSteps,
		TotalSteps: snapshot.TotalSteps, ProcessedRows: snapshot.ProcessedRows,
		TotalRows: snapshot.TotalRows, ProgressPercent: percent,
		StartedAtMS: snapshot.StartedAtMS, UpdatedAtMS: snapshot.UpdatedAtMS,
	}
}
