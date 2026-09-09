package setup

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	managerconfigservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestPreflightSQLiteAdoptionRequiresAndVerifiesSourceParameters(t *testing.T) {
	service, _, cfg := newSQLiteAdoptionTestService(t)
	sourcePath, dataKeyPath := newEncryptedSQLiteAdoptionSource(t, "source-admin")

	missing, err := service.PreflightSQLiteAdoption(context.Background(), SQLiteAdoptionRequest{
		SourcePath: sourcePath,
	})
	if err != nil {
		t.Fatalf("PreflightSQLiteAdoption(missing) error = %v", err)
	}
	if missing.Ready || !missing.DataKeyVerified || !sameSQLitePath(missing.DataKeyPath, dataKeyPath) {
		t.Fatalf("missing preflight = %#v", missing)
	}
	if !slices.Equal(missing.RequiredParameters, []string{"sourceAdminKey"}) {
		t.Fatalf("required parameters = %#v", missing.RequiredParameters)
	}

	ready, err := service.PreflightSQLiteAdoption(context.Background(), SQLiteAdoptionRequest{
		SourcePath:     sourcePath,
		DataKeyPath:    dataKeyPath,
		SourceAdminKey: "source-admin",
	})
	if err != nil {
		t.Fatalf("PreflightSQLiteAdoption(ready) error = %v", err)
	}
	if !ready.Ready || !ready.AdminKeyVerified || !ready.DataKeyVerified ||
		!ready.ProjectInitialized || !ready.LockRisk || !ready.RestartRequired {
		t.Fatalf("ready preflight = %#v", ready)
	}
	if sameSQLitePath(cfg.DBPath, ready.SourcePath) {
		t.Fatal("preflight returned current database instead of source")
	}
}

