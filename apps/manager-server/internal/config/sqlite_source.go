package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	SQLiteSourceSelectionVersion = 1
	SQLiteSourceStatePending     = "pending"
	SQLiteSourceStatePrepared    = "control_prepared"
	SQLiteSourceStateActive      = "active"
	SQLiteSourceOperationAdopt   = "adopt"
	SQLiteSourceOperationSwitch  = "switch"

	sqliteSourceSelectionName = ".cpa-manager-plus.sqlite-source.json"
	preAdoptionBackupSuffix   = ".before-sqlite-adoption"
)

// SQLiteSourceSelection is the durable hand-off between the first-run panel
// and the next Manager Server process. The running process never swaps its
// open connection pool; the selection becomes effective only after restart.
type SQLiteSourceSelection struct {
	Version              int    `json:"version"`
	State                string `json:"state"`
	Operation            string `json:"operation,omitempty"`
	DatabasePath         string `json:"databasePath"`
	DataKeyPath          string `json:"dataKeyPath,omitempty"`
	PreviousDatabasePath string `json:"previousDatabasePath,omitempty"`
	PreviousDataKeyPath  string `json:"previousDataKeyPath,omitempty"`
	ControlBackupSuffix  string `json:"controlBackupSuffix,omitempty"`
	IdempotencyKey       string `json:"idempotencyKey,omitempty"`
	SelectedAtMS         int64  `json:"selectedAtMs"`
	ActivatedAtMS        int64  `json:"activatedAtMs,omitempty"`
	FailedDatabasePath   string `json:"failedDatabasePath,omitempty"`
	FailedDataKeyPath    string `json:"failedDataKeyPath,omitempty"`
	LastErrorStage       string `json:"lastErrorStage,omitempty"`
	LastErrorCause       string `json:"lastErrorCause,omitempty"`
	LastError            string `json:"lastError,omitempty"`
	FailedAtMS           int64  `json:"failedAtMs,omitempty"`
}

type SQLiteSourceSwitchFailure struct {
	Stage string
	Cause string
}

func SQLiteSourceSelectionPath(dataDir string) string {
	return filepath.Join(dataDir, sqliteSourceSelectionName)
}

func LoadSQLiteSourceSelection(dataDir string) (SQLiteSourceSelection, bool, error) {
	path := SQLiteSourceSelectionPath(dataDir)
	selection, err := readSQLiteSourceSelection(path)
	if err == nil {
		return selection, true, nil
	}
	mainErr := err
	selection, backupErr := readSQLiteSourceSelection(path + ".bak")
	if backupErr == nil {
		return selection, true, nil
	}
	if errors.Is(mainErr, os.ErrNotExist) && errors.Is(backupErr, os.ErrNotExist) {
		return SQLiteSourceSelection{}, false, nil
	}
	return SQLiteSourceSelection{}, false, fmt.Errorf(
		"load SQLite source selection (main: %v; backup: %v)", mainErr, backupErr,
	)
}

func SaveSQLiteSourceSelection(dataDir string, selection SQLiteSourceSelection) error {
	if err := normalizeSQLiteSourceSelection(&selection); err != nil {
		return err
	}
	if selection.SelectedAtMS == 0 {
		selection.SelectedAtMS = time.Now().UnixMilli()
	}
	data, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return fmt.Errorf("encode SQLite source selection: %w", err)
	}
	data = append(data, '\n')
	return writeSQLiteSourceSelectionAtomic(SQLiteSourceSelectionPath(dataDir), data)
}

