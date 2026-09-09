package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/buildinfo"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/adminreset"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/cpaconnection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/derivedmaintenance"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/managerdatasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/runtimeconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	adminauthservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	bootstrapservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrap"
	collectorservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	databasemanagementservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/databasemanagement"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/worker"
)

func main() {
	if handled, err := writeVersion(os.Args[1:], os.Stdout); handled {
		if err != nil {
			log.Printf("write version: %v", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reset-admin-key", "reset-admin-password":
			if err := adminreset.Run(context.Background(), os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("reset admin key: %v", err)
				os.Exit(1)
			}
			return
		case "cleanup-derived":
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := derivedmaintenance.Run(ctx, os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("cleanup derived data: %v", err)
				os.Exit(1)
			}
			return
		case "store-cpa-connection":
			if err := cpaconnection.Run(context.Background(), os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("store CPA connection: %v", err)
				os.Exit(1)
			}
			return
		case "manager-data-snapshot":
			if err := runManagerDataSnapshotCommand(os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("manage Manager data snapshot: %v", err)
				os.Exit(1)
			}
			return
		case "sanitize-runtime-config":
			if err := runtimeconfig.Run(os.Args[2:], os.Stdout, os.Stderr); err != nil {
				log.Printf("sanitize runtime config: %v", err)
				os.Exit(1)
			}
			return
		}
	}
	runServer()
}

func writeVersion(args []string, stdout io.Writer) (bool, error) {
	if len(args) != 1 || (args[0] != "-v" && args[0] != "--version") {
		return false, nil
	}
	_, err := fmt.Fprintln(stdout, buildinfo.Version)
	return true, err
}

func runManagerDataSnapshotCommand(args []string, stdout io.Writer, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return managerdatasnapshot.Run(ctx, args, stdout, stderr)
}

func runServer() {
	for {
		restartRequested, dataDir, err := runServerIteration()
		if err != nil {
			stage, cause := serverIterationErrorDetails(err)
			if dataDir != "" {
				restored, rolledBack, rollbackErr := config.RollbackSQLiteSourceSwitch(
					dataDir,
					config.SQLiteSourceSwitchFailure{Stage: stage, Cause: cause},
				)
				if rollbackErr != nil {
					log.Fatalf("start manager server: %v; rollback pending SQLite source switch: %v", err, rollbackErr)
				}
				if rolledBack {
					log.Printf(
						"SQLite source switch to %s failed during %s: %s; restored previous source %s",
						restored.FailedDatabasePath,
						restored.LastErrorStage,
						restored.LastErrorCause,
						restored.DatabasePath,
					)
					continue
				}
			}
			log.Fatalf("start manager server: %v", err)
		}
		if !restartRequested {
			return
		}
		log.Printf("restarting cpa-manager-plus with refreshed configuration")
	}
}

type serverIterationError struct {
	stage string
	cause error
}

func (e *serverIterationError) Error() string {
	if e == nil {
		return "manager server startup failed"
	}
	return fmt.Sprintf("%s: %v", e.stage, e.cause)
}

func (e *serverIterationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func failServerIteration(stage string, cause error) error {
	return &serverIterationError{stage: stage, cause: cause}
}

func serverIterationErrorDetails(err error) (string, string) {
	var iterationErr *serverIterationError
	if errors.As(err, &iterationErr) {
		return iterationErr.stage, iterationErr.cause.Error()
	}
	return "startup", err.Error()
}

func runServerIteration() (bool, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return false, "", failServerIteration("load configuration", err)
	}
	dataDir := strings.TrimSpace(cfg.DataDir)
	if dataDir == "" {
		dataDir = filepath.Dir(cfg.DBPath)
	}
	dataDirectoryLock, err := processlock.AcquireDataDirectory(dataDir)
	if err != nil {
		return false, dataDir, failServerIteration("acquire data directory process lock", err)
	}
	defer func() {
		if err := dataDirectoryLock.Close(); err != nil {
			log.Printf("close manager data directory process lock: %v", err)
		}
	}()
	databaseLock, err := processlock.Acquire(cfg.DBPath)
	if err != nil {
		return false, dataDir, failServerIteration("acquire SQLite process lock", err)
	}
	defer func() {
		if err := databaseLock.Close(); err != nil {
			log.Printf("close manager database process lock: %v", err)
		}
	}()
	cfg.DBPath = databaseLock.DatabasePath()
	if err := validateSelectedSQLiteSourceFiles(cfg, dataDir); err != nil {
		return false, dataDir, failServerIteration("verify selected SQLite source files", err)
	}
	if err := sqliterepo.RequireExistingDataKeyForEncryptedCPAConnection(
		context.Background(),
		cfg.DBPath,
		cfg.DataKey,
		cfg.DataKeyPath,
	); err != nil {
		return false, dataDir, failServerIteration("validate data key availability", err)
	}
	dataKey, dataKeyCreated, err := security.LoadOrCreateDataKey(cfg.DataKey, cfg.DataKeyPath)
	if err != nil {
		return false, dataDir, failServerIteration("load data key", err)
	}
	protector, err := security.NewProtector(dataKey)
	if err != nil {
		return false, dataDir, failServerIteration("initialize secret protector", err)
	}
	if cfg.SQLiteSourceState == config.SQLiteSourceStatePending {
		selection, prepareErr := config.PrepareSQLiteSourceControl(dataDir)
		if prepareErr != nil {
			return false, dataDir, failServerIteration("prepare SQLite control state", prepareErr)
		}
		cfg.SQLiteSourceState = selection.State
	}
	controlStore, err := control.NewStore(filepath.Join(dataDir, "database-control.json.enc"), protector)
	if err != nil {
		return false, dataDir, failServerIteration("initialize encrypted database control", err)
	}
	controlState, controlLoadErr := controlStore.Load()
	if controlLoadErr != nil && !errors.Is(controlLoadErr, control.ErrNotFound) {
		return false, dataDir, failServerIteration("load encrypted database control", controlLoadErr)
	}
	if errors.Is(controlLoadErr, control.ErrNotFound) {
		controlState = control.DefaultState()
	}
	if err := databasemanagementservice.RecoverInterruptedSQLiteCacheReplace(cfg.DBPath); err != nil {
		return false, dataDir, failServerIteration("recover interrupted SQLite cache replacement", err)
	}
	db, err := store.Open(cfg.DBPath, protector)
	if err != nil {
		log.Printf("open sqlite failed; attempting restricted database recovery mode: %v", err)
		if recoveryErr := runDatabaseRecoveryServer(cfg, controlStore, controlState, dataDir); recoveryErr != nil {
			return false, dataDir, failServerIteration(
				"open SQLite database",
				errors.Join(err, fmt.Errorf("restricted database recovery mode: %w", recoveryErr)),
			)
		}
		return false, dataDir, nil
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("close sqlite: %v", err)
		}
	}()
	if err := databasemanagementservice.CleanupRecoveredSQLiteCacheBackup(cfg.DBPath); err != nil {
		log.Printf("cleanup recovered sqlite cache backup: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	restart := newRestartCoordinator()
	walSlot := &walMaintenanceSlot{}

	var databaseRuntime *databasemanagementservice.Runtime
	databaseRuntime, err = databasemanagementservice.NewRuntime(databasemanagementservice.RuntimeOptions{
		Control: controlStore, SQLite: db.Backend(), SQLitePath: cfg.DBPath, DataDir: dataDir,
		EnableOutbox: databasemanagementservice.SQLiteOutboxEnabler(db.Backend().DB()),
		AcquireWriteFence: func(ctx context.Context) (func(), error) {
			return databaseRuntime.AcquireApplicationWriteFence(ctx)
		},
		ReplaceSQLiteCache: func(replaceCtx context.Context, temporaryPath string, generation uint64, watermark int64) (database.Backend, error) {
			if err := walSlot.CloseCurrent(); err != nil {
				return nil, fmt.Errorf("close SQLite WAL maintenance before cache replacement: %w", err)
			}
			next, replaceErr := databaseRuntime.ReplaceSQLiteCacheFile(
				replaceCtx, temporaryPath, generation, watermark,
			)
			maintenance, maintenanceErr := sqliterepo.NewWALMaintenance(cfg.DBPath)
			if maintenanceErr == nil {
				maintenance.Start(ctx)
				walSlot.Store(maintenance)
			} else {
				log.Printf("restart SQLite WAL maintenance after cache replacement: %v", maintenanceErr)
			}
			return next, replaceErr
		},
		ValidateBackend: func(_ context.Context, backend database.Backend) error {
			_, validateErr := store.NewWithBackendChecked(backend, protector)
			return validateErr
		},
		ReadCutoverOK:        func() bool { return true },
		DerivedConformanceOK: func() bool { return true },
		WriteSwitchOK: func(target database.BackendKind) bool {
			return target == database.BackendSQLite || target == database.BackendMySQL
		},
	})
	if err != nil {
		return false, dataDir, failServerIteration("initialize database routing runtime", err)
	}
	defer func() {
		if err := databaseRuntime.Close(); err != nil {
			log.Printf("close database runtime: %v", err)
		}
	}()
	mysqlConnectCtx, cancelMySQLConnect := context.WithTimeout(context.Background(), 8*time.Second)
	if err := databaseRuntime.ConnectConfiguredMySQL(mysqlConnectCtx); err != nil {
		log.Printf("configured MySQL is unavailable; continuing with SQLite and queued replication: %v", err)
	}
	cancelMySQLConnect()
	initializeDatabaseCtx, cancelInitializeDatabase := context.WithTimeout(context.Background(), 30*time.Second)
	if err := databaseRuntime.Initialize(initializeDatabaseCtx); err != nil {
		cancelInitializeDatabase()
		return false, dataDir, failServerIteration("restore database routing invariants", err)
	}
	cancelInitializeDatabase()
	applicationStore, err := store.NewRoutedApplicationStore(context.Background(), databaseRuntime, protector)
	if err != nil {
		return false, dataDir, failServerIteration("initialize routed application store", err)
	}
	if controlState.MySQL.Host != "" {
		prepareMySQLCtx, cancelPrepareMySQL := context.WithTimeout(context.Background(), 30*time.Second)
		if err := applicationStore.PrepareBackend(prepareMySQLCtx, database.BackendMySQL); err != nil {
			log.Printf("prepare MySQL application repositories: %v", err)
		}
		cancelPrepareMySQL()
	}

	bootstrapResult, err := bootstrapservice.Run(context.Background(), cfg, applicationStore, dataKeyCreated)
	if err != nil {
		return false, dataDir, failServerIteration("bootstrap manager server", err)
	}
	if bootstrapResult.GeneratedAdminKey != "" {
		log.Printf("CPA Manager Plus admin key generated: %s", bootstrapResult.GeneratedAdminKey)
	} else {
		log.Printf("CPA Manager Plus admin credential initialized")
	}
	if bootstrapResult.DataKeyCreated {
		log.Printf("CPA Manager Plus data key created at %s", cfg.DataKeyPath)
	}
	if bootstrapResult.MigratedLegacy {
		log.Printf("CPA Manager Plus legacy data migrated")
	}
	if err := syncDatabaseControlAdminAuth(context.Background(), controlStore, applicationStore); err != nil {
		return false, dataDir, failServerIteration("synchronize database control authentication copy", err)
	}

	manager := collector.NewManager(cfg, applicationStore)
	collectorService := collectorservice.New(manager)
	collectorWorker := worker.NewCollectorWorker(cfg, applicationStore, collectorService)
	databaseRuntime.Start(ctx)
	walMaintenance, err := sqliterepo.NewWALMaintenance(cfg.DBPath)
	if err != nil {
		log.Printf("configure SQLite WAL maintenance: %v", err)
	} else {
		walMaintenance.Start(ctx)
		walSlot.Store(walMaintenance)
	}
	defer func() {
		if err := walSlot.CloseCurrent(); err != nil {
			log.Printf("close SQLite WAL maintenance: %v", err)
		}
	}()

	serverApp := httpapi.New(cfg, applicationStore, manager)
	serverApp.AppContext().DatabaseMaintenance = walSlot
	serverApp.AppContext().DatabaseManagement = databaseRuntime
	serverApp.AppContext().SQLiteSourceSwitchGuard = databaseRuntime
	serverApp.AppContext().RestartRequester = restart
	recoveryCtx, cancelRecovery := context.WithTimeout(context.Background(), 10*time.Second)
	if err := serverApp.AppContext().CodexInspectionService.Recover(recoveryCtx); err != nil {
		log.Printf("recover codex inspection runs: %v", err)
	}
	cancelRecovery()
	if err := serverApp.AppContext().UsageService.StartImportSessionCleanup(ctx); err != nil {
		return false, dataDir, failServerIteration("start usage import session cleanup", err)
	}
	automationSettingsService := serverApp.AppContext().AccountProcessingPolicyService
	runtimeSettings := automationSettingsService.RuntimeSettings(ctx)
	rateLimitAutoDisableWorker := worker.NewRateLimitAutoDisableWorkerWithMutationCoordinator(
		applicationStore,
		serverApp.AppContext().AuthFileMutationCoordinator,
		collector.RuntimeConfig{
			CPAUpstreamURL: cfg.CPAUpstreamURL,
			ManagementKey:  cfg.ManagementKey,
		},
	)
	accountActionWorker := worker.NewAccountActionCandidateWorkerWithMutationCoordinator(
		applicationStore,
		serverApp.AppContext().AuthFileMutationCoordinator,
		runtimeSettings.AccountActionsAutoDisable,
	)
	accountHistoryRollupWorker := worker.NewAccountHistoryRollupWorker(applicationStore)
	usageDerivedRollupWorker := worker.NewUsagePricingRollupWorker(applicationStore)
	serverApp.AppContext().ModelPriceService.SetPricesChangedNotifier(usageDerivedRollupWorker.Wake)
	var usageHourlyAggregateWorker *worker.UsageHourlyAggregateWorker
	if cfg.DashboardHourlyRollupEnabled {
		usageHourlyAggregateWorker = worker.NewUsageHourlyAggregateWorker(applicationStore)
	}
	serverApp.AppContext().UsageService.SetEventsInsertedNotifier(func() {
		accountHistoryRollupWorker.Wake()
		usageDerivedRollupWorker.Wake()
		if usageHourlyAggregateWorker != nil {
			usageHourlyAggregateWorker.Wake()
		}
	})
	automationRuntime := worker.NewAutomationRuntime(
		automationSettingsService,
		manager,
		rateLimitAutoDisableWorker,
		accountActionWorker,
	)
	serverApp.AppContext().AutomationRuntimeService = automationRuntime
	manager.SetUsageEventHandler(worker.NewUsageEventFanout(
		automationRuntime.UsageEventHandler(),
		accountHistoryRollupWorker,
		usageDerivedRollupWorker,
		usageHourlyAggregateWorker,
	))

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           serverApp.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	pprofServer, err := newPprofServer(cfg.PprofAddr)
	if err != nil {
		return false, dataDir, failServerIteration("configure pprof", err)
	}

	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return false, dataDir, failServerIteration("bind HTTP listener", err)
	}
	if cfg.SQLiteSourceState == config.SQLiteSourceStatePrepared {
		selection, activateErr := config.ActivateSQLiteSourceSelection(dataDir)
		if activateErr != nil {
			_ = listener.Close()
			return false, dataDir, failServerIteration("activate SQLite source", activateErr)
		}
		cfg.SQLiteSourceState = selection.State
		log.Printf("adopted SQLite source is active at %s", cfg.DBPath)
	}
	if pprofServer != nil {
		go func() {
			log.Printf("cpa-manager-plus pprof listening on %s", pprofServer.Addr)
			if err := pprofServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("pprof server: %v", err)
			}
		}()
	}
	log.Printf("cpa-manager-plus listening on %s", listener.Addr())
	codexInspectionWorker := worker.NewCodexInspectionWorker(serverApp.AppContext().Store, serverApp.AppContext().CodexInspectionService)
	serverResult := make(chan error, 1)
	go serveHTTPServer(server, listener, stop, serverResult)
	go serverApp.AppContext().UpdateCheckService.Run(ctx)

	if err := applicationStore.RunDerivedStartupMaintenance(ctx); err != nil && ctx.Err() == nil {
		log.Printf("[startup] post-listen index preparation failed; continuing without blocking background workers: %v", err)
	}
	if ctx.Err() == nil {
		log.Printf("[startup] starting background workers")
		automationRuntime.Start(ctx)
		codexInspectionWorker.Start(ctx)
		accountHistoryRollupWorker.Start(ctx)
		usageDerivedRollupWorker.Start(ctx)
		if usageHourlyAggregateWorker != nil {
			usageHourlyAggregateWorker.Start(ctx)
		}
		applicationStore.StartDerivedMaintenance(ctx)
		collectorWorker.Start(ctx)
		worker.NewLegacyQuotaSnapshotMigrationWorker(applicationStore).Start(ctx)
	}

	usageCacheAccountingMigrationWorker := worker.NewUsageCacheAccountingMigrationWorker(applicationStore, func() {
		accountHistoryRollupWorker.Wake()
		usageDerivedRollupWorker.Wake()
		if usageHourlyAggregateWorker != nil {
			usageHourlyAggregateWorker.Wake()
		}
		go runUsageResponseMetadataBackfill(ctx, applicationStore)
	})
	if ctx.Err() == nil {
		usageCacheAccountingMigrationWorker.Start(ctx)
	}

	restartRequested := false
	select {
	case <-restart.Requested():
		restartRequested = true
		stop()
	case <-ctx.Done():
		select {
		case err := <-serverResult:
			if err != nil {
				log.Printf("http server stopped unexpectedly: %v", err)
			}
		default:
		}
	case err := <-serverResult:
		if err != nil {
			log.Printf("http server stopped unexpectedly: %v", err)
		}
		// Ensure every worker using the shared process context observes the same
		// shutdown even when the HTTP listener, rather than a signal, initiated it.
		stop()
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
		if closeErr := server.Close(); closeErr != nil {
			log.Printf("force close HTTP server: %v", closeErr)
		}
	}
	cancelShutdown()
	if pprofServer != nil {
		pprofShutdownCtx, cancelPprofShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		if err := pprofServer.Shutdown(pprofShutdownCtx); err != nil {
			log.Printf("shutdown pprof: %v", err)
			if closeErr := pprofServer.Close(); closeErr != nil {
				log.Printf("force close pprof server: %v", closeErr)
			}
		}
		cancelPprofShutdown()
	}
	stopCodexInspectionWorker(codexInspectionWorker, 20*time.Second)
	collectorWorker.Stop(context.Background())
	return restartRequested, dataDir, nil
}