func TestPreflightSQLiteAdoptionReportsWrongDataKeyAndHeldProcessLock(t *testing.T) {
	service, _, _ := newSQLiteAdoptionTestService(t)
	sourcePath, _ := newEncryptedSQLiteAdoptionSource(t, "source-admin")
	wrongKeyPath := filepath.Join(t.TempDir(), "data.key")
	if _, _, err := security.LoadOrCreateDataKey("", wrongKeyPath); err != nil {
		t.Fatal(err)
	}

	_, err := service.PreflightSQLiteAdoption(context.Background(), SQLiteAdoptionRequest{
		SourcePath: sourcePath, DataKeyPath: wrongKeyPath, SourceAdminKey: "source-admin",
	})
	if SQLiteAdoptionErrorCode(err) != SQLiteAdoptionCodeDataKeyInvalid {
		t.Fatalf("wrong data key error = %v code=%q", err, SQLiteAdoptionErrorCode(err))
	}

	held, err := processlock.Acquire(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	_, err = service.PreflightSQLiteAdoption(context.Background(), SQLiteAdoptionRequest{
		SourcePath: sourcePath, SourceAdminKey: "source-admin",
	})
	if SQLiteAdoptionErrorCode(err) != SQLiteAdoptionCodeLocked || SQLiteAdoptionErrorStatus(err) != 423 {
		t.Fatalf("held source error = %v code=%q status=%d", err, SQLiteAdoptionErrorCode(err), SQLiteAdoptionErrorStatus(err))
	}
}

func TestAdoptSQLitePersistsRestartSelectionOnlyAfterConfirmation(t *testing.T) {
	service, _, cfg := newSQLiteAdoptionTestService(t)
	sourcePath, dataKeyPath := newEncryptedSQLiteAdoptionSource(t, "source-admin")
	req := SQLiteAdoptionRequest{
		SourcePath: sourcePath, DataKeyPath: dataKeyPath, SourceAdminKey: "source-admin",
	}
	if _, err := service.AdoptSQLite(context.Background(), req); SQLiteAdoptionErrorCode(err) != SQLiteAdoptionCodeConfirmationRequired {
		t.Fatalf("unconfirmed adoption error = %v", err)
	}
	if _, ok, err := config.LoadSQLiteSourceSelection(cfg.DataDir); err != nil || ok {
		t.Fatalf("selection exists before confirmation: ok=%v err=%v", ok, err)
	}

	req.ConfirmSourceStopped = true
	result, err := service.AdoptSQLite(context.Background(), req)
	if err != nil {
		t.Fatalf("AdoptSQLite() error = %v", err)
	}
	if !result.OK || !result.RestartRequired || result.SelectionState != config.SQLiteSourceStatePending {
		t.Fatalf("adoption result = %#v", result)
	}
	selection, ok, err := config.LoadSQLiteSourceSelection(cfg.DataDir)
	if err != nil || !ok {
		t.Fatalf("load selection: ok=%v err=%v", ok, err)
	}
	if !sameSQLitePath(selection.DatabasePath, result.SourcePath) ||
		!sameSQLitePath(selection.DataKeyPath, dataKeyPath) {
		t.Fatalf("selection = %#v", selection)
	}
}

func TestPreflightSQLiteAdoptionRejectsCompletedSetupAndEnvironmentDBPath(t *testing.T) {
	service, current, cfg := newSQLiteAdoptionTestService(t)
	sourcePath, _ := newEncryptedSQLiteAdoptionSource(t, "source-admin")
	cfg.DBPathEnvSet = true
	service.cfg = cfg
	_, err := service.PreflightSQLiteAdoption(context.Background(), SQLiteAdoptionRequest{SourcePath: sourcePath})
	if SQLiteAdoptionErrorCode(err) != SQLiteAdoptionCodeEnvironmentManaged {
		t.Fatalf("environment-managed error = %v", err)
	}

	service.cfg.DBPathEnvSet = false
	state, ok, err := current.LoadBootstrapState(context.Background())
	if err != nil || !ok {
		t.Fatalf("load current bootstrap state: ok=%v err=%v", ok, err)
	}
	state.ProjectInitialized = true
	state.Status = "ready"
	if err := current.SaveBootstrapState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	_, err = service.PreflightSQLiteAdoption(context.Background(), SQLiteAdoptionRequest{SourcePath: sourcePath})
	if SQLiteAdoptionErrorCode(err) != SQLiteAdoptionCodeUnavailable {
		t.Fatalf("completed setup error = %v", err)
	}
}

func TestSwitchSQLiteSourceRetainsCurrentAdminAndPersistsRestartSelection(t *testing.T) {
	service, current, cfg := newSQLiteAdoptionTestService(t)
	if err := current.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: "http://current.example", ManagementKey: "current-management-key",
		Queue: "usage", PopSide: "right",
	}); err != nil {
		t.Fatal(err)
	}
	state, ok, err := current.LoadBootstrapState(context.Background())
	if err != nil || !ok {
		t.Fatalf("load bootstrap state: ok=%v err=%v", ok, err)
	}
	state.Status = "ready"
	state.ProjectInitialized = true
	if err := current.SaveBootstrapState(context.Background(), state); err != nil {
		t.Fatal(err)
	}

	sourcePath, dataKeyPath := newSQLiteAdoptionSourceWithoutAdmin(t)
	preflight, err := service.PreflightSQLiteSwitch(context.Background(), SQLiteAdoptionRequest{
		SourcePath: sourcePath, DataKeyPath: dataKeyPath,
	})
	if err != nil {
		t.Fatalf("PreflightSQLiteSwitch() error = %v", err)
	}
	if !preflight.Ready || !preflight.AdminKeyWillBeRetained || preflight.AdminKeyRequired {
		t.Fatalf("preflight = %#v", preflight)
	}

	request := SQLiteSourceSwitchRequest{
		SQLiteAdoptionRequest: SQLiteAdoptionRequest{
			SourcePath: sourcePath, DataKeyPath: dataKeyPath, ConfirmSourceStopped: true,
		},
		ExpectedGeneration: 1,
		IdempotencyKey:     "switch-source-1",
	}
	result, err := service.SwitchSQLiteSource(context.Background(), request)
	if err != nil {
		t.Fatalf("SwitchSQLiteSource() error = %v", err)
	}
	if !result.OK || !result.RestartRequired || !result.AdminKeyRetained {
		t.Fatalf("switch result = %#v", result)
	}
	selection, ok, err := config.LoadSQLiteSourceSelection(cfg.DataDir)
	if err != nil || !ok {
		t.Fatalf("load selection: ok=%v err=%v", ok, err)
	}
	if selection.Operation != config.SQLiteSourceOperationSwitch ||
		selection.IdempotencyKey != request.IdempotencyKey ||
		selection.ControlBackupSuffix == "" ||
		!sameSQLitePath(selection.PreviousDatabasePath, cfg.DBPath) {
		t.Fatalf("selection = %#v", selection)
	}
	status, err := service.SQLiteSourceStatus()
	if err != nil || !status.RestartRequired || !sameSQLitePath(status.PendingPath, sourcePath) {
		t.Fatalf("source status = %#v err=%v", status, err)
	}

	key, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Open(sourcePath, protector)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	credential, found, err := source.LoadAdminCredential(context.Background())
	if err != nil || !found || !security.VerifyAdminKey(credential, "current-admin") {
		t.Fatalf("retained credential: found=%v err=%v value=%#v", found, err, credential)
	}

	// A lost HTTP response can be retried without supplying a newly-created
	// sourceAdminKey: the idempotency record is resolved before a second preflight.
	replayed, err := service.SwitchSQLiteSource(context.Background(), request)
	if err != nil || replayed.SelectionState != config.SQLiteSourceStatePending {
		t.Fatalf("idempotent replay = %#v err=%v", replayed, err)
	}
}

