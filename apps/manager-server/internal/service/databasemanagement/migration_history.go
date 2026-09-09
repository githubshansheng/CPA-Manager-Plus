package databasemanagement

import (
	"context"
	"errors"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
)

type MigrationHistoryResponse struct {
	ActiveMigrationID string                   `json:"activeMigrationId,omitempty"`
	Migrations        []MigrationHistoryRecord `json:"migrations"`
}

type MigrationHistoryRecord struct {
	ID              string                             `json:"id"`
	Source          databasemigration.Backend          `json:"source"`
	Target          databasemigration.Backend          `json:"target"`
	Phase           databasemigration.MigrationPhase   `json:"phase"`
	Status          databasemigration.RunStatus        `json:"status"`
	Generation      int64                              `json:"generation"`
	BatchSize       int                                `json:"batchSize"`
	ProgressPercent float64                            `json:"progressPercent"`
	CopiedRows      int64                              `json:"copiedRows"`
	TotalRows       int64                              `json:"totalRows"`
	CopiedBytes     int64                              `json:"copiedBytes"`
	Requests        int64                              `json:"requests"`
	CompletedTables int                                `json:"completedTables"`
	TotalTables     int                                `json:"totalTables"`
	CreatedAtMS     int64                              `json:"createdAtMs"`
	UpdatedAtMS     int64                              `json:"updatedAtMs"`
	FinishedAtMS    int64                              `json:"finishedAtMs,omitempty"`
	LastError       string                             `json:"lastError,omitempty"`
	Tables          []MigrationHistoryTable            `json:"tables"`
	Events          []databasemigration.MigrationEvent `json:"events"`
}

type MigrationHistoryTable struct {
	Name        string `json:"name"`
	Stage       string `json:"stage"`
	RowsCopied  int64  `json:"rowsCopied"`
	TotalRows   int64  `json:"totalRows,omitempty"`
	CopiedBytes int64  `json:"copiedBytes"`
	Requests    int64  `json:"requests"`
	Completed   bool   `json:"completed"`
	UpdatedAtMS int64  `json:"updatedAtMs"`
}

func (r *Runtime) MigrationHistory(ctx context.Context, limit int) (MigrationHistoryResponse, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	state, err := r.loadState()
	if err != nil {
		return MigrationHistoryResponse{}, err
	}
	repository, err := r.metadataRepository(ctx, false)
	if err != nil {
		return MigrationHistoryResponse{}, err
	}
	result, err := r.readMigrationHistory(ctx, repository, state.Migration.ID, limit)
	if err == nil {
		return result, nil
	}
	// Existing deployments may not have the append-only event table until the
	// first request after upgrading. Initialize once on that read failure and
	// retry; steady-state five-second UI polling remains read-only.
	repository, ensureErr := r.metadataRepository(ctx, true)
	if ensureErr != nil {
		return MigrationHistoryResponse{}, errors.Join(err, ensureErr)
	}
	return r.readMigrationHistory(ctx, repository, state.Migration.ID, limit)
}

func (r *Runtime) readMigrationHistory(
	ctx context.Context,
	repository *databasemigration.SQLRepository,
	activeMigrationID string,
	limit int,
) (MigrationHistoryResponse, error) {
	migrations, err := repository.ListMigrations(ctx, limit)
	if err != nil {
		return MigrationHistoryResponse{}, err
	}
	derivedTables := newMySQLDerivedExecutor(nil).Tables()
	derivedSet := make(map[string]struct{}, len(derivedTables))
	for _, table := range derivedTables {
		derivedSet[table] = struct{}{}
	}
	result := MigrationHistoryResponse{
		ActiveMigrationID: activeMigrationID,
		Migrations:        make([]MigrationHistoryRecord, 0, len(migrations)),
	}
	for _, migration := range migrations {
		progress, progressErr := repository.ListTableProgress(ctx, migration.ID)
		if progressErr != nil {
			return MigrationHistoryResponse{}, progressErr
		}
		events, eventErr := repository.MigrationEvents(ctx, migration.ID, 500)
		if eventErr != nil {
			return MigrationHistoryResponse{}, eventErr
		}
		record := MigrationHistoryRecord{
			ID: migration.ID, Source: migration.Source, Target: migration.Target,
			Phase: migration.Phase, Status: migration.Status, Generation: migration.Generation,
			BatchSize: migration.BatchSize, CreatedAtMS: migration.CreatedAtMS,
			UpdatedAtMS: migration.UpdatedAtMS, FinishedAtMS: migration.FinishedAtMS,
			LastError: migration.LastError, Events: events,
			Tables: make([]MigrationHistoryTable, 0, len(progress)),
		}
		var historyProgress float64
		derivedCompleted := 0
		for _, table := range progress {
			stage := "history"
			totalRows := int64(0)
			if _, derived := derivedSet[table.Table]; derived {
				stage = "derived"
				if table.Completed {
					derivedCompleted++
				}
			} else {
				if watermark, watermarkErr := decodeHistoryTableWatermark(table.SourceWatermark); watermarkErr == nil {
					totalRows = watermark.Rows
				}
				record.CopiedRows += table.RowsCopied
				record.TotalRows += totalRows
			}
			if table.Completed {
				record.CompletedTables++
			}
			record.CopiedBytes += table.BytesCopied
			record.Requests += table.Requests
			record.Tables = append(record.Tables, MigrationHistoryTable{
				Name: table.Table, Stage: stage, RowsCopied: table.RowsCopied,
				TotalRows: totalRows, CopiedBytes: table.BytesCopied, Requests: table.Requests,
				Completed: table.Completed, UpdatedAtMS: table.UpdatedAtMS,
			})
		}
		record.TotalTables = len(schema.Current().AuthoritativeTables()) + len(derivedTables)
		if record.TotalRows > 0 {
			historyProgress = min(100, float64(record.CopiedRows)*100/float64(record.TotalRows))
		}
		derivedProgress := float64(0)
		if len(derivedTables) > 0 {
			derivedProgress = float64(derivedCompleted) * 100 / float64(len(derivedTables))
		}
		record.ProgressPercent = migrationProgressForPhase(migration.Phase, historyProgress, derivedProgress)
		result.Migrations = append(result.Migrations, record)
	}
	return result, nil
}