func validateSelectedSQLiteSourceFiles(cfg config.Config, dataDir string) error {
	if cfg.SQLiteSourceState != config.SQLiteSourceStatePending &&
		cfg.SQLiteSourceState != config.SQLiteSourceStatePrepared {
		return nil
	}
	selection, ok, err := config.LoadSQLiteSourceSelection(dataDir)
	if err != nil {
		return fmt.Errorf("load selected SQLite source: %w", err)
	}
	if !ok {
		return errors.New("selected SQLite source metadata is missing")
	}
	if err := requireExistingRegularFile(cfg.DBPath, "selected SQLite database"); err != nil {
		return err
	}
	if selection.DataKeyPath != "" {
		if err := requireExistingRegularFile(selection.DataKeyPath, "selected SQLite data key"); err != nil {
			return err
		}
	}
	return nil
}

func requireExistingRegularFile(path, description string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s %q: %w", description, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s %q is not a regular file", description, path)
	}
	if info.Size() == 0 {
		return fmt.Errorf("%s %q is empty", description, path)
	}
	return nil
}

func runDatabaseRecoveryServer(
	cfg config.Config,
	controlStore *control.Store,
	state control.State,
	dataDir string,
) error {
	if state.MySQL.Host == "" {
		return errors.New("sqlite is unavailable and mysql is not configured")
	}
	authService, err := adminauthservice.NewControlService(state.AdminAuthCopy)
	if err != nil {
		return err
	}
	runtime, err := databasemanagementservice.NewRuntime(databasemanagementservice.RuntimeOptions{
		Control: controlStore, SQLitePath: cfg.DBPath, DataDir: dataDir,
	})
	if err != nil {
		return err
	}
	defer runtime.Close()
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 8*time.Second)
	err = runtime.ConnectConfiguredMySQL(connectCtx)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("connect configured mysql: %w", err)
	}
	initializeCtx, cancelInitialize := context.WithTimeout(context.Background(), 30*time.Second)
	err = runtime.Initialize(initializeCtx)
	cancelInitialize()
	if err != nil {
		return fmt.Errorf("restore database routing invariants: %w", err)
	}
	serverApp := httpapi.NewDatabaseRecovery(cfg, authService, runtime)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: serverApp.Handler(), ReadHeaderTimeout: 10 * time.Second}
	result := make(chan error, 1)
	go serveHTTPServer(server, listener, stop, result)
	log.Printf("cpa-manager-plus database recovery mode listening on %s", listener.Addr())
	select {
	case <-ctx.Done():
	case err := <-result:
		if err != nil {
			return err
		}
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	return server.Shutdown(shutdownCtx)
}

