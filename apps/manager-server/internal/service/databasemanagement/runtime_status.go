package databasemanagement

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
)

func (r *Runtime) ConnectConfiguredMySQL(ctx context.Context) error {
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	state, err := r.loadState()
	if err != nil {
		return err
	}
	if state.MySQL.Host == "" {
		return nil
	}
	if _, err := dbmysql.Test(ctx, state.MySQL); err != nil {
		return err
	}
	db, err := dbmysql.Open(ctx, state.MySQL)
	if err != nil {
		return err
	}
	if r.validateBackend != nil {
		candidate := database.NewSQLBackend(database.BackendMySQL, db)
		if err := r.validateBackend(ctx, candidate); err != nil {
			_ = db.Close()
			return err
		}
	}
	latest, err := r.loadState()
	if err != nil {
		_ = db.Close()
		return err
	}
	if latest.MySQL != state.MySQL {
		_ = db.Close()
		return ErrGenerationConflict
	}
	r.replaceMySQL(database.NewSQLBackend(database.BackendMySQL, db))
	return nil
}

func (r *Runtime) sqliteStatus(parent context.Context, state control.State) SQLiteStatus {
	status := SQLiteStatus{RetentionDays: state.CachePolicy.RetentionDays, RebuildStatus: "idle"}
	if state.CachePolicy.Enabled {
		status.CleanupStatus = "idle"
	} else {
		status.CleanupStatus = "disabled"
	}
	db := r.sqliteDB()
	if db == nil {
		status.LastError = "sqlite is not open"
		return status
	}
	ctx, cancel := context.WithTimeout(parent, statusTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		status.LastError = err.Error()
		return status
	}
	status.Connected = true
	status.Available = true
	status.DatabaseBytes = fileSize(r.sqlitePath)
	status.WALBytes = fileSize(r.sqlitePath + "-wal")
	status.SHMBytes = fileSize(r.sqlitePath + "-shm")
	status.TotalBytes = status.DatabaseBytes + status.WALBytes + status.SHMBytes
	var pageSize int64
	if err := db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		status.LastError = err.Error()
		return status
	}
	if err := db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&status.PageCount); err != nil {
		status.LastError = err.Error()
		return status
	}
	if err := db.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&status.FreePageCount); err != nil {
		status.LastError = err.Error()
		return status
	}
	status.ReusableBytes = status.FreePageCount * pageSize
	status.EffectiveBytes = (status.PageCount - status.FreePageCount) * pageSize
	return status
}

func (r *Runtime) mysqlStatus(parent context.Context, state control.State) MySQLStatus {
	status := MySQLStatus{
		Configured: state.MySQL.Host != "", Database: state.MySQL.Database,
		MaskedAddress: maskEndpoint(state.MySQL.Host, state.MySQL.Port),
	}
	if !status.Configured {
		return status
	}
	db, release := r.mysqlDB()
	defer release()
	if db == nil {
		status.LastError = "mysql is not connected"
		return status
	}
	ctx, cancel := context.WithTimeout(parent, statusTimeout)
	defer cancel()
	sample := r.sampler.Sample(ctx, db)
	status.Connected = sample.Connected
	status.Available = sample.Connected
	status.Version = sample.Version
	status.PingLatencyMS = sample.PingLatencyMS
	status.Pool = MySQLPoolStatus{Open: sample.Pool.Open, InUse: sample.Pool.InUse,
		Idle: sample.Pool.Idle, WaitCount: sample.Pool.WaitCount,
		WaitDurationMS: sample.Pool.WaitDuration, MaxOpen: db.Stats().MaxOpenConnections}
	if sample.DatabaseBytes.Available {
		status.DatabaseBytes = int64(sample.DatabaseBytes.Value)
	}
	if sample.TableBytes.Available {
		status.TableBytes = int64(sample.TableBytes.Value)
	}
	if sample.IndexBytes.Available {
		status.IndexBytes = int64(sample.IndexBytes.Value)
	}
	if sample.UptimeSeconds.Available {
		status.UptimeSeconds = int64(sample.UptimeSeconds.Value)
	}
	status.Connections = metricValue(sample.Connections)
	status.MaxConnections = metricValue(sample.MaxConnections)
	status.QueriesPerSecond = metricValue(sample.QPS)
	status.TransactionsPerSecond = metricValue(sample.TPS)
	status.SlowQueries = metricValue(sample.SlowQueries)
	status.BufferPoolBytes = metricValue(sample.BufferPoolBytes)
	status.LockWaits = metricValue(sample.LockWaits)
	status.Deadlocks = metricValue(sample.Deadlocks)
	status.DataLockWaits = metricValue(sample.Advanced["dataLockWaits"])
	status.MetadataLockWaits = metricValue(sample.Advanced["metadataLockWaits"])
	status.LastError = sample.Error
	return status
}

