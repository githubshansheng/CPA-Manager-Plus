package setup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	_ "modernc.org/sqlite"
)

const (
	SQLiteAdoptionCodeUnavailable          = "sqlite_adoption_unavailable"
	SQLiteAdoptionCodePathRequired         = "sqlite_source_path_required"
	SQLiteAdoptionCodeEnvironmentManaged   = "sqlite_source_env_managed"
	SQLiteAdoptionCodeSameAsCurrent        = "sqlite_source_same_as_current"
	SQLiteAdoptionCodeLocked               = "sqlite_source_locked"
	SQLiteAdoptionCodeInvalid              = "sqlite_source_invalid"
	SQLiteAdoptionCodeNotWritable          = "sqlite_source_not_writable"
	SQLiteAdoptionCodeDataKeyInvalid       = "sqlite_source_data_key_invalid"
	SQLiteAdoptionCodeDataKeyConflict      = "sqlite_source_data_key_env_conflict"
	SQLiteAdoptionCodeAdminKeyInvalid      = "sqlite_source_admin_key_invalid"
	SQLiteAdoptionCodeParametersRequired   = "sqlite_source_parameters_required"
	SQLiteAdoptionCodeConfirmationRequired = "sqlite_source_confirmation_required"
	SQLiteAdoptionCodeSelectionConflict    = "sqlite_source_selection_conflict"
	SQLiteAdoptionCodeTopologyUnsafe       = "sqlite_source_topology_unsafe"
	SQLiteAdoptionCodeFailed               = "sqlite_source_adoption_failed"
)

type SQLiteAdoptionRequest struct {
	SourcePath           string `json:"sourcePath"`
	DataKeyPath          string `json:"dataKeyPath,omitempty"`
	SourceAdminKey       string `json:"sourceAdminKey,omitempty"`
	ConfirmSourceStopped bool   `json:"confirmSourceStopped,omitempty"`
}

type SQLiteSourceSwitchRequest struct {
	SQLiteAdoptionRequest
	ExpectedGeneration uint64 `json:"expectedGeneration"`
	IdempotencyKey     string `json:"idempotencyKey"`
}

type SQLiteAdoptionPreflightResult struct {
	Ready                  bool     `json:"ready"`
	SourcePath             string   `json:"sourcePath"`
	DataKeyPath            string   `json:"dataKeyPath,omitempty"`
	DatabaseBytes          int64    `json:"databaseBytes"`
	WALBytes               int64    `json:"walBytes,omitempty"`
	SHMBytes               int64    `json:"shmBytes,omitempty"`
	HasHistoricalData      bool     `json:"hasHistoricalData"`
	HasEncryptedConnection bool     `json:"hasEncryptedConnection"`
	DataKeyRequired        bool     `json:"dataKeyRequired"`
	DataKeyVerified        bool     `json:"dataKeyVerified"`
	AdminKeyRequired       bool     `json:"adminKeyRequired"`
	AdminKeyVerified       bool     `json:"adminKeyVerified"`
	AdminKeyWillBeCreated  bool     `json:"adminKeyWillBeCreated"`
	AdminKeyWillBeRetained bool     `json:"adminKeyWillBeRetained"`
	ProjectInitialized     bool     `json:"projectInitialized"`
	RequiredParameters     []string `json:"requiredParameters"`
	LockRisk               bool     `json:"lockRisk"`
	RestartRequired        bool     `json:"restartRequired"`
}

type SQLiteAdoptionResult struct {
	OK               bool   `json:"ok"`
	SourcePath       string `json:"sourcePath"`
	DataKeyPath      string `json:"dataKeyPath,omitempty"`
	RestartRequired  bool   `json:"restartRequired"`
	SelectionState   string `json:"selectionState"`
	AdminKeyRetained bool   `json:"adminKeyRetained,omitempty"`
}

type SQLiteSourceStatus struct {
	CurrentPath        string `json:"currentPath"`
	CurrentDataKeyPath string `json:"currentDataKeyPath,omitempty"`
	SelectionState     string `json:"selectionState,omitempty"`
	SelectionOperation string `json:"selectionOperation,omitempty"`
	PendingPath        string `json:"pendingPath,omitempty"`
	PendingDataKeyPath string `json:"pendingDataKeyPath,omitempty"`
	SelectedAtMS       int64  `json:"selectedAtMs,omitempty"`
	ActivatedAtMS      int64  `json:"activatedAtMs,omitempty"`
	RestartRequired    bool   `json:"restartRequired"`
	FailedPath         string `json:"failedPath,omitempty"`
	FailedDataKeyPath  string `json:"failedDataKeyPath,omitempty"`
	LastErrorStage     string `json:"lastErrorStage,omitempty"`
	LastErrorCause     string `json:"lastErrorCause,omitempty"`
	LastError          string `json:"lastError,omitempty"`
	FailedAtMS         int64  `json:"failedAtMs,omitempty"`
}