func syncDatabaseControlAdminAuth(ctx context.Context, controlStore *control.Store, st *store.Store) error {
	credential, ok, err := st.LoadAdminCredential(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("admin credential is not initialized")
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		return err
	}
	state, err := controlStore.Load()
	if errors.Is(err, control.ErrNotFound) {
		state = control.DefaultState()
	} else if err != nil {
		return err
	}
	if state.AdminAuthCopy == string(encoded) {
		return nil
	}
	_, err = controlStore.Update(state.Generation, func(current *control.State) error {
		current.AdminAuthCopy = string(encoded)
		return nil
	})
	return err
}

func serveHTTPServer(server *http.Server, listener net.Listener, stop context.CancelFunc, result chan<- error) {
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	result <- err
	stop()
}

type restartCoordinator struct {
	once      sync.Once
	requested chan struct{}
}

func newRestartCoordinator() *restartCoordinator {
	return &restartCoordinator{requested: make(chan struct{})}
}

func (r *restartCoordinator) RequestRestart() bool {
	if r == nil {
		return false
	}
	accepted := false
	r.once.Do(func() {
		accepted = true
		close(r.requested)
	})
	return accepted
}

func (r *restartCoordinator) Requested() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.requested
}

type codexInspectionStopper interface {
	StopAndWait(context.Context) error
}