func (r *Runtime) enrichMigrationStatus(
	ctx context.Context,
	repository *databasemigration.SQLRepository,
	state control.State,
	status *Status,
) {
	direction, _, _ := replicationRoute(state.WritePrimary)
	if replication, err := repository.Replication(ctx, direction); err == nil {
		health := databasemigration.ReplicationHealth(replication, time.Now())
		healthState := "idle"
		switch {
		case health.Stalled:
			healthState = "stalled"
		case health.LastError != "":
			healthState = "error"
		case health.Warning:
			healthState = "warning"
		case health.BacklogRows > 0:
			healthState = "running"
		}
		status.Replication = database.ReplicationStatus{
			Enabled: state.ReplicationEnabled, State: healthState, Direction: health.Direction,
			Epoch: uint64(max(health.Epoch, 0)), Source: database.BackendKind(health.Source),
			Target: database.BackendKind(health.Target), SourceWatermark: health.SourceWatermark,
			TargetWatermark: health.TargetWatermark, PendingMutations: health.BacklogRows,
			PendingBytes: health.BacklogBytes, OldestPendingAtMS: health.OldestBacklogAtMS,
			MutationsPerSecond: health.ThroughputRowsPerSec, RetryCount: health.Retries,
			LastProgressAtMS: health.LastProgressAtMS, LastSuccessAtMS: health.LastSuccessAtMS,
			HeartbeatAtMS: health.HeartbeatAtMS, Stalled: health.Stalled, Error: health.LastError,
		}
	}
	if policy, err := repository.CachePolicy(ctx); err == nil {
		status.CacheCoverage.RetentionDays = policy.RetentionDays
		status.CacheCoverage.CleanupEnabled = policy.Enabled
	}
	if coverage, err := repository.CacheCoverage(ctx); err == nil {
		status.CacheCoverage.Complete = coverage.Complete
		status.CacheCoverage.FromMS = coverage.EarliestAtMS
		status.CacheCoverage.ToMS = coverage.LatestAtMS
		status.CacheCoverage.MinimumEventID = coverage.EarliestID
		status.CacheCoverage.MaximumEventID = coverage.LatestID
		status.CacheCoverage.SyncedWatermark = coverage.Watermark
	}
	r.enrichCacheMaintenanceStatus(ctx, repository, status)
	if state.Migration.ID == "" {
		return
	}
	migration, err := repository.Migration(ctx, state.Migration.ID)
	if err != nil {
		status.DatabaseMigration = database.MigrationStatus{ID: state.Migration.ID,
			Phase: state.Migration.Phase, Status: state.Migration.Status, Error: err.Error()}
		return
	}
	result := database.MigrationStatus{ID: migration.ID, Phase: string(migration.Phase),
		Status: string(migration.Status), State: string(migration.Status), Source: database.BackendKind(migration.Source),
		Target: database.BackendKind(migration.Target), StartedAtMS: migration.CreatedAtMS,
		UpdatedAtMS: migration.UpdatedAtMS, FinishedAtMS: migration.FinishedAtMS,
		ValidationToken: migration.ValidationToken, PriceManifestSHA256: migration.FrozenPriceHash,
		Error: migration.LastError}
	validationByTable := map[string]databasemigration.TableValidation{}
	if migration.Validation != nil {
		status.CacheCoverage.LastValidatedAtMS = migration.Validation.ValidatedAtMS
		result.ValidationValid = migrationValidationIsCurrent(
			migration, status.Replication, state.ReplicationEnabled,
		)
		result.InputTokens = migration.Validation.SourceAggregate.InputTokens
		result.OutputTokens = migration.Validation.SourceAggregate.OutputTokens
		result.CachedTokens = migration.Validation.SourceAggregate.CachedTokens
		result.ReasoningTokens = migration.Validation.SourceAggregate.ReasoningTokens
		result.Cost = migration.Validation.SourceAggregate.Cost
		if !migration.Validation.Passed {
			result.ValidationError = strings.Join(migration.Validation.Errors, "; ")
		}
		for _, table := range migration.Validation.Tables {
			validationByTable[table.Table] = table
		}
	}
	if progress, active := r.validationProgressSnapshot(migration.ID); active {
		result.ValidationProgress = validationProgressStatus(progress)
	}
	for _, table := range schema.Current().AuthoritativeTables() {
		progress, exists, progressErr := repository.TableProgress(ctx, migration.ID, table.Name)
		if progressErr != nil {
			result.Tables = append(result.Tables, database.MigrationTableStatus{
				Name: table.Name, Stage: "history", Error: progressErr.Error(),
			})
			continue
		}
		if !exists {
			result.Tables = append(result.Tables, database.MigrationTableStatus{Name: table.Name, Stage: "history"})
			if result.CurrentTable == "" && migration.Phase == databasemigration.PhaseCopyHistory {
				result.CurrentTable = table.Name
			}
			continue
		}
		entry := database.MigrationTableStatus{Name: table.Name, CopiedRows: progress.RowsCopied,
			CopiedBytes: progress.BytesCopied, Requests: progress.Requests, Stage: "history",
			Completed: progress.Completed, UpdatedAtMS: progress.UpdatedAtMS}
		if watermark, watermarkErr := decodeHistoryTableWatermark(progress.SourceWatermark); watermarkErr == nil {
			entry.TotalRows = watermark.Rows
		}
		if len(progress.Checkpoint) > 0 {
			entry.LastPrimaryKey = string(progress.Checkpoint)
		}
		if progress.Completed {
			entry.Progress = 100
		} else if entry.TotalRows > 0 {
			entry.Progress = min(100, float64(entry.CopiedRows)*100/float64(entry.TotalRows))
		}
		if !progress.Completed && result.CurrentTable == "" && migration.Phase == databasemigration.PhaseCopyHistory {
			result.CurrentTable = table.Name
		}
		if validation, ok := validationByTable[table.Name]; ok {
			entry.Checksum = validation.TargetSHA256
			if !validation.Passed {
				entry.Error = validation.Error
				if entry.Error == "" {
					entry.Error = "validation mismatch"
				}
			}
		}
		result.CopiedRows += entry.CopiedRows
		result.TotalRows += entry.TotalRows
		result.CopiedBytes += entry.CopiedBytes
		result.Requests += entry.Requests
		result.Tables = append(result.Tables, entry)
	}
	historyProgress := float64(0)
	if result.TotalRows > 0 {
		historyProgress = min(100, float64(result.CopiedRows)*100/float64(result.TotalRows))
	}
	derivedProgress := r.enrichDerivedMigrationProgress(ctx, repository, migration, &result)
	result.ProgressPercent = migrationProgressForPhase(migration.Phase, historyProgress, derivedProgress)
	if activity, active := r.migrationWorkSnapshot(migration.ID); active {
		result.CurrentTable = activity.Table
		result.CurrentTableActive = true
		result.CurrentTableSinceMS = activity.StartedAtMS
		markActiveMigrationTable(result.Tables, activity.Table)
		markActiveMigrationTable(result.DerivedTables, activity.Table)
	}
	elapsedSeconds := time.Since(time.UnixMilli(migration.CreatedAtMS)).Seconds()
	if elapsedSeconds > 0 && result.CopiedRows > 0 {
		result.RowsPerSecond = float64(result.CopiedRows) / elapsedSeconds
		remaining := max(result.TotalRows-result.CopiedRows, 0)
		if remaining > 0 {
			result.ETASeconds = int64(float64(remaining) / result.RowsPerSecond)
		}
	}
	status.DatabaseMigration = result
}