// PrepareSQLiteSourceControl archives the control-plane files from the empty
// first-run database generation before a selected historical database and its
// data key become authoritative. It is idempotent across crashes: a source
// already moved to the fixed backup name is treated as prepared.
func PrepareSQLiteSourceControl(dataDir string) (SQLiteSourceSelection, error) {
	selection, ok, err := LoadSQLiteSourceSelection(dataDir)
	if err != nil {
		return SQLiteSourceSelection{}, err
	}
	if !ok {
		return SQLiteSourceSelection{}, errors.New("SQLite source selection is missing")
	}
	if selection.State != SQLiteSourceStatePending {
		return selection, nil
	}

	controlPath := filepath.Join(dataDir, "database-control.json.enc")
	backupSuffix := selection.ControlBackupSuffix
	if backupSuffix == "" {
		backupSuffix = preAdoptionBackupSuffix
	}
	for _, path := range []string{controlPath, controlPath + ".bak"} {
		backupPath := path + backupSuffix
		_, sourceErr := os.Stat(path)
		_, backupErr := os.Stat(backupPath)
		sourceExists := sourceErr == nil
		backupExists := backupErr == nil
		if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
			return SQLiteSourceSelection{}, fmt.Errorf("inspect pre-adoption control file %s: %w", path, sourceErr)
		}
		if backupErr != nil && !errors.Is(backupErr, os.ErrNotExist) {
			return SQLiteSourceSelection{}, fmt.Errorf("inspect pre-adoption control backup %s: %w", backupPath, backupErr)
		}
		if sourceExists && backupExists {
			return SQLiteSourceSelection{}, fmt.Errorf(
				"both the current and pre-adoption control files exist: %s and %s", path, backupPath,
			)
		}
		if sourceExists {
			if err := os.Rename(path, backupPath); err != nil {
				return SQLiteSourceSelection{}, fmt.Errorf("archive pre-adoption control file %s: %w", path, err)
			}
		}
	}
	if err := syncConfigDirectory(dataDir); err != nil {
		return SQLiteSourceSelection{}, fmt.Errorf("sync pre-adoption control directory: %w", err)
	}
	selection.State = SQLiteSourceStatePrepared
	if err := SaveSQLiteSourceSelection(dataDir, selection); err != nil {
		return SQLiteSourceSelection{}, fmt.Errorf("mark SQLite source control prepared: %w", err)
	}
	return selection, nil
}

func ActivateSQLiteSourceSelection(dataDir string) (SQLiteSourceSelection, error) {
	selection, ok, err := LoadSQLiteSourceSelection(dataDir)
	if err != nil {
		return SQLiteSourceSelection{}, err
	}
	if !ok {
		return SQLiteSourceSelection{}, errors.New("SQLite source selection is missing")
	}
	if selection.State == SQLiteSourceStateActive {
		return selection, nil
	}
	if selection.State != SQLiteSourceStatePrepared {
		return SQLiteSourceSelection{}, fmt.Errorf(
			"SQLite source selection cannot be activated from state %q", selection.State,
		)
	}
	selection.State = SQLiteSourceStateActive
	selection.ActivatedAtMS = time.Now().UnixMilli()
	if err := SaveSQLiteSourceSelection(dataDir, selection); err != nil {
		return SQLiteSourceSelection{}, err
	}
	return selection, nil
}

// RollbackSQLiteSourceSwitch restores the source and encrypted control files
// that were active before a runtime switch. It deliberately applies only to a
// pending/prepared switch: first-run adoption has no known-good running source
// to return to, and an active selection has already completed startup.
func RollbackSQLiteSourceSwitch(
	dataDir string,
	failure SQLiteSourceSwitchFailure,
) (SQLiteSourceSelection, bool, error) {
	selection, ok, err := LoadSQLiteSourceSelection(dataDir)
	if err != nil {
		return SQLiteSourceSelection{}, false, err
	}
	if !ok || selection.Operation != SQLiteSourceOperationSwitch ||
		(selection.State != SQLiteSourceStatePending && selection.State != SQLiteSourceStatePrepared) ||
		selection.PreviousDatabasePath == "" {
		return selection, false, nil
	}

	attemptedDatabasePath := selection.DatabasePath
	attemptedDataKeyPath := selection.DataKeyPath
	controlPrepared := selection.State == SQLiteSourceStatePrepared
	controlBackupExists, err := sqliteSourceControlBackupExists(dataDir, selection.ControlBackupSuffix)
	if err != nil {
		return SQLiteSourceSelection{}, false, fmt.Errorf("inspect previous SQLite control state: %w", err)
	}
	if controlPrepared || controlBackupExists {
		if err := restoreSQLiteSourceControl(dataDir, selection.ControlBackupSuffix, controlPrepared); err != nil {
			return SQLiteSourceSelection{}, false, fmt.Errorf("restore previous SQLite control state: %w", err)
		}
	}

	now := time.Now().UnixMilli()
	selection.State = SQLiteSourceStateActive
	selection.DatabasePath = selection.PreviousDatabasePath
	selection.DataKeyPath = selection.PreviousDataKeyPath
	selection.PreviousDatabasePath = ""
	selection.PreviousDataKeyPath = ""
	selection.ControlBackupSuffix = ""
	selection.IdempotencyKey = ""
	selection.ActivatedAtMS = now
	selection.FailedDatabasePath = attemptedDatabasePath
	selection.FailedDataKeyPath = attemptedDataKeyPath
	selection.LastErrorStage = strings.TrimSpace(failure.Stage)
	selection.LastErrorCause = strings.TrimSpace(failure.Cause)
	selection.FailedAtMS = now
	selection.LastError = sqliteSourceSwitchFailureMessage(
		attemptedDatabasePath,
		selection.LastErrorStage,
		selection.LastErrorCause,
	)
	if err := SaveSQLiteSourceSelection(dataDir, selection); err != nil {
		return SQLiteSourceSelection{}, false, fmt.Errorf("persist previous SQLite source after rollback: %w", err)
	}
	// Mirror the recovered record into the normal .bak rotation as well. A
	// fallback load must never resurrect the failed pending target if the main
	// selection file is later lost or found unreadable.
	if err := SaveSQLiteSourceSelection(dataDir, selection); err != nil {
		return SQLiteSourceSelection{}, false, fmt.Errorf("mirror previous SQLite source rollback backup: %w", err)
	}
	return selection, true, nil
}

