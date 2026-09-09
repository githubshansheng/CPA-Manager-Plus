package databasemanagement

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/outboxcontext"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

const sqliteRebuildBackupSuffix = ".rebuild-backup"

// ReplaceSQLiteCacheFile installs a previously validated rebuild artifact. It
// is called only while the process-wide write fence is held. The old database
// is retained under a deterministic backup name until the new handle has been
// opened, journal-audited and enabled; every failure path restores and reopens
// the old database before returning.
func (r *Runtime) ReplaceSQLiteCacheFile(
	ctx context.Context,
	temporaryPath string,
	expectedGeneration uint64,
	expectedWatermark int64,
) (database.Backend, error) {
	state, err := r.requireGeneration(expectedGeneration)
	if err != nil {
		return nil, err
	}
	if state.WritePrimary != database.BackendSQLite || state.RoutingEpoch == 0 {
		return nil, errors.New("sqlite cache replacement requires SQLite write primary with an active epoch")
	}
	livePath, temporaryPath, err := r.validateSQLiteReplacementPaths(temporaryPath)
	if err != nil {
		return nil, err
	}
	backupPath := livePath + sqliteRebuildBackupSuffix

	r.backendMu.Lock()
	defer r.backendMu.Unlock()
	if r.sqlite == nil || r.sqlite.DB() == nil {
		return nil, errors.New("sqlite backend is not open")
	}
	oldDB := r.sqlite.DB()
	repository := databasemigration.NewSQLRepository(oldDB, databasemigration.DialectSQLite)
	pending, _, _, watermark, err := repository.OutboxBacklog(ctx)
	if err != nil {
		return nil, err
	}
	if pending != 0 || watermark != expectedWatermark {
		return nil, fmt.Errorf("sqlite cache replacement watermark changed: pending=%d watermark=%d expected=%d",
			pending, watermark, expectedWatermark)
	}
	var checkpointBusy, checkpointLog, checkpointed int64
	if err := oldDB.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(
		&checkpointBusy, &checkpointLog, &checkpointed,
	); err != nil {
		return nil, fmt.Errorf("checkpoint sqlite before cache replacement: %w", err)
	}
	if checkpointBusy != 0 || checkpointed < checkpointLog {
		return nil, fmt.Errorf("sqlite checkpoint is busy before cache replacement: busy=%d log=%d checkpointed=%d",
			checkpointBusy, checkpointLog, checkpointed)
	}
	if err := r.sqlite.Close(); err != nil {
		return nil, fmt.Errorf("close sqlite before cache replacement: %w", err)
	}
	r.sqlite = nil

	if _, err := os.Stat(backupPath); err == nil {
		restored, restoreErr := r.openSQLiteReplacement(ctx, livePath, state.RoutingEpoch)
		r.sqlite = restored
		return nil, errors.Join(errors.New("stale sqlite rebuild backup exists; restart recovery is required"), restoreErr)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := moveSQLiteFileSet(livePath, backupPath, true); err != nil {
		restored, restoreErr := r.openSQLiteReplacement(ctx, livePath, state.RoutingEpoch)
		r.sqlite = restored
		return nil, errors.Join(fmt.Errorf("stage sqlite rebuild backup: %w", err), restoreErr)
	}
	installed := false
	rollback := func(cause error) (database.Backend, error) {
		if installed {
			_ = removeSQLiteFileSet(livePath)
		}
		restoreErr := moveSQLiteFileSet(backupPath, livePath, true)
		restored, openErr := r.openSQLiteReplacement(ctx, livePath, state.RoutingEpoch)
		r.sqlite = restored
		return nil, errors.Join(cause, restoreErr, openErr)
	}
	if err := moveSQLiteFileSet(temporaryPath, livePath, true); err != nil {
		return rollback(fmt.Errorf("install rebuilt sqlite cache: %w", err))
	}
	installed = true
	next, err := r.openSQLiteReplacement(ctx, livePath, state.RoutingEpoch)
	if err != nil {
		return rollback(fmt.Errorf("open rebuilt sqlite cache: %w", err))
	}
	r.sqlite = next
	// Failure to remove the old cache does not invalidate the already-opened
	// live database. Startup performs the same cleanup after validating it.
	_ = removeSQLiteFileSet(backupPath)
	return next, nil
}

func (r *Runtime) validateSQLiteReplacementPaths(temporaryPath string) (string, string, error) {
	livePath, err := filepath.Abs(r.sqlitePath)
	if err != nil {
		return "", "", err
	}
	temporaryPath, err = filepath.Abs(strings.TrimSpace(temporaryPath))
	if err != nil {
		return "", "", err
	}
	dataDir := filepath.Clean(r.dataDir)
	if filepath.Dir(livePath) != dataDir || filepath.Dir(temporaryPath) != dataDir ||
		!strings.HasPrefix(filepath.Base(temporaryPath), ".sqlite-cache-rebuild-") ||
		filepath.Clean(temporaryPath) == filepath.Clean(livePath) {
		return "", "", errors.New("sqlite cache replacement paths are outside the configured data directory")
	}
	return livePath, temporaryPath, nil
}

func (r *Runtime) openSQLiteReplacement(ctx context.Context, path string, epoch uint64) (database.Backend, error) {
	db, err := sqliterepo.Open(path)
	if err != nil {
		return nil, err
	}
	if err := outboxcontext.Enable(ctx, db, int64(epoch)); err != nil {
		_ = db.Close()
		return nil, err
	}
	return database.NewSQLBackend(database.BackendSQLite, db), nil
}

func moveSQLiteFileSet(source, target string, requireMain bool) error {
	moved := make([]string, 0, 3)
	for _, suffix := range []string{"-wal", "-shm", ""} {
		sourcePath, targetPath := source+suffix, target+suffix
		if _, err := os.Stat(sourcePath); err != nil {
			if errors.Is(err, os.ErrNotExist) && (suffix != "" || !requireMain) {
				continue
			}
			rollbackMovedSQLiteFiles(source, target, moved)
			return err
		}
		if err := os.Rename(sourcePath, targetPath); err != nil {
			rollbackMovedSQLiteFiles(source, target, moved)
			return err
		}
		moved = append(moved, suffix)
	}
	return nil
}

func rollbackMovedSQLiteFiles(source, target string, suffixes []string) {
	for index := len(suffixes) - 1; index >= 0; index-- {
		_ = os.Rename(target+suffixes[index], source+suffixes[index])
	}
}

func removeSQLiteFileSet(path string) error {
	var result error
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	return result
}

// RecoverInterruptedSQLiteCacheReplace restores the live path after a crash in
// the narrow rename window. It must run while the DataDir process lock is held
// and before SQLite is opened.
func RecoverInterruptedSQLiteCacheReplace(path string) error {
	livePath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	backupPath := livePath + sqliteRebuildBackupSuffix
	_, liveErr := os.Stat(livePath)
	_, backupErr := os.Stat(backupPath)
	if errors.Is(liveErr, os.ErrNotExist) && backupErr == nil {
		return moveSQLiteFileSet(backupPath, livePath, true)
	}
	if liveErr != nil && !errors.Is(liveErr, os.ErrNotExist) {
		return liveErr
	}
	if backupErr != nil && !errors.Is(backupErr, os.ErrNotExist) {
		return backupErr
	}
	return nil
}

// CleanupRecoveredSQLiteCacheBackup removes a backup only after the live
// database has been opened and validated successfully by startup.
func CleanupRecoveredSQLiteCacheBackup(path string) error {
	livePath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	return removeSQLiteFileSet(livePath + sqliteRebuildBackupSuffix)
}
