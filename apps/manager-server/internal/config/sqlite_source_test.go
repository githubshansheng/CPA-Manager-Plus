package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteSourceSelectionDrivesNextConfigLoad(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	sourcePath := filepath.Join(root, "legacy", "usage.sqlite")
	dataKeyPath := filepath.Join(root, "legacy", "data.key")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataKeyPath, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveSQLiteSourceSelection(dataDir, SQLiteSourceSelection{
		Version:      SQLiteSourceSelectionVersion,
		State:        SQLiteSourceStatePending,
		DatabasePath: sourcePath,
		DataKeyPath:  dataKeyPath,
	}); err != nil {
		t.Fatalf("SaveSQLiteSourceSelection() error = %v", err)
	}

	configPath := filepath.Join(root, "config.json")
	t.Setenv(configEnvKey, configPath)
	if err := os.WriteFile(configPath, []byte(`{"dataDir":"data"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DBPath != sourcePath || cfg.DataKeyPath != dataKeyPath {
		t.Fatalf("adopted paths = %q, %q", cfg.DBPath, cfg.DataKeyPath)
	}
	if cfg.SQLiteSourceState != SQLiteSourceStatePending || cfg.SQLiteSourceSelectionPath == "" {
		t.Fatalf("adopted source metadata = %#v", cfg)
	}
}

func TestSQLiteSourceSelectionRejectsConflictingEnvironmentPath(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	sourcePath := filepath.Join(root, "legacy", "usage.sqlite")
	if err := SaveSQLiteSourceSelection(dataDir, SQLiteSourceSelection{
		State:        SQLiteSourceStateActive,
		DatabasePath: sourcePath,
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configEnvKey, filepath.Join(root, "config.json"))
	t.Setenv("USAGE_DATA_DIR", dataDir)
	t.Setenv("USAGE_DB_PATH", filepath.Join(root, "different.sqlite"))

	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want environment conflict")
	}
}

func TestPrepareAndActivateSQLiteSourceControl(t *testing.T) {
	dataDir := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "usage.sqlite")
	if err := SaveSQLiteSourceSelection(dataDir, SQLiteSourceSelection{
		State:        SQLiteSourceStatePending,
		DatabasePath: sourcePath,
	}); err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(dataDir, "database-control.json.enc")
	if err := os.WriteFile(controlPath, []byte("old-control"), 0o600); err != nil {
		t.Fatal(err)
	}

	prepared, err := PrepareSQLiteSourceControl(dataDir)
	if err != nil {
		t.Fatalf("PrepareSQLiteSourceControl() error = %v", err)
	}
	if prepared.State != SQLiteSourceStatePrepared {
		t.Fatalf("prepared state = %q", prepared.State)
	}
	backupPath := controlPath + preAdoptionBackupSuffix
	if data, err := os.ReadFile(backupPath); err != nil || string(data) != "old-control" {
		t.Fatalf("control backup data=%q err=%v", data, err)
	}
	if _, err := os.Stat(controlPath); !os.IsNotExist(err) {
		t.Fatalf("current control file still exists: %v", err)
	}

	active, err := ActivateSQLiteSourceSelection(dataDir)
	if err != nil {
		t.Fatalf("ActivateSQLiteSourceSelection() error = %v", err)
	}
	if active.State != SQLiteSourceStateActive || active.ActivatedAtMS == 0 {
		t.Fatalf("active selection = %#v", active)
	}
}

func TestPrepareSQLiteSourceControlUsesSelectionSpecificBackupForRepeatedSwitches(t *testing.T) {
	dataDir := t.TempDir()
	controlPath := filepath.Join(dataDir, "database-control.json.enc")
	if err := os.WriteFile(controlPath, []byte("current-control"), 0o600); err != nil {
		t.Fatal(err)
	}
	const suffix = ".before-sqlite-switch-123456"
	if err := SaveSQLiteSourceSelection(dataDir, SQLiteSourceSelection{
		State:               SQLiteSourceStatePending,
		Operation:           SQLiteSourceOperationSwitch,
		DatabasePath:        filepath.Join(t.TempDir(), "usage.sqlite"),
		ControlBackupSuffix: suffix,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSQLiteSourceControl(dataDir); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(controlPath + suffix); err != nil || string(data) != "current-control" {
		t.Fatalf("switch backup data=%q err=%v", data, err)
	}
	if _, err := os.Stat(controlPath + preAdoptionBackupSuffix); !os.IsNotExist(err) {
		t.Fatalf("legacy fixed backup unexpectedly used: %v", err)
	}
}

func TestRollbackPendingSQLiteSourceSwitchRestoresPreviousSelection(t *testing.T) {
	dataDir := t.TempDir()
	previousDatabasePath := filepath.Join(t.TempDir(), "previous.sqlite")
	previousDataKeyPath := filepath.Join(t.TempDir(), "previous-data.key")
	failedDatabasePath := filepath.Join(t.TempDir(), "failed.sqlite")
	failedDataKeyPath := filepath.Join(t.TempDir(), "failed-data.key")
	if err := SaveSQLiteSourceSelection(dataDir, SQLiteSourceSelection{
		State:                SQLiteSourceStatePending,
		Operation:            SQLiteSourceOperationSwitch,
		DatabasePath:         failedDatabasePath,
		DataKeyPath:          failedDataKeyPath,
		PreviousDatabasePath: previousDatabasePath,
		PreviousDataKeyPath:  previousDataKeyPath,
		ControlBackupSuffix:  ".before-sqlite-switch-123",
		IdempotencyKey:       "switch-123",
	}); err != nil {
		t.Fatal(err)
	}

	restored, ok, err := RollbackSQLiteSourceSwitch(dataDir, SQLiteSourceSwitchFailure{
		Stage: "process lock",
		Cause: "database is already in use",
	})
	if err != nil || !ok {
		t.Fatalf("RollbackSQLiteSourceSwitch() ok=%v error=%v", ok, err)
	}
	if restored.State != SQLiteSourceStateActive ||
		restored.DatabasePath != previousDatabasePath ||
		restored.DataKeyPath != previousDataKeyPath ||
		restored.FailedDatabasePath != failedDatabasePath ||
		restored.FailedDataKeyPath != failedDataKeyPath ||
		restored.LastErrorStage != "process lock" ||
		restored.LastErrorCause != "database is already in use" ||
		restored.LastError == "" || restored.FailedAtMS == 0 ||
		restored.PreviousDatabasePath != "" || restored.IdempotencyKey != "" {
		t.Fatalf("restored selection = %#v", restored)
	}
	if err := os.Remove(SQLiteSourceSelectionPath(dataDir)); err != nil {
		t.Fatal(err)
	}
	fallback, ok, err := LoadSQLiteSourceSelection(dataDir)
	if err != nil || !ok || fallback.State != SQLiteSourceStateActive ||
		fallback.DatabasePath != previousDatabasePath || fallback.FailedDatabasePath != failedDatabasePath {
		t.Fatalf("rollback backup fallback = %#v ok=%v error=%v", fallback, ok, err)
	}
}

func TestRollbackPreparedSQLiteSourceSwitchRestoresControlFiles(t *testing.T) {
	dataDir := t.TempDir()
	controlPath := filepath.Join(dataDir, "database-control.json.enc")
	if err := os.WriteFile(controlPath, []byte("previous-main"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(controlPath+".bak", []byte("previous-backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	const suffix = ".before-sqlite-switch-456"
	if err := SaveSQLiteSourceSelection(dataDir, SQLiteSourceSelection{
		State:                SQLiteSourceStatePending,
		Operation:            SQLiteSourceOperationSwitch,
		DatabasePath:         filepath.Join(t.TempDir(), "failed.sqlite"),
		PreviousDatabasePath: filepath.Join(t.TempDir(), "previous.sqlite"),
		ControlBackupSuffix:  suffix,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSQLiteSourceControl(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(controlPath, []byte("failed-main"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(controlPath+".bak", []byte("failed-backup"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := RollbackSQLiteSourceSwitch(dataDir, SQLiteSourceSwitchFailure{
		Stage: "database open", Cause: "invalid data key",
	}); err != nil || !ok {
		t.Fatalf("RollbackSQLiteSourceSwitch() ok=%v error=%v", ok, err)
	}
	for path, want := range map[string]string{
		controlPath:          "previous-main",
		controlPath + ".bak": "previous-backup",
	} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("restored %s data=%q error=%v", path, data, err)
		}
		failedData, err := os.ReadFile(path + suffix + ".failed-target")
		if err != nil || !strings.HasPrefix(string(failedData), "failed-") {
			t.Fatalf("failed target archive %s data=%q error=%v", path, failedData, err)
		}
	}
}

func TestRollbackPendingSQLiteSourceSwitchRestoresOnlyPartiallyArchivedControl(t *testing.T) {
	dataDir := t.TempDir()
	controlPath := filepath.Join(dataDir, "database-control.json.enc")
	const suffix = ".before-sqlite-switch-partial"
	if err := os.WriteFile(controlPath+suffix, []byte("previous-main"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(controlPath+".bak", []byte("untouched-previous-backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveSQLiteSourceSelection(dataDir, SQLiteSourceSelection{
		State:                SQLiteSourceStatePending,
		Operation:            SQLiteSourceOperationSwitch,
		DatabasePath:         filepath.Join(t.TempDir(), "failed.sqlite"),
		PreviousDatabasePath: filepath.Join(t.TempDir(), "previous.sqlite"),
		ControlBackupSuffix:  suffix,
	}); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := RollbackSQLiteSourceSwitch(dataDir, SQLiteSourceSwitchFailure{
		Stage: "prepare SQLite control state", Cause: "simulated selection write failure",
	}); err != nil || !ok {
		t.Fatalf("RollbackSQLiteSourceSwitch() ok=%v error=%v", ok, err)
	}
	for path, want := range map[string]string{
		controlPath:          "previous-main",
		controlPath + ".bak": "untouched-previous-backup",
	} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("restored %s data=%q error=%v", path, data, err)
		}
	}
}