type SQLiteAdoptionError struct {
	Code       string
	Stage      string
	SourcePath string
	Cause      error
}

func (e *SQLiteAdoptionError) Error() string {
	if e == nil {
		return "SQLite source adoption failed"
	}
	prefix := "SQLite source adoption"
	if e.Stage != "" {
		prefix += " " + e.Stage
	}
	if e.SourcePath != "" {
		prefix += fmt.Sprintf(" for %q", e.SourcePath)
	}
	if e.Cause == nil {
		return prefix + " failed"
	}
	return prefix + " failed: " + e.Cause.Error()
}

func (e *SQLiteAdoptionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func SQLiteAdoptionErrorDetails(err error) (code string, details map[string]any) {
	var adoptionErr *SQLiteAdoptionError
	if !errors.As(err, &adoptionErr) {
		return SQLiteAdoptionCodeFailed, nil
	}
	code = adoptionErr.Code
	details = map[string]any{}
	if adoptionErr.Stage != "" {
		details["stage"] = adoptionErr.Stage
	}
	if adoptionErr.SourcePath != "" {
		details["sourcePath"] = adoptionErr.SourcePath
	}
	if adoptionErr.Cause != nil {
		details["cause"] = adoptionErr.Cause.Error()
	}
	return code, details
}

func SQLiteAdoptionErrorStatus(err error) int {
	code, _ := SQLiteAdoptionErrorDetails(err)
	switch code {
	case SQLiteAdoptionCodeLocked:
		return http.StatusLocked
	case SQLiteAdoptionCodeUnavailable, SQLiteAdoptionCodeEnvironmentManaged,
		SQLiteAdoptionCodeSameAsCurrent, SQLiteAdoptionCodeSelectionConflict,
		SQLiteAdoptionCodeTopologyUnsafe:
		return http.StatusConflict
	case SQLiteAdoptionCodeInvalid, SQLiteAdoptionCodeNotWritable,
		SQLiteAdoptionCodeDataKeyInvalid, SQLiteAdoptionCodeDataKeyConflict,
		SQLiteAdoptionCodeAdminKeyInvalid, SQLiteAdoptionCodeParametersRequired:
		return http.StatusUnprocessableEntity
	case SQLiteAdoptionCodePathRequired, SQLiteAdoptionCodeConfirmationRequired:
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func (s *Service) PreflightSQLiteAdoption(ctx context.Context, req SQLiteAdoptionRequest) (SQLiteAdoptionPreflightResult, error) {
	result, lock, err := s.inspectSQLiteAdoption(ctx, req, false)
	if lock != nil {
		_ = lock.Close()
	}
	return result, err
}

func (s *Service) PreflightSQLiteSwitch(ctx context.Context, req SQLiteAdoptionRequest) (SQLiteAdoptionPreflightResult, error) {
	result, lock, err := s.inspectSQLiteAdoption(ctx, req, true)
	if lock != nil {
		_ = lock.Close()
	}
	return result, err
}

func (s *Service) SQLiteSourceStatus() (SQLiteSourceStatus, error) {
	dataDir := strings.TrimSpace(s.cfg.DataDir)
	if dataDir == "" {
		dataDir = filepath.Dir(s.cfg.DBPath)
	}
	status := SQLiteSourceStatus{
		CurrentPath:        s.cfg.DBPath,
		CurrentDataKeyPath: s.cfg.DataKeyPath,
	}
	selection, ok, err := config.LoadSQLiteSourceSelection(dataDir)
	if err != nil || !ok {
		return status, err
	}
	status.SelectionState = selection.State
	status.SelectionOperation = selection.Operation
	status.SelectedAtMS = selection.SelectedAtMS
	status.ActivatedAtMS = selection.ActivatedAtMS
	status.FailedPath = selection.FailedDatabasePath
	status.FailedDataKeyPath = selection.FailedDataKeyPath
	status.LastErrorStage = selection.LastErrorStage
	status.LastErrorCause = selection.LastErrorCause
	status.LastError = selection.LastError
	status.FailedAtMS = selection.FailedAtMS
	if selection.State == config.SQLiteSourceStatePending || selection.State == config.SQLiteSourceStatePrepared {
		status.PendingPath = selection.DatabasePath
		status.PendingDataKeyPath = selection.DataKeyPath
		status.RestartRequired = !sameSQLitePath(s.cfg.DBPath, selection.DatabasePath) ||
			!sameOptionalSQLitePath(s.cfg.DataKeyPath, selection.DataKeyPath)
	}
	return status, nil
}

func (s *Service) AdoptSQLite(ctx context.Context, req SQLiteAdoptionRequest) (SQLiteAdoptionResult, error) {
	if !req.ConfirmSourceStopped {
		return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
			SQLiteAdoptionCodeConfirmationRequired,
			"confirmation",
			strings.TrimSpace(req.SourcePath),
			errors.New("confirmSourceStopped must be true after every process using the SQLite source has been stopped"),
		)
	}
	result, lock, err := s.inspectSQLiteAdoption(ctx, req, false)
	if lock != nil {
		defer lock.Close()
	}
	if err != nil {
		return SQLiteAdoptionResult{}, err
	}
	if !result.Ready {
		return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
			SQLiteAdoptionCodeParametersRequired,
			"parameters",
			result.SourcePath,
			fmt.Errorf("required parameters are missing: %s", strings.Join(result.RequiredParameters, ", ")),
		)
	}

	dataDir := strings.TrimSpace(s.cfg.DataDir)
	if dataDir == "" {
		dataDir = filepath.Dir(s.cfg.DBPath)
	}
	adminKeyRetained, err := s.retainCurrentAdminCredential(ctx, result)
	if err != nil {
		return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
			SQLiteAdoptionCodeFailed, "administrator credential retention", result.SourcePath, err,
		)
	}
	selection := config.SQLiteSourceSelection{
		Version:              config.SQLiteSourceSelectionVersion,
		State:                config.SQLiteSourceStatePending,
		Operation:            config.SQLiteSourceOperationAdopt,
		DatabasePath:         result.SourcePath,
		DataKeyPath:          result.DataKeyPath,
		PreviousDatabasePath: s.cfg.DBPath,
		PreviousDataKeyPath:  s.cfg.DataKeyPath,
		SelectedAtMS:         time.Now().UnixMilli(),
	}
	if existing, ok, loadErr := config.LoadSQLiteSourceSelection(dataDir); loadErr != nil {
		return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
			SQLiteAdoptionCodeFailed, "load selection", result.SourcePath, loadErr,
		)
	} else if ok {
		if !sameSQLitePath(existing.DatabasePath, selection.DatabasePath) ||
			!sameOptionalSQLitePath(existing.DataKeyPath, selection.DataKeyPath) {
			return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
				SQLiteAdoptionCodeSelectionConflict,
				"persist selection",
				result.SourcePath,
				errors.New("a different SQLite source selection already exists"),
			)
		}
		return SQLiteAdoptionResult{
			OK: true, SourcePath: existing.DatabasePath, DataKeyPath: existing.DataKeyPath,
			RestartRequired: existing.State != config.SQLiteSourceStateActive,
			SelectionState:  existing.State, AdminKeyRetained: adminKeyRetained,
		}, nil
	}
	if err := config.SaveSQLiteSourceSelection(dataDir, selection); err != nil {
		return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
			SQLiteAdoptionCodeFailed, "persist selection", result.SourcePath, err,
		)
	}
	return SQLiteAdoptionResult{
		OK: true, SourcePath: result.SourcePath, DataKeyPath: result.DataKeyPath,
		RestartRequired: true, SelectionState: config.SQLiteSourceStatePending,
		AdminKeyRetained: adminKeyRetained,
	}, nil
}