func migrationValidationIsCurrent(
	migration databasemigration.Migration,
	replication database.ReplicationStatus,
	replicationEnabled bool,
) bool {
	if migration.Validation == nil || !migration.Validation.Passed || migration.ValidationToken == "" {
		return false
	}
	if !replicationEnabled {
		return true
	}
	return migration.FinalOutboxWatermark == replication.SourceWatermark &&
		migration.FinalOutboxWatermark == replication.TargetWatermark
}

func migrationProgressForPhase(
	phase databasemigration.MigrationPhase,
	historyProgress float64,
	derivedProgress float64,
) float64 {
	switch phase {
	case databasemigration.PhaseCopyHistory:
		return historyProgress
	case databasemigration.PhaseRebuildDerived:
		return derivedProgress
	case databasemigration.PhaseValidate,
		databasemigration.PhaseReadyToCutover,
		databasemigration.PhaseCompleted:
		// Reaching validation means both history copying and every derived-table
		// rebuild checkpoint completed. Validation itself is an explicit admin
		// action, so reporting zero here incorrectly looks like stalled work.
		return 100
	default:
		return 0
	}
}

func (r *Runtime) enrichDerivedMigrationProgress(
	ctx context.Context,
	repository *databasemigration.SQLRepository,
	migration databasemigration.Migration,
	result *database.MigrationStatus,
) float64 {
	if result == nil {
		return 0
	}
	tables := newMySQLDerivedExecutor(nil).Tables()
	result.TotalSteps = len(tables)
	for _, table := range tables {
		entry := database.MigrationTableStatus{Name: table, Stage: "derived"}
		progress, exists, err := repository.TableProgress(ctx, migration.ID, table)
		if err != nil {
			entry.Error = err.Error()
		} else if exists {
			entry.CopiedRows = progress.RowsCopied
			entry.CopiedBytes = progress.BytesCopied
			entry.Requests = progress.Requests
			entry.Completed = progress.Completed
			entry.UpdatedAtMS = progress.UpdatedAtMS
			if progress.Completed {
				entry.Progress = 100
				result.CompletedSteps++
			}
		}
		if !entry.Completed && result.CurrentTable == "" && migration.Phase == databasemigration.PhaseRebuildDerived {
			result.CurrentTable = table
		}
		result.DerivedTables = append(result.DerivedTables, entry)
	}
	if result.TotalSteps == 0 {
		return 0
	}
	return float64(result.CompletedSteps) * 100 / float64(result.TotalSteps)
}