func sqliteSourceControlBackupExists(dataDir, backupSuffix string) (bool, error) {
	backupSuffix = strings.TrimSpace(backupSuffix)
	if backupSuffix == "" {
		return false, nil
	}
	controlPath := filepath.Join(dataDir, "database-control.json.enc")
	for _, path := range []string{controlPath, controlPath + ".bak"} {
		if _, err := os.Stat(path + backupSuffix); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func restoreSQLiteSourceControl(dataDir, backupSuffix string, preparationComplete bool) error {
	backupSuffix = strings.TrimSpace(backupSuffix)
	if backupSuffix == "" {
		return errors.New("SQLite source switch control backup suffix is missing")
	}
	controlPath := filepath.Join(dataDir, "database-control.json.enc")
	for _, path := range []string{controlPath, controlPath + ".bak"} {
		backupPath := path + backupSuffix
		failedPath := backupPath + ".failed-target"
		_, backupErr := os.Stat(backupPath)
		switch {
		case backupErr == nil:
			if _, currentErr := os.Stat(path); currentErr == nil {
				if _, failedErr := os.Stat(failedPath); failedErr == nil {
					return fmt.Errorf("failed target control archive already exists: %s", failedPath)
				} else if !errors.Is(failedErr, os.ErrNotExist) {
					return fmt.Errorf("inspect failed target control archive %s: %w", failedPath, failedErr)
				}
				if err := os.Rename(path, failedPath); err != nil {
					return fmt.Errorf("archive failed target control file %s: %w", path, err)
				}
			} else if !errors.Is(currentErr, os.ErrNotExist) {
				return fmt.Errorf("inspect failed target control file %s: %w", path, currentErr)
			}
			if err := os.Rename(backupPath, path); err != nil {
				return fmt.Errorf("restore control backup %s: %w", backupPath, err)
			}
		case errors.Is(backupErr, os.ErrNotExist):
			if !preparationComplete {
				// Preparation failed before its durable state transition. Only paths
				// with an actual switch backup were moved; untouched current files
				// still belong to the previous source.
				continue
			}
			// Preparation completed but this control file did not exist for the
			// previous source. Preserve any file created for the failed target as
			// diagnostic evidence instead of deleting it.
			if _, failedErr := os.Stat(failedPath); failedErr == nil {
				// A prior rollback attempt already handled this path. If a current
				// file is also present it is the restored previous control file.
				continue
			} else if !errors.Is(failedErr, os.ErrNotExist) {
				return fmt.Errorf("inspect failed target control archive %s: %w", failedPath, failedErr)
			}
			if _, currentErr := os.Stat(path); currentErr == nil {
				if err := os.Rename(path, failedPath); err != nil {
					return fmt.Errorf("archive target-only control file %s: %w", path, err)
				}
			} else if !errors.Is(currentErr, os.ErrNotExist) {
				return fmt.Errorf("inspect target-only control file %s: %w", path, currentErr)
			}
		default:
			return fmt.Errorf("inspect control backup %s: %w", backupPath, backupErr)
		}
	}
	if err := syncConfigDirectory(dataDir); err != nil {
		return fmt.Errorf("sync restored SQLite control directory: %w", err)
	}
	return nil
}

func sqliteSourceSwitchFailureMessage(path, stage, cause string) string {
	message := "SQLite source switch startup failed"
	if strings.TrimSpace(path) != "" {
		message += fmt.Sprintf(" for %q", path)
	}
	if strings.TrimSpace(stage) != "" {
		message += " during " + strings.TrimSpace(stage)
	}
	if strings.TrimSpace(cause) != "" {
		message += ": " + strings.TrimSpace(cause)
	}
	return message + "; the previous SQLite source was restored"
}

func readSQLiteSourceSelection(path string) (SQLiteSourceSelection, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SQLiteSourceSelection{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var selection SQLiteSourceSelection
	if err := decoder.Decode(&selection); err != nil {
		return SQLiteSourceSelection{}, fmt.Errorf("decode %s: %w", path, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return SQLiteSourceSelection{}, fmt.Errorf("decode %s: multiple JSON values", path)
		}
		return SQLiteSourceSelection{}, fmt.Errorf("decode trailing data in %s: %w", path, err)
	}
	if err := normalizeSQLiteSourceSelection(&selection); err != nil {
		return SQLiteSourceSelection{}, fmt.Errorf("validate %s: %w", path, err)
	}
	return selection, nil
}

func normalizeSQLiteSourceSelection(selection *SQLiteSourceSelection) error {
	if selection == nil {
		return errors.New("SQLite source selection is required")
	}
	if selection.Version == 0 {
		selection.Version = SQLiteSourceSelectionVersion
	}
	if selection.Version != SQLiteSourceSelectionVersion {
		return fmt.Errorf("unsupported SQLite source selection version %d", selection.Version)
	}
	selection.State = strings.TrimSpace(selection.State)
	switch selection.State {
	case SQLiteSourceStatePending, SQLiteSourceStatePrepared, SQLiteSourceStateActive:
	default:
		return fmt.Errorf("invalid SQLite source selection state %q", selection.State)
	}
	selection.DatabasePath = filepath.Clean(strings.TrimSpace(selection.DatabasePath))
	if selection.DatabasePath == "." || !filepath.IsAbs(selection.DatabasePath) {
		return errors.New("SQLite source database path must be absolute")
	}
	selection.DataKeyPath = strings.TrimSpace(selection.DataKeyPath)
	if selection.DataKeyPath != "" {
		selection.DataKeyPath = filepath.Clean(selection.DataKeyPath)
		if !filepath.IsAbs(selection.DataKeyPath) {
			return errors.New("SQLite source data key path must be absolute")
		}
	}
	selection.Operation = strings.TrimSpace(selection.Operation)
	if selection.Operation != "" && selection.Operation != SQLiteSourceOperationAdopt &&
		selection.Operation != SQLiteSourceOperationSwitch {
		return fmt.Errorf("invalid SQLite source selection operation %q", selection.Operation)
	}
	for name, path := range map[string]*string{
		"previous database": &selection.PreviousDatabasePath,
		"previous data key": &selection.PreviousDataKeyPath,
		"failed database":   &selection.FailedDatabasePath,
		"failed data key":   &selection.FailedDataKeyPath,
	} {
		*path = strings.TrimSpace(*path)
		if *path == "" {
			continue
		}
		*path = filepath.Clean(*path)
		if !filepath.IsAbs(*path) {
			return fmt.Errorf("SQLite source %s path must be absolute", name)
		}
	}
	selection.ControlBackupSuffix = strings.TrimSpace(selection.ControlBackupSuffix)
	if selection.ControlBackupSuffix != "" {
		if filepath.Base(selection.ControlBackupSuffix) != selection.ControlBackupSuffix ||
			strings.ContainsAny(selection.ControlBackupSuffix, `/\\`) ||
			!strings.HasPrefix(selection.ControlBackupSuffix, ".before-sqlite-") {
			return errors.New("SQLite source control backup suffix is invalid")
		}
	}
	selection.IdempotencyKey = strings.TrimSpace(selection.IdempotencyKey)
	if len(selection.IdempotencyKey) > 200 {
		return errors.New("SQLite source idempotency key exceeds 200 characters")
	}
	selection.LastErrorStage = strings.TrimSpace(selection.LastErrorStage)
	selection.LastErrorCause = strings.TrimSpace(selection.LastErrorCause)
	selection.LastError = strings.TrimSpace(selection.LastError)
	return nil
}

func writeSQLiteSourceSelectionAtomic(path string, data []byte) (returnErr error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create SQLite source selection directory: %w", err)
	}
	temp, err := os.CreateTemp(directory, ".sqlite-source-selection-*.tmp")
	if err != nil {
		return fmt.Errorf("create SQLite source selection temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		if returnErr != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}

	backupPath := path + ".bak"
	hadCurrent := false
	if _, err := os.Stat(path); err == nil {
		hadCurrent = true
		if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale SQLite source selection backup: %w", err)
		}
		if err := os.Rename(path, backupPath); err != nil {
			return fmt.Errorf("rotate SQLite source selection backup: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect SQLite source selection: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		if hadCurrent {
			_ = os.Rename(backupPath, path)
		}
		return fmt.Errorf("install SQLite source selection: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict SQLite source selection permissions: %w", err)
	}
	if err := syncConfigDirectory(directory); err != nil {
		return fmt.Errorf("sync SQLite source selection directory: %w", err)
	}
	return nil
}

func syncConfigDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