func (s *Service) SwitchSQLiteSource(ctx context.Context, req SQLiteSourceSwitchRequest) (SQLiteAdoptionResult, error) {
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.ExpectedGeneration == 0 || req.IdempotencyKey == "" || len(req.IdempotencyKey) > 200 {
		return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
			SQLiteAdoptionCodeInvalid,
			"mutation control",
			strings.TrimSpace(req.SourcePath),
			errors.New("expectedGeneration and an idempotencyKey of at most 200 characters are required"),
		)
	}
	if !req.ConfirmSourceStopped {
		return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
			SQLiteAdoptionCodeConfirmationRequired,
			"confirmation",
			strings.TrimSpace(req.SourcePath),
			errors.New("confirmSourceStopped must be true after every process using the SQLite source has been stopped"),
		)
	}
	dataDir := strings.TrimSpace(s.cfg.DataDir)
	if dataDir == "" {
		dataDir = filepath.Dir(s.cfg.DBPath)
	}
	existingSelection, existingSelectionOK, err := config.LoadSQLiteSourceSelection(dataDir)
	if err != nil {
		return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
			SQLiteAdoptionCodeFailed, "load selection", strings.TrimSpace(req.SourcePath), err,
		)
	}
	if existingSelectionOK && existingSelection.IdempotencyKey == req.IdempotencyKey {
		requestedDataKeyPath := strings.TrimSpace(req.DataKeyPath)
		if !sameSQLitePath(existingSelection.DatabasePath, req.SourcePath) ||
			(requestedDataKeyPath != "" && !sameOptionalSQLitePath(existingSelection.DataKeyPath, requestedDataKeyPath)) {
			return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
				SQLiteAdoptionCodeSelectionConflict,
				"idempotency validation",
				strings.TrimSpace(req.SourcePath),
				errors.New("idempotencyKey was already used for a different SQLite source"),
			)
		}
		return SQLiteAdoptionResult{
			OK: true, SourcePath: existingSelection.DatabasePath, DataKeyPath: existingSelection.DataKeyPath,
			RestartRequired: existingSelection.State != config.SQLiteSourceStateActive,
			SelectionState:  existingSelection.State,
		}, nil
	}
	result, lock, err := s.inspectSQLiteAdoption(ctx, req.SQLiteAdoptionRequest, true)
	if lock != nil {
		defer lock.Close()
	}
	if err != nil {
		return SQLiteAdoptionResult{}, err
	}
	if !result.Ready {
		return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
			SQLiteAdoptionCodeParametersRequired,
			"parameters",
			result.SourcePath,
			fmt.Errorf("required parameters are missing: %s", strings.Join(result.RequiredParameters, ", ")),
		)
	}
	adminKeyRetained, err := s.retainCurrentAdminCredential(ctx, result)
	if err != nil {
		return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
			SQLiteAdoptionCodeFailed, "administrator credential retention", result.SourcePath, err,
		)
	}

	selectedAt := time.Now().UnixMilli()
	selection := config.SQLiteSourceSelection{
		Version:              config.SQLiteSourceSelectionVersion,
		State:                config.SQLiteSourceStatePending,
		Operation:            config.SQLiteSourceOperationSwitch,
		DatabasePath:         result.SourcePath,
		DataKeyPath:          result.DataKeyPath,
		PreviousDatabasePath: s.cfg.DBPath,
		PreviousDataKeyPath:  s.cfg.DataKeyPath,
		ControlBackupSuffix:  fmt.Sprintf(".before-sqlite-switch-%d", selectedAt),
		IdempotencyKey:       req.IdempotencyKey,
		SelectedAtMS:         selectedAt,
	}
	if existing, ok := existingSelection, existingSelectionOK; ok {
		if existing.State == config.SQLiteSourceStatePrepared {
			return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
				SQLiteAdoptionCodeSelectionConflict,
				"persist selection",
				result.SourcePath,
				errors.New("a SQLite source switch is already being prepared by a restarting process"),
			)
		}
		if existing.IdempotencyKey == req.IdempotencyKey {
			if !sameSQLitePath(existing.DatabasePath, selection.DatabasePath) ||
				!sameOptionalSQLitePath(existing.DataKeyPath, selection.DataKeyPath) {
				return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
					SQLiteAdoptionCodeSelectionConflict,
					"idempotency validation",
					result.SourcePath,
					errors.New("idempotencyKey was already used for a different SQLite source"),
				)
			}
			return SQLiteAdoptionResult{
				OK: true, SourcePath: existing.DatabasePath, DataKeyPath: existing.DataKeyPath,
				RestartRequired: existing.State != config.SQLiteSourceStateActive,
				SelectionState:  existing.State, AdminKeyRetained: adminKeyRetained,
			}, nil
		}
	}
	if err := config.SaveSQLiteSourceSelection(dataDir, selection); err != nil {
		return SQLiteAdoptionResult{}, newSQLiteAdoptionError(
			SQLiteAdoptionCodeFailed, "persist selection", result.SourcePath, err,
		)
	}
	return SQLiteAdoptionResult{
		OK: true, SourcePath: result.SourcePath, DataKeyPath: result.DataKeyPath,
		RestartRequired: true, SelectionState: config.SQLiteSourceStatePending,
		AdminKeyRetained: adminKeyRetained,
	}, nil
}