func markActiveMigrationTable(tables []database.MigrationTableStatus, name string) {
	for index := range tables {
		if tables[index].Name == name {
			tables[index].Active = true
			return
		}
	}
}

func fileSize(path string) int64 {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func metricValue(metric dbmysql.Metric) MetricValue {
	message := strings.ToLower(metric.Error)
	return MetricValue{Value: metric.Value, Available: metric.Available,
		PermissionDenied: strings.Contains(message, "denied") || strings.Contains(message, "permission"),
		Error:            metric.Error}
}

func maskEndpoint(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if port == 0 {
		port = 3306
	}
	masked := host
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			masked = strconv.Itoa(int(v4[0])) + "." + strconv.Itoa(int(v4[1])) + "." + strconv.Itoa(int(v4[2])) + ".***"
		} else {
			masked = "[" + host[:min(len(host), 4)] + ":…]"
		}
	} else if !strings.EqualFold(host, "localhost") {
		labels := strings.Split(host, ".")
		if len(labels) > 1 {
			labels[0] = "***"
			masked = strings.Join(labels, ".")
		} else if len(host) > 2 {
			masked = host[:1] + strings.Repeat("*", min(len(host)-1, 6))
		}
	}
	return net.JoinHostPort(masked, strconv.Itoa(port))
}