func TestSQLiteSourceStatusReportsRecoveredSwitchFailure(t *testing.T) {
	service, _, cfg := newSQLiteAdoptionTestService(t)
	failedPath := filepath.Join(t.TempDir(), "failed.sqlite")
	if err := config.SaveSQLiteSourceSelection(cfg.DataDir, config.SQLiteSourceSelection{
		State:              config.SQLiteSourceStateActive,
		Operation:          config.SQLiteSourceOperationSwitch,
		DatabasePath:       cfg.DBPath,
		DataKeyPath:        cfg.DataKeyPath,
		FailedDatabasePath: failedPath,
		LastErrorStage:     "load data key",
		LastErrorCause:     "invalid key length",
		LastError:          "SQLite source switch failed and the previous source was restored",
		FailedAtMS:         123456,
	}); err != nil {
		t.Fatal(err)
	}

	status, err := service.SQLiteSourceStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.RestartRequired || status.FailedPath != failedPath ||
		status.LastErrorStage != "load data key" || status.LastErrorCause != "invalid key length" ||
		status.LastError == "" || status.FailedAtMS != 123456 {
		t.Fatalf("source status = %#v", status)
	}
}

func newSQLiteAdoptionTestService(t *testing.T) (*Service, *store.Store, config.Config) {
	t.Helper()
	dataDir := t.TempDir()
	key, _, err := security.LoadOrCreateDataKey("", filepath.Join(dataDir, "data.key"))
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		DataDir:     dataDir,
		DBPath:      filepath.Join(dataDir, "usage.sqlite"),
		DataKeyPath: filepath.Join(dataDir, "data.key"),
		Queue:       "usage", PopSide: "right", BatchSize: 100,
	}
	current, err := store.Open(cfg.DBPath, protector)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = current.Close() })
	credential, err := security.NewAdminCredential("current-admin", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := current.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	if err := current.SaveBootstrapState(context.Background(), store.BootstrapState{
		Version: 1, Status: "needs_setup", AdminReady: true, DataKeyReady: true,
	}); err != nil {
		t.Fatal(err)
	}
	manager := collector.NewManager(cfg, current)
	collectorService := collectorservice.New(manager)
	managerConfigService := managerconfigservice.New(cfg, current, collectorService)
	return New(cfg, current, collectorService, managerConfigService, 1, "test"), current, cfg
}

func newEncryptedSQLiteAdoptionSource(t *testing.T, adminKey string) (string, string) {
	t.Helper()
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "usage.sqlite")
	dataKeyPath := filepath.Join(directory, "data.key")
	key, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Open(sourcePath, protector)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	if err := source.SaveAdminCredential(context.Background(), credential); err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	if err := source.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: "http://legacy.example", ManagementKey: "legacy-management-key",
		Queue: "usage", PopSide: "right",
	}); err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	return sourcePath, dataKeyPath
}

func newSQLiteAdoptionSourceWithoutAdmin(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "usage.sqlite")
	dataKeyPath := filepath.Join(directory, "data.key")
	key, _, err := security.LoadOrCreateDataKey("", dataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Open(sourcePath, protector)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: "http://legacy-without-admin.example",
		ManagementKey:  "legacy-management-key",
		Queue:          "usage",
		PopSide:        "right",
	}); err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	return sourcePath, dataKeyPath
}