func (s *Service) inspectSQLiteAdoption(
	ctx context.Context,
	req SQLiteAdoptionRequest,
	allowInitialized bool,
) (SQLiteAdoptionPreflightResult, *processlock.Lock, error) {
	availabilityErr := s.requireSQLiteAdoptionAvailable(ctx)
	if allowInitialized {
		availabilityErr = s.requireSQLiteSwitchAvailable(ctx)
	}
	if availabilityErr != nil {
		return SQLiteAdoptionPreflightResult{}, nil, availabilityErr
	}
	requestedPath := strings.TrimSpace(req.SourcePath)
	if requestedPath == "" {
		return SQLiteAdoptionPreflightResult{}, nil, newSQLiteAdoptionError(
			SQLiteAdoptionCodePathRequired, "path validation", "", errors.New("sourcePath is required"),
		)
	}
	if !filepath.IsAbs(requestedPath) {
		return SQLiteAdoptionPreflightResult{}, nil, newSQLiteAdoptionError(
			SQLiteAdoptionCodeInvalid, "path validation", requestedPath,
			errors.New("sourcePath must be an absolute path on the Manager Server host"),
		)
	}
	if s.cfg.DBPathEnvSet {
		return SQLiteAdoptionPreflightResult{}, nil, newSQLiteAdoptionError(
			SQLiteAdoptionCodeEnvironmentManaged, "configuration", requestedPath,
			errors.New("USAGE_DB_PATH is set; remove the environment override before selecting a source in the panel"),
		)
	}
	if sameSQLitePath(s.cfg.DBPath, requestedPath) {
		return SQLiteAdoptionPreflightResult{}, nil, newSQLiteAdoptionError(
			SQLiteAdoptionCodeSameAsCurrent, "path validation", requestedPath,
			errors.New("the selected source is already the database opened by this Manager Server"),
		)
	}

	lock, err := processlock.Acquire(requestedPath)
	if err != nil {
		code := SQLiteAdoptionCodeInvalid
		stage := "process lock"
		cause := err
		if errors.Is(err, processlock.ErrLocked) {
			code = SQLiteAdoptionCodeLocked
			cause = fmt.Errorf("the source process lock is held; stop the project using this database and retry: %w", err)
		}
		return SQLiteAdoptionPreflightResult{}, nil, newSQLiteAdoptionError(code, stage, requestedPath, cause)
	}
	canonicalPath := lock.DatabasePath()
	fail := func(code, stage string, cause error) (SQLiteAdoptionPreflightResult, *processlock.Lock, error) {
		_ = lock.Close()
		return SQLiteAdoptionPreflightResult{}, nil, newSQLiteAdoptionError(code, stage, canonicalPath, cause)
	}

	info, err := os.Stat(canonicalPath)
	if err != nil {
		return fail(SQLiteAdoptionCodeInvalid, "file inspection", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fail(SQLiteAdoptionCodeInvalid, "file inspection", errors.New("source must be a non-empty regular SQLite file"))
	}
	file, err := os.OpenFile(canonicalPath, os.O_RDWR, 0)
	if err != nil {
		return fail(SQLiteAdoptionCodeNotWritable, "write access", err)
	}
	if err := file.Close(); err != nil {
		return fail(SQLiteAdoptionCodeNotWritable, "write access", err)
	}

	db, err := openSQLiteAdoptionDatabase(canonicalPath, "ro", 250)
	if err != nil {
		return fail(SQLiteAdoptionCodeInvalid, "open read-only", err)
	}
	inspection, inspectErr := inspectSQLiteAdoptionDatabase(ctx, db)
	closeErr := db.Close()
	if inspectErr != nil || closeErr != nil {
		return fail(SQLiteAdoptionCodeInvalid, "database validation", errors.Join(inspectErr, closeErr))
	}
	if err := probeSQLiteAdoptionWriteLock(ctx, canonicalPath); err != nil {
		code := SQLiteAdoptionCodeNotWritable
		if sqliteLockError(err) {
			code = SQLiteAdoptionCodeLocked
		}
		return fail(code, "write-lock probe", err)
	}

	storageInspection, err := sqliterepo.InspectPersistedCPAConnectionStorage(ctx, canonicalPath)
	if err != nil {
		return fail(SQLiteAdoptionCodeInvalid, "encrypted connection inspection", err)
	}
	result := SQLiteAdoptionPreflightResult{
		SourcePath:             canonicalPath,
		DatabaseBytes:          info.Size(),
		WALBytes:               existingFileSize(canonicalPath + "-wal"),
		SHMBytes:               existingFileSize(canonicalPath + "-shm"),
		HasHistoricalData:      inspection.hasHistoricalData,
		HasEncryptedConnection: storageInspection.HasEncryptedConnection,
		DataKeyRequired:        storageInspection.HasEncryptedConnection,
		AdminKeyRequired:       inspection.adminCredential != nil,
		AdminKeyWillBeCreated:  false,
		AdminKeyWillBeRetained: inspection.adminCredential == nil,
		RequiredParameters:     []string{},
		LockRisk:               true,
		RestartRequired:        true,
	}

	var protector *security.Protector
	if storageInspection.HasEncryptedConnection {
		key, dataKeyPath, missing, keyErr := s.resolveSQLiteAdoptionDataKey(req, canonicalPath)
		if keyErr != nil {
			return fail(SQLiteAdoptionErrorCode(keyErr), "data key validation", keyErr)
		}
		result.DataKeyPath = dataKeyPath
		if missing {
			result.RequiredParameters = append(result.RequiredParameters, "dataKeyPath")
		} else {
			protector, err = security.NewProtector(key)
			if err != nil {
				return fail(SQLiteAdoptionCodeDataKeyInvalid, "data key validation", err)
			}
			if err := verifySQLiteAdoptionEncryptedValues(inspection.settings, protector); err != nil {
				return fail(SQLiteAdoptionCodeDataKeyInvalid, "data key validation", err)
			}
			result.DataKeyVerified = true
		}
	}

	if inspection.adminCredential != nil {
		sourceAdminKey := strings.TrimSpace(req.SourceAdminKey)
		if sourceAdminKey == "" {
			result.RequiredParameters = append(result.RequiredParameters, "sourceAdminKey")
		} else if !security.VerifyAdminKey(*inspection.adminCredential, sourceAdminKey) {
			return fail(
				SQLiteAdoptionCodeAdminKeyInvalid,
				"source administrator verification",
				errors.New("sourceAdminKey does not match the administrator credential stored in the selected database"),
			)
		} else {
			result.AdminKeyVerified = true
		}
	}
	result.ProjectInitialized, err = sqliteAdoptionProjectInitialized(inspection.settings, protector)
	if err != nil {
		return fail(SQLiteAdoptionCodeDataKeyInvalid, "connection decryption", err)
	}
	sort.Strings(result.RequiredParameters)
	result.Ready = len(result.RequiredParameters) == 0
	return result, lock, nil
}

func (s *Service) requireSQLiteAdoptionAvailable(ctx context.Context) error {
	info, err := s.Info(ctx)
	if err != nil {
		return newSQLiteAdoptionError(SQLiteAdoptionCodeFailed, "setup state", "", err)
	}
	if info.ProjectInitialized || !info.SetupRequired {
		return newSQLiteAdoptionError(
			SQLiteAdoptionCodeUnavailable,
			"setup state",
			"",
			errors.New("SQLite source adoption is available only while first-time setup is required"),
		)
	}
	return nil
}

func (s *Service) requireSQLiteSwitchAvailable(ctx context.Context) error {
	info, err := s.Info(ctx)
	if err != nil {
		return newSQLiteAdoptionError(SQLiteAdoptionCodeFailed, "setup state", "", err)
	}
	if !info.ProjectInitialized || info.SetupRequired {
		return newSQLiteAdoptionError(
			SQLiteAdoptionCodeUnavailable,
			"setup state",
			"",
			errors.New("SQLite source switching is available only after project initialization is complete"),
		)
	}
	return nil
}

func (s *Service) retainCurrentAdminCredential(
	ctx context.Context,
	result SQLiteAdoptionPreflightResult,
) (bool, error) {
	if !result.AdminKeyWillBeRetained {
		return false, nil
	}
	credential, ok, err := s.store.LoadAdminCredential(ctx)
	if err != nil {
		return false, fmt.Errorf("load current administrator credential: %w", err)
	}
	if !ok {
		return false, errors.New("current administrator credential is not initialized")
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return false, fmt.Errorf("encode current administrator credential: %w", err)
	}
	db, err := openSQLiteAdoptionDatabase(result.SourcePath, "rw", 2_000)
	if err != nil {
		return false, fmt.Errorf("open source for administrator credential retention: %w", err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin administrator credential retention: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `create table if not exists settings (
		key text primary key,
		value text not null,
		updated_at_ms integer not null
	)`); err != nil {
		return false, fmt.Errorf("ensure source settings table: %w", err)
	}
	var existing string
	err = tx.QueryRowContext(ctx, `select value from settings where key = 'admin_credential_v1'`).Scan(&existing)
	if err == nil {
		return false, errors.New("source administrator credential appeared after preflight; run preflight again")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("recheck source administrator credential: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`insert into settings (key, value, updated_at_ms) values ('admin_credential_v1', ?, ?)`,
		string(encoded), time.Now().UnixMilli(),
	); err != nil {
		return false, fmt.Errorf("retain current administrator credential in source: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit administrator credential retention: %w", err)
	}
	committed = true
	return true, nil
}

func (s *Service) resolveSQLiteAdoptionDataKey(
	req SQLiteAdoptionRequest,
	databasePath string,
) (key []byte, selectedPath string, missing bool, err error) {
	requestedPath := strings.TrimSpace(req.DataKeyPath)
	if s.cfg.DataKey != "" {
		if requestedPath != "" {
			return nil, "", false, newSQLiteAdoptionError(
				SQLiteAdoptionCodeDataKeyConflict,
				"data key configuration",
				databasePath,
				errors.New("a data key is supplied by environment or secret file; dataKeyPath cannot override it"),
			)
		}
		key, _, err := security.LoadOrCreateDataKey(s.cfg.DataKey, "")
		if err != nil {
			return nil, "", false, newSQLiteAdoptionError(SQLiteAdoptionCodeDataKeyInvalid, "data key parsing", databasePath, err)
		}
		return key, "", false, nil
	}
	if s.cfg.DataKeyPathEnvSet {
		if requestedPath != "" && !sameSQLitePath(requestedPath, s.cfg.DataKeyPath) {
			return nil, "", false, newSQLiteAdoptionError(
				SQLiteAdoptionCodeDataKeyConflict,
				"data key configuration",
				databasePath,
				fmt.Errorf("CPA_MANAGER_DATA_KEY_PATH fixes the data key at %q", s.cfg.DataKeyPath),
			)
		}
		requestedPath = s.cfg.DataKeyPath
	}
	if requestedPath == "" {
		autoPath := filepath.Join(filepath.Dir(databasePath), "data.key")
		if _, statErr := os.Stat(autoPath); statErr == nil {
			requestedPath = autoPath
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, "", false, newSQLiteAdoptionError(SQLiteAdoptionCodeDataKeyInvalid, "data key inspection", databasePath, statErr)
		} else {
			return nil, "", true, nil
		}
	}
	if !filepath.IsAbs(requestedPath) {
		return nil, "", false, newSQLiteAdoptionError(
			SQLiteAdoptionCodeDataKeyInvalid,
			"data key path validation",
			databasePath,
			errors.New("dataKeyPath must be an absolute path on the Manager Server host"),
		)
	}
	absolutePath, err := filepath.Abs(requestedPath)
	if err != nil {
		return nil, "", false, newSQLiteAdoptionError(SQLiteAdoptionCodeDataKeyInvalid, "data key path validation", databasePath, err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(absolutePath); resolveErr == nil {
		absolutePath = resolved
	} else if !errors.Is(resolveErr, os.ErrNotExist) {
		return nil, "", false, newSQLiteAdoptionError(SQLiteAdoptionCodeDataKeyInvalid, "data key path resolution", databasePath, resolveErr)
	}
	data, err := os.ReadFile(absolutePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, filepath.Clean(absolutePath), true, nil
		}
		return nil, "", false, newSQLiteAdoptionError(SQLiteAdoptionCodeDataKeyInvalid, "data key read", databasePath, err)
	}
	key, _, err = security.LoadOrCreateDataKey(strings.TrimSpace(string(data)), "")
	if err != nil {
		return nil, "", false, newSQLiteAdoptionError(SQLiteAdoptionCodeDataKeyInvalid, "data key parsing", databasePath, err)
	}
	return key, filepath.Clean(absolutePath), false, nil
}

type sqliteAdoptionInspection struct {
	settings          map[string]string
	adminCredential   *model.AdminCredential
	hasHistoricalData bool
}

func inspectSQLiteAdoptionDatabase(ctx context.Context, db *sql.DB) (sqliteAdoptionInspection, error) {
	var quickCheck string
	if err := db.QueryRowContext(ctx, `pragma quick_check(1)`).Scan(&quickCheck); err != nil {
		return sqliteAdoptionInspection{}, fmt.Errorf("run SQLite quick_check: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(quickCheck), "ok") {
		return sqliteAdoptionInspection{}, fmt.Errorf("SQLite quick_check reported: %s", quickCheck)
	}
	tables := map[string]bool{}
	rows, err := db.QueryContext(ctx, `select name from sqlite_schema where type = 'table' and name in ('settings', 'usage_events')`)
	if err != nil {
		return sqliteAdoptionInspection{}, fmt.Errorf("inspect CPA Manager tables: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return sqliteAdoptionInspection{}, err
		}
		tables[name] = true
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return sqliteAdoptionInspection{}, err
	}
	if !tables["settings"] && !tables["usage_events"] {
		return sqliteAdoptionInspection{}, errors.New("database does not contain CPA Manager settings or usage_events tables")
	}

	inspection := sqliteAdoptionInspection{settings: map[string]string{}}
	if tables["usage_events"] {
		var exists int
		if err := db.QueryRowContext(ctx, `select exists(select 1 from usage_events limit 1)`).Scan(&exists); err != nil {
			return sqliteAdoptionInspection{}, fmt.Errorf("inspect historical usage events: %w", err)
		}
		inspection.hasHistoricalData = exists == 1
	}
	if !tables["settings"] {
		return inspection, nil
	}
	settingsRows, err := db.QueryContext(ctx, `select key, value from settings where key in ('admin_credential_v1', 'bootstrap_state_v1', 'setup', 'manager_config_v1')`)
	if err != nil {
		return sqliteAdoptionInspection{}, fmt.Errorf("read source settings: %w", err)
	}
	defer settingsRows.Close()
	for settingsRows.Next() {
		var key, value string
		if err := settingsRows.Scan(&key, &value); err != nil {
			return sqliteAdoptionInspection{}, err
		}
		inspection.settings[key] = value
	}
	if err := settingsRows.Err(); err != nil {
		return sqliteAdoptionInspection{}, err
	}
	if raw := inspection.settings["admin_credential_v1"]; raw != "" {
		var credential model.AdminCredential
		if err := json.Unmarshal([]byte(raw), &credential); err != nil {
			return sqliteAdoptionInspection{}, fmt.Errorf("decode source administrator credential: %w", err)
		}
		if credential.Salt == "" || credential.KeyHash == "" {
			return sqliteAdoptionInspection{}, errors.New("source administrator credential is incomplete")
		}
		inspection.adminCredential = &credential
	}
	if raw := inspection.settings["bootstrap_state_v1"]; raw != "" {
		var state model.BootstrapState
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return sqliteAdoptionInspection{}, fmt.Errorf("decode source bootstrap state: %w", err)
		}
		inspection.hasHistoricalData = inspection.hasHistoricalData || state.HasHistoricalData
	}
	return inspection, nil
}

func verifySQLiteAdoptionEncryptedValues(settings map[string]string, protector *security.Protector) error {
	for key, value := range sqliteAdoptionManagementKeys(settings) {
		if !security.IsValidProtectedEnvelope(value) {
			continue
		}
		if _, err := protector.UnprotectString(value); err != nil {
			return fmt.Errorf("decrypt %s managementKey: %w", key, err)
		}
	}
	return nil
}

func sqliteAdoptionProjectInitialized(settings map[string]string, protector *security.Protector) (bool, error) {
	for _, connection := range sqliteAdoptionConnections(settings) {
		managementKey := strings.TrimSpace(connection.managementKey)
		if security.IsValidProtectedEnvelope(managementKey) {
			if protector == nil {
				continue
			}
			decrypted, err := protector.UnprotectString(managementKey)
			if err != nil {
				return false, err
			}
			managementKey = decrypted
		}
		if strings.TrimSpace(connection.baseURL) != "" && strings.TrimSpace(managementKey) != "" {
			return true, nil
		}
	}
	if raw := settings["bootstrap_state_v1"]; raw != "" {
		var state model.BootstrapState
		if err := json.Unmarshal([]byte(raw), &state); err == nil && state.ProjectInitialized {
			return true, nil
		}
	}
	return false, nil
}

type sqliteAdoptionConnection struct {
	baseURL       string
	managementKey string
}

func sqliteAdoptionConnections(settings map[string]string) []sqliteAdoptionConnection {
	connections := make([]sqliteAdoptionConnection, 0, 2)
	if raw := settings["setup"]; raw != "" {
		var value struct {
			CPAUpstreamURL string `json:"cpaBaseUrl"`
			ManagementKey  string `json:"managementKey"`
		}
		if json.Unmarshal([]byte(raw), &value) == nil {
			connections = append(connections, sqliteAdoptionConnection{value.CPAUpstreamURL, value.ManagementKey})
		}
	}
	if raw := settings["manager_config_v1"]; raw != "" {
		var value struct {
			CPAConnection struct {
				CPABaseURL    string `json:"cpaBaseUrl"`
				ManagementKey string `json:"managementKey"`
			} `json:"cpaConnection"`
		}
		if json.Unmarshal([]byte(raw), &value) == nil {
			connections = append(connections, sqliteAdoptionConnection{
				value.CPAConnection.CPABaseURL, value.CPAConnection.ManagementKey,
			})
		}
	}
	return connections
}

func sqliteAdoptionManagementKeys(settings map[string]string) map[string]string {
	keys := map[string]string{}
	connections := sqliteAdoptionConnections(settings)
	if len(connections) > 0 {
		keys["setup"] = connections[0].managementKey
	}
	if len(connections) > 1 {
		keys["manager_config_v1"] = connections[1].managementKey
	}
	return keys
}

func openSQLiteAdoptionDatabase(path, mode string, busyTimeoutMS int) (*sql.DB, error) {
	uriPath := filepath.ToSlash(path)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := &url.URL{Scheme: "file", Path: uriPath}
	query := dsn.Query()
	query.Set("mode", mode)
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS))
	if mode == "rw" {
		query.Set("_txlock", "immediate")
	}
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func probeSQLiteAdoptionWriteLock(ctx context.Context, path string) error {
	db, err := openSQLiteAdoptionDatabase(path, "rw", 250)
	if err != nil {
		return err
	}
	defer db.Close()
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := db.BeginTx(probeCtx, nil)
	if err != nil {
		return err
	}
	return tx.Rollback()
}

func newSQLiteAdoptionError(code, stage, sourcePath string, cause error) *SQLiteAdoptionError {
	return &SQLiteAdoptionError{Code: code, Stage: stage, SourcePath: sourcePath, Cause: cause}
}

func SQLiteAdoptionErrorCode(err error) string {
	var adoptionErr *SQLiteAdoptionError
	if errors.As(err, &adoptionErr) && adoptionErr.Code != "" {
		return adoptionErr.Code
	}
	return SQLiteAdoptionCodeFailed
}

func sameSQLitePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(strings.TrimSpace(left))
	rightAbs, rightErr := filepath.Abs(strings.TrimSpace(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftAbs = filepath.Clean(leftAbs)
	rightAbs = filepath.Clean(rightAbs)
	leftInfo, leftStatErr := os.Stat(leftAbs)
	rightInfo, rightStatErr := os.Stat(rightAbs)
	if leftStatErr == nil && rightStatErr == nil && os.SameFile(leftInfo, rightInfo) {
		return true
	}
	if resolved, err := filepath.EvalSymlinks(leftAbs); err == nil {
		leftAbs = filepath.Clean(resolved)
	}
	if resolved, err := filepath.EvalSymlinks(rightAbs); err == nil {
		rightAbs = filepath.Clean(resolved)
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(leftAbs, rightAbs)
	}
	return leftAbs == rightAbs
}

func sameOptionalSQLitePath(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return strings.TrimSpace(left) == "" && strings.TrimSpace(right) == ""
	}
	return sameSQLitePath(left, right)
}

func existingFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	return info.Size()
}

func sqliteLockError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked") ||
		strings.Contains(message, "sqlite_busy") || strings.Contains(message, "sqlite_locked")
}