func stopCodexInspectionWorker(stopper codexInspectionStopper, timeout time.Duration) {
	if stopper == nil {
		return
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), timeout)
	err := stopper.StopAndWait(shutdownCtx)
	cancelShutdown()
	if err == nil {
		return
	}
	// Shutdown must remain bounded. The worker has already fenced new starts and
	// cancelled owned work; if it still cannot finish within the grace period,
	// leave an explicit error for the supervisor and let startup recovery reclaim
	// the expired lease instead of hanging the process indefinitely.
	log.Printf("shutdown codex inspection worker: %v; continuing bounded process shutdown", err)
}

func newPprofServer(addr string) (*http.Server, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", addr, err)
	}
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("pprof address must use a loopback host: %q", addr)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", httppprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", httppprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", httppprof.Trace)
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}, nil
}

func runUsageResponseMetadataBackfill(ctx context.Context, db *store.Store) {
	const batchLimit = 1000
	total := 0
	for {
		updated, err := db.BackfillUsageResponseMetadata(ctx, batchLimit)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("usage response metadata backfill: %v", err)
			}
			return
		}
		if updated == 0 {
			if total > 0 {
				log.Printf("usage response metadata backfill completed: updated=%d", total)
			}
			return
		}
		total += updated
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type walMaintenanceSlot struct {
	current atomic.Pointer[sqliterepo.WALMaintenance]
}

func (s *walMaintenanceSlot) Store(maintenance *sqliterepo.WALMaintenance) {
	if s == nil {
		return
	}
	s.current.Store(maintenance)
}

func (s *walMaintenanceSlot) Snapshot() sqliterepo.WALMaintenanceSnapshot {
	if s == nil {
		return sqliterepo.WALMaintenanceSnapshot{}
	}
	maintenance := s.current.Load()
	if maintenance == nil {
		return sqliterepo.WALMaintenanceSnapshot{}
	}
	return maintenance.Snapshot()
}

func (s *walMaintenanceSlot) CloseCurrent() error {
	if s == nil {
		return nil
	}
	maintenance := s.current.Swap(nil)
	if maintenance == nil {
		return nil
	}
	return maintenance.Close()
}
