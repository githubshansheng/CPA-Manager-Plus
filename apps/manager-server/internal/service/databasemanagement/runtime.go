package databasemanagement

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/outboxcontext"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/modelprice"
)

const (
	forwardDirection = "sqlite_to_mysql"
	statusTimeout    = 3 * time.Second
)

type RuntimeOptions struct {
	Control           *control.Store
	SQLite            database.Backend
	MySQL             database.Backend
	SQLitePath        string
	DataDir           string
	EnableOutbox      func(context.Context, uint64) error
	AcquireWriteFence func(context.Context) (func(), error)
	// ReplaceSQLiteCache owns the process-wide destructive boundary. While the
	// write fence is held it must stop users of the old handle, atomically move
	// the validated temporary file into place with rollback to the old file on
	// failure, reopen every SQLite-dependent service/worker, reinstall the
	// authoritative journal, and return the newly-bound backend. Returning an
	// error must leave the old database usable. A nil callback disables rebuild.
	ReplaceSQLiteCache func(context.Context, string, uint64, int64) (database.Backend, error)
	// ValidateBackend constructs the complete application repository graph for
	// a candidate backend. Configuration is not committed until this dialect
	// contract succeeds.
	ValidateBackend func(context.Context, database.Backend) error
	ReadCutoverOK   func() bool
	// DerivedConformanceOK is installed only after MySQL aggregate, projection,
	// and search semantics have passed the shared SQLite/MySQL repository suite.
	DerivedConformanceOK func() bool
	WriteSwitchOK        func(database.BackendKind) bool
}

// Runtime coordinates the durable control file, backend health and the
// migration metadata repository. The optional readiness gates deliberately
// default to false: a route is never changed before its repository suite and
// write fencing implementation have been installed by the process runtime.
type Runtime struct {
	control              *control.Store
	sqlite               database.Backend
	mysql                database.Backend
	sqlitePath           string
	dataDir              string
	enableOutbox         func(context.Context, uint64) error
	acquireWriteFence    func(context.Context) (func(), error)
	processWriteFence    func(context.Context) (func(), error)
	replaceSQLiteCache   func(context.Context, string, uint64, int64) (database.Backend, error)
	validateBackend      func(context.Context, database.Backend) error
	readCutoverOK        func() bool
	derivedConformanceOK func() bool
	writeSwitchOK        func(database.BackendKind) bool

	operationMu   sync.Mutex
	cacheMu       sync.Mutex
	backendMu     sync.RWMutex
	backgroundMu  sync.RWMutex
	migrationMu   sync.RWMutex
	migrationSeq  uint64
	migrationRun  migrationWorkState
	validationMu  sync.RWMutex
	validationRun validationProgressState
	sampler       *dbmysql.Sampler
}

func NewRuntime(options RuntimeOptions) (*Runtime, error) {
	if options.Control == nil {
		return nil, errors.New("database control store is required")
	}
	dataDir := strings.TrimSpace(options.DataDir)
	if dataDir == "" {
		dataDir = filepath.Dir(options.SQLitePath)
	}
	absoluteDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve database data directory: %w", err)
	}
	runtime := &Runtime{
		control: options.Control, sqlite: options.SQLite, mysql: options.MySQL,
		sqlitePath: options.SQLitePath, dataDir: absoluteDataDir,
		enableOutbox: options.EnableOutbox, readCutoverOK: options.ReadCutoverOK,
		processWriteFence:    options.AcquireWriteFence,
		replaceSQLiteCache:   options.ReplaceSQLiteCache,
		validateBackend:      options.ValidateBackend,
		derivedConformanceOK: options.DerivedConformanceOK,
		writeSwitchOK:        options.WriteSwitchOK, sampler: dbmysql.NewSampler(),
	}
	if options.AcquireWriteFence != nil {
		runtime.acquireWriteFence = runtime.acquireGlobalWriteFence
	}
	return runtime, nil
}

func (r *Runtime) acquireGlobalWriteFence(ctx context.Context) (func(), error) {
	r.backgroundMu.Lock()
	if r.processWriteFence == nil {
		r.backgroundMu.Unlock()
		return nil, errors.New("process write fence is not installed")
	}
	releaseProcess, err := r.processWriteFence(ctx)
	if err != nil {
		r.backgroundMu.Unlock()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseProcess()
			r.backgroundMu.Unlock()
		})
	}, nil
}

// AcquireApplicationWriteFence blocks authority and derived mutations on both
// live backends. Runtime migration and replication batches are paused by the
// outer background gate installed by acquireGlobalWriteFence.
func (r *Runtime) AcquireApplicationWriteFence(ctx context.Context) (func(), error) {
	r.backendMu.RLock()
	var sqliteDB, mysqlDB *sql.DB
	if r.sqlite != nil {
		sqliteDB = r.sqlite.DB()
	}
	if r.mysql != nil {
		mysqlDB = r.mysql.DB()
	}
	r.backendMu.RUnlock()
	var releases []func()
	for _, db := range []*sql.DB{sqliteDB, mysqlDB} {
		if db == nil {
			continue
		}
		release, err := outboxcontext.AcquireWriteFence(ctx, db)
		if err != nil {
			for index := len(releases) - 1; index >= 0; index-- {
				releases[index]()
			}
			return nil, err
		}
		releases = append(releases, release)
	}
	if len(releases) == 0 {
		return nil, errors.New("no database is available for a write fence")
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for index := len(releases) - 1; index >= 0; index-- {
				releases[index]()
			}
		})
	}, nil
}

func (r *Runtime) Close() error {
	r.backendMu.Lock()
	defer r.backendMu.Unlock()
	var result error
	if r.mysql != nil {
		result = errors.Join(result, r.mysql.Close())
		r.mysql = nil
	}
	// The bootstrap Store initially owns the same SQLite handle, but after an
	// online cache rebuild Runtime owns a newly-opened replacement handle. Close
	// both cases here; sql.DB.Close is idempotent and the bootstrap defer may
	// safely observe an already-closed original handle.
	if r.sqlite != nil {
		result = errors.Join(result, r.sqlite.Close())
		r.sqlite = nil
	}
	return result
}

func (r *Runtime) Status(ctx context.Context) (Status, error) {
	state, err := r.loadState()
	if err != nil {
		return Status{}, err
	}
	status := Status{
		Generation: state.Generation,
		DatabaseTopology: database.TopologyStatus{
			Generation: state.Generation, WritePrimary: state.WritePrimary,
			BusinessReadPrimary: state.BusinessReadPrimary, SystemReadPrimary: state.SystemReadPrimary,
			FailoverState: state.Failover.Status,
		},
	}
	status.DatabaseTopology.ReadCutoverReady = r.readCutoverOK != nil && r.readCutoverOK() &&
		r.derivedConformanceOK != nil && r.derivedConformanceOK()
	status.DatabaseTopology.WriteFailoverReady = r.writeSwitchOK != nil &&
		r.writeSwitchOK(database.BackendMySQL)
	status.Databases.SQLite = r.sqliteStatus(ctx, state)
	status.Databases.MySQL = r.mysqlStatus(ctx, state)
	status.DatabaseTopology.ReadOnly = state.WritePrimary == database.BackendSQLite && !status.Databases.SQLite.Available ||
		state.WritePrimary == database.BackendMySQL && !status.Databases.MySQL.Available
	status.CacheCoverage.RetentionDays = state.CachePolicy.RetentionDays
	status.CacheCoverage.CleanupEnabled = state.CachePolicy.Enabled
	if state.ReplicationEnabled {
		direction, source, target := replicationRoute(state.WritePrimary)
		status.Replication.Enabled = true
		status.Replication.State = "running"
		status.Replication.Direction = direction
		status.Replication.Epoch = state.RoutingEpoch
		status.Replication.Source = source
		status.Replication.Target = target
	}
	if !state.ReplicationEnabled {
		status.Replication.State = "idle"
	}
	if repository, repoErr := r.metadataRepository(ctx, false); repoErr == nil && repository != nil {
		r.enrichMigrationStatus(ctx, repository, state, &status)
	}
	return status, nil
}

func replicationRoute(writePrimary database.BackendKind) (string, database.BackendKind, database.BackendKind) {
	if writePrimary == database.BackendMySQL {
		return reverseDirection, database.BackendMySQL, database.BackendSQLite
	}
	return forwardDirection, database.BackendSQLite, database.BackendMySQL
}

func (r *Runtime) TestMySQL(ctx context.Context, input MySQLConnectionInput) (MySQLTestResult, error) {
	if err := input.Validate(); err != nil {
		return MySQLTestResult{}, err
	}
	state, err := r.loadState()
	if err != nil {
		return MySQLTestResult{}, err
	}
	config, cleanup, err := r.mysqlConfig(input, state, false)
	if err != nil {
		return MySQLTestResult{}, err
	}
	defer cleanup()
	result, err := dbmysql.Test(ctx, config)
	if err != nil {
		return MySQLTestResult{Success: false, Error: sanitizeDatabaseError(err)}, nil
	}
	warnings := make([]string, 0, 3)
	for name, capability := range result.Capabilities {
		if !capability.Available {
			warnings = append(warnings, name+": "+capability.Error)
		}
	}
	if config.TLSMode == dbmysql.TLSDisabled {
		warnings = append(warnings, "TLS is disabled; credentials and database traffic are not protected")
	}
	return MySQLTestResult{Success: true, Version: result.Version, LatencyMS: result.PingLatencyMS, Warnings: warnings}, nil
}

func (r *Runtime) SaveMySQLConfig(ctx context.Context, input MySQLConfigMutation) (Status, error) {
	if err := input.MutationControl.Validate(); err != nil {
		return Status{}, err
	}
	if err := input.MySQLConnectionInput.Validate(); err != nil {
		return Status{}, err
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	state, err := r.requireGeneration(input.ExpectedGeneration)
	if err != nil {
		return Status{}, err
	}
	repository, err := r.metadataRepository(ctx, true)
	if err != nil {
		return Status{}, err
	}
	if replay, ok, err := replayStatus(ctx, repository, "mysql_config", input, input.IdempotencyKey); err != nil || ok {
		return replay, err
	}
	config, cleanup, err := r.mysqlConfig(input.MySQLConnectionInput, state, true)
	if err != nil {
		return Status{}, err
	}
	defer cleanup()
	testResult, err := dbmysql.Test(ctx, config)
	if err != nil {
		return Status{}, fmt.Errorf("test mysql configuration: %w", err)
	}
	if err := testResult.RequireMigrationCapabilities(); err != nil {
		return Status{}, errors.Join(ErrInvalidRequest, err)
	}
	opened, err := dbmysql.Open(ctx, config)
	if err != nil {
		return Status{}, err
	}
	if _, err := preflightMySQLTargetSchema(ctx, opened); err != nil {
		_ = opened.Close()
		return Status{}, err
	}
	if r.validateBackend != nil {
		candidate := database.NewSQLBackend(database.BackendMySQL, opened)
		if err := r.validateBackend(ctx, candidate); err != nil {
			_ = opened.Close()
			return Status{}, errors.Join(ErrInvalidRequest, fmt.Errorf("mysql repository contract: %w", err))
		}
	}
	next, err := r.control.Update(input.ExpectedGeneration, func(current *control.State) error {
		current.MySQL = config
		return nil
	})
	if err != nil {
		_ = opened.Close()
		return Status{}, mapControlError(err)
	}
	r.replaceMySQL(database.NewSQLBackend(database.BackendMySQL, opened))
	status, err := r.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	status.Generation = next.Generation
	if err := storeStatusReplay(ctx, repository, "mysql_config", input, input.IdempotencyKey, status); err != nil {
		return Status{}, err
	}
	return status, nil
}

func (r *Runtime) EnableReplication(ctx context.Context, input MutationControl) (Status, error) {
	if err := input.Validate(); err != nil {
		return Status{}, err
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	state, err := r.requireGeneration(input.ExpectedGeneration)
	if err != nil {
		return Status{}, err
	}
	repository, err := r.metadataRepository(ctx, true)
	if err != nil {
		return Status{}, err
	}
	if replay, ok, err := replayStatus(ctx, repository, "enable_replication", input, input.IdempotencyKey); err != nil || ok {
		return replay, err
	}
	if state.MySQL.Host == "" {
		return Status{}, errors.Join(ErrInvalidRequest, errors.New("mysql is not configured"))
	}
	mysqlDB, release := r.mysqlDB()
	defer release()
	if mysqlDB == nil {
		return Status{}, errors.Join(ErrUnavailable, errors.New("mysql is not connected"))
	}
	testResult, err := dbmysql.Test(ctx, state.MySQL)
	if err != nil {
		return Status{}, fmt.Errorf("preflight mysql target: %w", err)
	}
	if err := testResult.RequireMigrationCapabilities(); err != nil {
		return Status{}, errors.Join(ErrInvalidRequest, err)
	}
	if _, err := preflightMySQLTargetSchema(ctx, mysqlDB); err != nil {
		return Status{}, err
	}
	if err := schema.Ensure(ctx, mysqlDB); err != nil {
		return Status{}, err
	}
	validation, err := schema.Validate(ctx, mysqlDB)
	if err != nil {
		return Status{}, err
	}
	if !validation.Valid {
		return Status{}, fmt.Errorf("%w: mysql schema differs from the canonical manifest: %v", ErrInvalidRequest, validation.Differences)
	}
	mysqlRepository := databasemigration.NewSQLRepository(mysqlDB, databasemigration.DialectMySQL)
	if err := mysqlRepository.EnsureSchema(ctx); err != nil {
		return Status{}, err
	}
	if err := outboxcontext.PrepareMySQLJournal(ctx, mysqlDB); err != nil {
		return Status{}, fmt.Errorf("install mysql reverse outbox contract: %w", err)
	}
	if err := copyConfiguration(ctx, r.sqliteDB(), mysqlDB, schema.Current()); err != nil {
		return Status{}, fmt.Errorf("copy complete configuration to mysql: %w", err)
	}
	if r.enableOutbox == nil {
		return Status{}, errors.Join(ErrUnavailable, errors.New("authoritative outbox instrumentation is not installed"))
	}
	nextEpoch := state.RoutingEpoch
	if nextEpoch == 0 {
		nextEpoch = 1
	}
	if err := r.enableOutbox(ctx, nextEpoch); err != nil {
		return Status{}, fmt.Errorf("enable sqlite outbox: %w", err)
	}
	next, err := r.control.Update(input.ExpectedGeneration, func(current *control.State) error {
		current.ReplicationEnabled = true
		current.RoutingEpoch = nextEpoch
		return nil
	})
	if err != nil {
		return Status{}, mapControlError(err)
	}
	status, err := r.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	status.Generation = next.Generation
	if err := storeStatusReplay(ctx, repository, "enable_replication", input, input.IdempotencyKey, status); err != nil {
		return Status{}, err
	}
	return status, nil
}

// preflightMySQLTargetSchema is deliberately read-only. A non-empty target
// must already match the complete canonical manifest before EnableReplication
// may issue any CPAMP DDL; this prevents an unrelated or partially compatible
// schema from being modified and only then rejected.
func preflightMySQLTargetSchema(ctx context.Context, db *sql.DB) (empty bool, err error) {
	if db == nil {
		return false, errors.Join(ErrUnavailable, errors.New("mysql is not connected"))
	}
	var tables int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema=DATABASE()`).Scan(&tables); err != nil {
		return false, fmt.Errorf("inspect mysql target schema: %w", err)
	}
	if tables == 0 {
		return true, nil
	}
	validation, err := schema.Validate(ctx, db)
	if err != nil {
		return false, fmt.Errorf("validate existing mysql target schema: %w", err)
	}
	if !validation.Valid {
		return false, errors.Join(ErrInvalidRequest, fmt.Errorf(
			"mysql target schema is non-empty and incompatible with the canonical manifest: %v",
			validation.Differences))
	}
	return false, nil
}

func (r *Runtime) StartMigration(ctx context.Context, input MutationControl) (Status, error) {
	if err := input.Validate(); err != nil {
		return Status{}, err
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	state, err := r.requireGeneration(input.ExpectedGeneration)
	if err != nil {
		return Status{}, err
	}
	repository, err := r.metadataRepository(ctx, true)
	if err != nil {
		return Status{}, err
	}
	if replay, ok, err := replayStatus(ctx, repository, "start_migration", input, input.IdempotencyKey); err != nil || ok {
		return replay, err
	}
	if !state.ReplicationEnabled {
		return Status{}, errors.Join(ErrInvalidRequest, errors.New("replication must be enabled before history migration"))
	}
	migrationStatus := state.Migration.Status
	if state.Migration.ID != "" {
		persisted, migrationErr := repository.Migration(ctx, state.Migration.ID)
		if migrationErr == nil {
			migrationStatus = string(persisted.Status)
		} else if !errors.Is(migrationErr, databasemigration.ErrNotFound) {
			return Status{}, mapMigrationError(migrationErr)
		}
	}
	if state.Migration.ID != "" && migrationStatus != string(databasemigration.StatusCanceled) &&
		migrationStatus != string(databasemigration.StatusSucceeded) {
		if migrationStatus == string(databasemigration.StatusPaused) ||
			migrationStatus == string(databasemigration.StatusFailed) {
			return Status{}, errors.Join(ErrInvalidRequest, fmt.Errorf(
				"database migration %q is %s; resume or cancel it before starting a new migration",
				state.Migration.ID, migrationStatus))
		}
		return Status{}, errors.Join(ErrInvalidRequest, errors.New("another database migration is active"))
	}
	mysqlDB, releaseMySQL := r.mysqlDB()
	defer releaseMySQL()
	if mysqlDB == nil {
		return Status{}, errors.Join(ErrUnavailable, errors.New("mysql is not connected"))
	}
	maxAllowedPacket, err := mysqlSessionMaxAllowedPacket(ctx, mysqlDB)
	if err != nil {
		return Status{}, err
	}
	if _, err := preflightSQLiteSource(ctx, r.sqliteDB(), schema.Current().AuthoritativeTables(), maxAllowedPacket); err != nil {
		return Status{}, errors.Join(ErrInvalidRequest, fmt.Errorf("sqlite to mysql source preflight failed: %w", err))
	}
	frozenPriceHash, frozenPriceBook, err := configurationPriceSnapshot(ctx, r.sqliteDB())
	if err != nil {
		return Status{}, fmt.Errorf("freeze model price manifest: %w", err)
	}
	routing, err := repository.Routing(ctx)
	if err != nil {
		return Status{}, err
	}
	migration, _, err := repository.CreateMigration(ctx, databasemigration.CreateMigrationRequest{
		IdempotencyKey: input.IdempotencyKey, ExpectedGeneration: routing.Generation,
		Source: databasemigration.BackendSQLite, Target: databasemigration.BackendMySQL,
		BatchSize: databasemigration.DefaultBatchSize, FrozenPriceHash: frozenPriceHash,
		FrozenPriceBook: frozenPriceBook,
	})
	if err != nil {
		return Status{}, mapMigrationError(err)
	}
	migration, err = repository.AdvanceMigration(ctx, migration.ID, migration.Generation, databasemigration.PhaseCopyHistory)
	if err != nil {
		return Status{}, mapMigrationError(err)
	}
	next, err := r.control.Update(input.ExpectedGeneration, func(current *control.State) error {
		current.Migration = control.MigrationRef{ID: migration.ID, Phase: string(migration.Phase), Status: string(migration.Status)}
		return nil
	})
	if err != nil {
		return Status{}, mapControlError(err)
	}
	status, err := r.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	status.Generation = next.Generation
	if err := storeStatusReplay(ctx, repository, "start_migration", input, input.IdempotencyKey, status); err != nil {
		return Status{}, err
	}
	return status, nil
}

func (r *Runtime) UpdateMigration(ctx context.Context, migrationID, action string, input MutationControl) (Status, error) {
	if err := input.Validate(); err != nil {
		return Status{}, err
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	state, err := r.requireGeneration(input.ExpectedGeneration)
	if err != nil {
		return Status{}, err
	}
	if state.Migration.ID != migrationID {
		return Status{}, errors.Join(ErrInvalidRequest, errors.New("migration id does not match the active migration"))
	}
	repository, err := r.metadataRepository(ctx, true)
	if err != nil {
		return Status{}, err
	}
	operation := "migration_" + action
	replayInput := struct {
		ID string `json:"id"`
		MutationControl
	}{ID: migrationID, MutationControl: input}
	if replay, ok, err := replayStatus(ctx, repository, operation, replayInput, input.IdempotencyKey); err != nil || ok {
		return replay, err
	}
	migration, err := repository.Migration(ctx, migrationID)
	if err != nil {
		return Status{}, mapMigrationError(err)
	}
	switch action {
	case "pause":
		migration, err = repository.PauseMigration(ctx, migrationID, migration.Generation)
	case "resume":
		if err = r.requireMigrationResumeTarget(ctx, migration); err == nil {
			migration, err = repository.ResumeMigration(ctx, migrationID, migration.Generation)
		}
	case "cancel":
		migration, err = repository.CancelMigration(ctx, migrationID, migration.Generation)
	case "validate":
		migration, err = r.validateMigration(ctx, migration, repository)
	default:
		return Status{}, errors.Join(ErrInvalidRequest, errors.New("unsupported migration action"))
	}
	if err != nil {
		// A failed administrative attempt may happen before any durable state
		// transition (for example, strict schema validation). Preserve the exact
		// error in the append-only migration timeline instead of leaving it only
		// in an ephemeral HTTP response.
		// Detach the audit write from the request lifetime: a browser timeout or
		// disconnect is itself one of the failures that must remain observable.
		auditCtx, cancelAudit := context.WithTimeout(context.WithoutCancel(ctx), statusTimeout)
		auditErr := repository.RecordMigrationError(auditCtx, migrationID, action+"_failed", err)
		cancelAudit()
		if auditErr != nil {
			return Status{}, errors.Join(mapMigrationError(err),
				fmt.Errorf("record migration operation failure: %w", auditErr))
		}
		return Status{}, mapMigrationError(err)
	}
	if action == "pause" || action == "cancel" {
		r.cancelMigrationWork(migrationID)
	}
	next, err := r.control.Update(input.ExpectedGeneration, func(current *control.State) error {
		current.Migration = control.MigrationRef{ID: migration.ID, Phase: string(migration.Phase), Status: string(migration.Status), ValidationToken: migration.ValidationToken}
		return nil
	})
	if err != nil {
		return Status{}, mapControlError(err)
	}
	status, err := r.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	status.Generation = next.Generation
	if err := storeStatusReplay(ctx, repository, operation, replayInput, input.IdempotencyKey, status); err != nil {
		return Status{}, err
	}
	return status, nil
}

func (r *Runtime) Cutover(ctx context.Context, input CutoverMutation) (Status, error) {
	if err := input.MutationControl.Validate(); err != nil {
		return Status{}, err
	}
	if err := input.DangerousOperationConfirmation.Validate(database.BackendMySQL); err != nil {
		return Status{}, err
	}
	if r.readCutoverOK == nil || !r.readCutoverOK() {
		return Status{}, errors.Join(ErrUnavailable, errors.New("mysql business-read repository conformance gate has not passed"))
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	state, err := r.requireGeneration(input.ExpectedGeneration)
	if err != nil {
		return Status{}, err
	}
	if err := requireCurrentValidationConfirmation(state,
		input.DangerousOperationConfirmation); err != nil {
		return Status{}, err
	}
	if state.Migration.ID != input.MigrationID {
		return Status{}, errors.Join(ErrInvalidRequest, errors.New("migration id does not match the active migration"))
	}
	repository, err := r.metadataRepository(ctx, true)
	if err != nil {
		return Status{}, err
	}
	if replay, ok, replayErr := replayStatus(ctx, repository, "routing_cutover", input, input.IdempotencyKey); replayErr != nil || ok {
		return replay, replayErr
	}
	migration, err := repository.Migration(ctx, input.MigrationID)
	if err != nil {
		return Status{}, mapMigrationError(err)
	}
	if migration.Validation == nil || !migration.Validation.Passed || migration.ValidationToken != input.ValidationToken {
		return Status{}, errors.Join(ErrUnsafeOperation, errors.New("cutover validation token is missing, stale, or failed"))
	}
	if err := requireMigrationReadyForCutover(migration); err != nil {
		return Status{}, err
	}
	if r.acquireWriteFence == nil {
		return Status{}, errors.Join(ErrUnavailable, errors.New("global authoritative write fence is not installed"))
	}
	releaseFence, err := r.acquireWriteFence(ctx)
	if err != nil {
		return Status{}, err
	}
	defer releaseFence()
	pending, _, _, finalWatermark, err := repository.OutboxBacklog(ctx)
	if err != nil {
		return Status{}, err
	}
	if pending != 0 {
		return Status{}, errors.Join(ErrUnsafeOperation, errors.New("sqlite outbox gained pending mutations after validation"))
	}
	mysqlDB, releaseMySQL := r.mysqlDB()
	defer releaseMySQL()
	if mysqlDB == nil {
		return Status{}, errors.Join(ErrUnavailable, errors.New("mysql is not connected"))
	}
	appliedWatermark, err := mysqlAppliedWatermark(ctx, mysqlDB)
	if err != nil {
		return Status{}, err
	}
	if finalWatermark != migration.FinalOutboxWatermark || appliedWatermark != finalWatermark {
		return Status{}, errors.Join(ErrUnsafeOperation, fmt.Errorf(
			"validation watermark is stale: validated=%d source=%d target=%d",
			migration.FinalOutboxWatermark, finalWatermark, appliedWatermark))
	}
	if err := synchronizeBusinessReadRouting(ctx, repository,
		databasemigration.BackendMySQL); err != nil {
		return Status{}, err
	}
	mysqlRepository := databasemigration.NewSQLRepository(mysqlDB, databasemigration.DialectMySQL)
	if err := mysqlRepository.EnsureSchema(ctx); err != nil {
		return Status{}, err
	}
	if err := synchronizeBusinessReadRouting(ctx, mysqlRepository,
		databasemigration.BackendMySQL); err != nil {
		return Status{}, err
	}
	if migration.Phase == databasemigration.PhaseReadyToCutover {
		migration, err = repository.CompleteCutover(ctx, migration.ID, migration.Generation, input.ValidationToken)
		if err != nil {
			return Status{}, mapMigrationError(err)
		}
	}
	next, err := r.control.Update(input.ExpectedGeneration, func(current *control.State) error {
		if current.Migration.ID != migration.ID {
			return control.ErrGenerationConflict
		}
		current.BusinessReadPrimary = database.BackendMySQL
		current.Migration = control.MigrationRef{ID: migration.ID, Phase: string(migration.Phase),
			Status: string(migration.Status), ValidationToken: migration.ValidationToken}
		return nil
	})
	if err != nil {
		return Status{}, mapControlError(err)
	}
	status, err := r.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	status.Generation = next.Generation
	if err := storeStatusReplay(ctx, repository, "routing_cutover", input, input.IdempotencyKey, status); err != nil {
		return Status{}, err
	}
	return status, nil
}

func synchronizeBusinessReadRouting(
	ctx context.Context,
	repository *databasemigration.SQLRepository,
	target databasemigration.Backend,
) error {
	routing, err := repository.Routing(ctx)
	if err != nil {
		return err
	}
	if routing.BusinessRead == target {
		return nil
	}
	_, err = repository.CompareAndSwapRouting(ctx, routing.Generation, routing.Epoch,
		databasemigration.RoutingChange{WritePrimary: routing.WritePrimary,
			BusinessRead: target, SystemRead: routing.SystemRead})
	return err
}

func (r *Runtime) Failover(ctx context.Context, input FailoverMutation) (Status, error) {
	return r.failover(ctx, input)
}

func (r *Runtime) UpdateCachePolicy(ctx context.Context, input CachePolicyMutation) (Status, error) {
	if err := input.MutationControl.Validate(); err != nil {
		return Status{}, err
	}
	if input.RetentionDays < 1 || input.RetentionDays > 3650 {
		return Status{}, errors.Join(ErrInvalidRequest, errors.New("retentionDays must be between 1 and 3650"))
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	if _, err := r.requireGeneration(input.ExpectedGeneration); err != nil {
		return Status{}, err
	}
	repository, err := r.metadataRepository(ctx, true)
	if err != nil {
		return Status{}, err
	}
	if replay, ok, err := replayStatus(ctx, repository, "cache_policy", input, input.IdempotencyKey); err != nil || ok {
		return replay, err
	}
	policy, err := repository.CachePolicy(ctx)
	if err != nil {
		return Status{}, err
	}
	if _, err := repository.SetCachePolicy(ctx, policy.Generation, input.Enabled, input.RetentionDays, databasemigration.DefaultBatchSize); err != nil {
		return Status{}, mapMigrationError(err)
	}
	next, err := r.control.Update(input.ExpectedGeneration, func(current *control.State) error {
		current.CachePolicy.Enabled = input.Enabled
		current.CachePolicy.RetentionDays = input.RetentionDays
		return nil
	})
	if err != nil {
		return Status{}, mapControlError(err)
	}
	status, err := r.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	status.Generation = next.Generation
	if err := storeStatusReplay(ctx, repository, "cache_policy", input, input.IdempotencyKey, status); err != nil {
		return Status{}, err
	}
	return status, nil
}

func (r *Runtime) PreviewCacheCleanup(ctx context.Context, input MutationControl) (CacheCleanupPreview, error) {
	if err := input.Validate(); err != nil {
		return CacheCleanupPreview{}, err
	}
	state, err := r.requireGeneration(input.ExpectedGeneration)
	if err != nil {
		return CacheCleanupPreview{}, err
	}
	if r.sqliteDB() == nil {
		return CacheCleanupPreview{Eligible: false, BlockedReason: "sqlite is not open"}, nil
	}
	repository, err := r.metadataRepository(ctx, true)
	if err != nil {
		return CacheCleanupPreview{}, err
	}
	safety, err := r.cacheCleanupSafety(ctx, state, repository)
	if err != nil {
		return CacheCleanupPreview{}, err
	}
	if safetyErr := databasemigration.EvaluateCleanupSafety(safety, time.Now()); safetyErr != nil {
		return CacheCleanupPreview{Eligible: false, BlockedReason: safetyErr.Error()}, nil
	}
	manager := r.cacheManager(repository)
	preview, err := manager.PreviewCleanup(ctx, state.CachePolicy.RetentionDays)
	if err != nil {
		return CacheCleanupPreview{}, err
	}
	return summarizeCacheCleanupPreview(preview), nil
}

func (r *Runtime) CleanupCache(ctx context.Context, input CacheCleanupMutation) (Status, error) {
	if err := input.MutationControl.Validate(); err != nil {
		return Status{}, err
	}
	if err := input.DangerousOperationConfirmation.Validate(database.BackendSQLite); err != nil {
		return Status{}, err
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	state, err := r.requireGeneration(input.ExpectedGeneration)
	if err != nil {
		return Status{}, err
	}
	if err := requireCurrentValidationConfirmation(state,
		input.DangerousOperationConfirmation); err != nil {
		return Status{}, err
	}
	if r.sqliteDB() == nil {
		return Status{}, errors.Join(ErrUnavailable, errors.New("sqlite is not open"))
	}
	repository, err := r.metadataRepository(ctx, true)
	if err != nil {
		return Status{}, err
	}
	if replay, ok, replayErr := replayStatus(ctx, repository, "cache_cleanup", input, input.IdempotencyKey); replayErr != nil || ok {
		return replay, replayErr
	}
	if rebuild, exists, activeErr := repository.ActiveMaintenanceTask(ctx,
		databasemigration.TaskRebuild); activeErr != nil {
		return Status{}, activeErr
	} else if exists {
		return Status{}, errors.Join(ErrInvalidRequest,
			fmt.Errorf("sqlite cache rebuild task %s is already %s", rebuild.ID, rebuild.Status))
	}
	if active, exists, activeErr := repository.ActiveMaintenanceTask(ctx, databasemigration.TaskCleanup); activeErr != nil {
		return Status{}, activeErr
	} else if exists {
		if active.IdempotencyKey == input.IdempotencyKey {
			status, statusErr := r.Status(ctx)
			if statusErr != nil {
				return Status{}, statusErr
			}
			if storeErr := storeStatusReplay(ctx, repository, "cache_cleanup", input,
				input.IdempotencyKey, status); storeErr != nil {
				return Status{}, storeErr
			}
			return status, nil
		}
		return Status{}, errors.Join(ErrInvalidRequest,
			fmt.Errorf("sqlite cache cleanup task %s is already %s", active.ID, active.Status))
	}
	safety, err := r.cacheCleanupSafety(ctx, state, repository)
	if err != nil {
		return Status{}, err
	}
	if err := databasemigration.EvaluateCleanupSafety(safety, time.Now()); err != nil {
		return Status{}, mapMigrationError(err)
	}
	manager := r.cacheManager(repository)
	preview, err := manager.PreviewCleanup(ctx, state.CachePolicy.RetentionDays)
	if err != nil {
		return Status{}, err
	}
	if _, _, err := manager.StartCleanup(ctx, input.IdempotencyKey, preview,
		state.CachePolicy.RetentionDays, safety); err != nil {
		return Status{}, mapMigrationError(err)
	}
	status, err := r.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	if err := storeStatusReplay(ctx, repository, "cache_cleanup", input, input.IdempotencyKey, status); err != nil {
		return Status{}, err
	}
	return status, nil
}

func (r *Runtime) RebuildCache(ctx context.Context, input CacheRebuildMutation) (Status, error) {
	if err := input.MutationControl.Validate(); err != nil {
		return Status{}, err
	}
	if err := input.DangerousOperationConfirmation.Validate(database.BackendSQLite); err != nil {
		return Status{}, err
	}
	if input.RetentionDays < 1 || input.RetentionDays > 3650 {
		return Status{}, errors.Join(ErrInvalidRequest,
			errors.New("retentionDays must be between 1 and 3650"))
	}
	if r.acquireWriteFence == nil || r.replaceSQLiteCache == nil {
		return Status{}, errors.Join(ErrUnavailable,
			errors.New("sqlite cache rebuild requires a global write fence and a process-wide replacement callback"))
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	state, err := r.requireGeneration(input.ExpectedGeneration)
	if err != nil {
		return Status{}, err
	}
	if err := requireCurrentValidationConfirmation(state,
		input.DangerousOperationConfirmation); err != nil {
		return Status{}, err
	}
	if state.WritePrimary != database.BackendSQLite {
		return Status{}, errors.Join(ErrUnsafeOperation,
			errors.New("sqlite cache rebuild currently requires sqlite to remain the fenced write primary"))
	}
	if r.sqliteDB() == nil {
		return Status{}, errors.Join(ErrUnavailable, errors.New("sqlite is not open"))
	}
	repository, err := r.metadataRepository(ctx, true)
	if err != nil {
		return Status{}, err
	}
	if replay, ok, replayErr := replayStatus(ctx, repository, "cache_rebuild", input,
		input.IdempotencyKey); replayErr != nil || ok {
		return replay, replayErr
	}
	for _, kind := range []databasemigration.MaintenanceTaskKind{
		databasemigration.TaskRebuild, databasemigration.TaskCleanup,
	} {
		active, exists, activeErr := repository.ActiveMaintenanceTask(ctx, kind)
		if activeErr != nil {
			return Status{}, activeErr
		}
		if !exists {
			continue
		}
		if kind == databasemigration.TaskRebuild && active.IdempotencyKey == input.IdempotencyKey {
			status, statusErr := r.Status(ctx)
			if statusErr != nil {
				return Status{}, statusErr
			}
			if storeErr := storeStatusReplay(ctx, repository, "cache_rebuild", input,
				input.IdempotencyKey, status); storeErr != nil {
				return Status{}, storeErr
			}
			return status, nil
		}
		return Status{}, errors.Join(ErrInvalidRequest,
			fmt.Errorf("sqlite cache %s task %s is already %s", kind, active.ID, active.Status))
	}
	safety, err := r.cacheCleanupSafety(ctx, state, repository)
	if err != nil {
		return Status{}, err
	}
	if err := databasemigration.EvaluateCleanupSafety(safety, time.Now()); err != nil {
		return Status{}, mapMigrationError(err)
	}
	manager := r.cacheManager(repository)
	if _, _, err := manager.StartRebuild(ctx, input.IdempotencyKey, input.RetentionDays,
		safety); err != nil {
		return Status{}, mapMigrationError(err)
	}
	status, err := r.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	if err := storeStatusReplay(ctx, repository, "cache_rebuild", input,
		input.IdempotencyKey, status); err != nil {
		return Status{}, err
	}
	return status, nil
}

func (r *Runtime) ResponseCoverage(ctx context.Context, _ string) ResponseCoverage {
	state, err := r.loadState()
	if err != nil || state.BusinessReadPrimary != database.BackendMySQL {
		return ResponseCoverage{}
	}
	// A fallback header is emitted only by a routed query implementation after
	// it actually selected SQLite. The coordinator alone must not claim fallback.
	return ResponseCoverage{}
}

func (r *Runtime) loadState() (control.State, error) {
	state, err := r.control.Load()
	if errors.Is(err, control.ErrNotFound) {
		return control.DefaultState(), nil
	}
	return state, err
}

func (r *Runtime) requireGeneration(expected uint64) (control.State, error) {
	state, err := r.loadState()
	if err != nil {
		return control.State{}, err
	}
	if state.Generation != expected {
		return control.State{}, fmt.Errorf("%w: expected %d, current %d", ErrGenerationConflict, expected, state.Generation)
	}
	return state, nil
}

func (r *Runtime) metadataRepository(ctx context.Context, ensure bool) (*databasemigration.SQLRepository, error) {
	if db := r.sqliteDB(); db != nil {
		repository := databasemigration.NewSQLRepository(db, databasemigration.DialectSQLite)
		if ensure {
			if err := repository.EnsureSchema(ctx); err != nil {
				return nil, err
			}
		}
		return repository, nil
	}
	mysqlDB, release := r.mysqlDB()
	defer release()
	if mysqlDB == nil {
		return nil, errors.Join(ErrUnavailable, errors.New("no database is available for migration control state"))
	}
	repository := databasemigration.NewSQLRepository(mysqlDB, databasemigration.DialectMySQL)
	if ensure {
		if err := repository.EnsureSchema(ctx); err != nil {
			return nil, err
		}
	}
	return repository, nil
}

func (r *Runtime) sqliteDB() *sql.DB {
	r.backendMu.RLock()
	defer r.backendMu.RUnlock()
	if r.sqlite == nil {
		return nil
	}
	return r.sqlite.DB()
}

func (r *Runtime) mysqlDB() (*sql.DB, func()) {
	r.backendMu.RLock()
	if r.mysql == nil {
		r.backendMu.RUnlock()
		return nil, func() {}
	}
	return r.mysql.DB(), r.backendMu.RUnlock
}

func (r *Runtime) replaceMySQL(next database.Backend) {
	// Keep the old pool alive until every background operation that acquired it
	// has released its backend lease. In particular, a long derived rebuild may
	// hold a transaction open while an administrator reconnects or changes the
	// MySQL configuration. Closing that pool underneath the transaction turns a
	// harmless replacement into go-sql-driver's "invalid connection" error.
	r.backgroundMu.Lock()
	defer r.backgroundMu.Unlock()
	r.backendMu.Lock()
	previous := r.mysql
	r.mysql = next
	r.backendMu.Unlock()
	if previous != nil && previous != next {
		_ = previous.Close()
	}
}

func mapControlError(err error) error {
	if errors.Is(err, control.ErrGenerationConflict) {
		return errors.Join(ErrGenerationConflict, err)
	}
	return err
}

func mapMigrationError(err error) error {
	switch {
	case errors.Is(err, databasemigration.ErrGenerationConflict), errors.Is(err, databasemigration.ErrIdempotencyConflict):
		return errors.Join(ErrGenerationConflict, err)
	case errors.Is(err, databasemigration.ErrInvalidTransition):
		return errors.Join(ErrInvalidRequest, err)
	case errors.Is(err, databasemigration.ErrCleanupUnsafe):
		return errors.Join(ErrUnsafeOperation, err)
	default:
		return err
	}
}

func sanitizeDatabaseError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if index := strings.Index(strings.ToLower(message), "password="); index >= 0 {
		message = message[:index] + "password=***"
	}
	return message
}

func replayStatus(ctx context.Context, repository *databasemigration.SQLRepository, operation string, request any, key string) (Status, bool, error) {
	record, err := repository.OperationIdempotency(ctx, key)
	if errors.Is(err, databasemigration.ErrNotFound) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, err
	}
	hash, err := databasemigration.HashIdempotencyRequest(request)
	if err != nil {
		return Status{}, false, err
	}
	if record.Operation != operation || record.RequestHash != hash {
		return Status{}, false, errors.Join(ErrGenerationConflict, databasemigration.ErrIdempotencyConflict)
	}
	var status Status
	if err := json.Unmarshal(record.Result, &status); err != nil {
		return Status{}, false, err
	}
	return status, true, nil
}

func storeStatusReplay(ctx context.Context, repository *databasemigration.SQLRepository, operation string, request any, key string, status Status) error {
	hash, err := databasemigration.HashIdempotencyRequest(request)
	if err != nil {
		return err
	}
	result, err := json.Marshal(status)
	if err != nil {
		return err
	}
	_, _, err = repository.StoreOperationIdempotency(ctx, databasemigration.OperationIdempotencyRecord{
		Key: key, Operation: operation, RequestHash: hash, Result: result, Generation: int64(status.Generation),
	})
	return mapMigrationError(err)
}

func (r *Runtime) mysqlConfig(input MySQLConnectionInput, state control.State, persistentCA bool) (dbmysql.Config, func(), error) {
	host, port, err := splitAddress(input.Address)
	if err != nil {
		return dbmysql.Config{}, func() {}, err
	}
	password := input.Password
	if password == "" && sameMySQLIdentity(state.MySQL, host, port, input.Database, input.Username) {
		password = state.MySQL.Password
	}
	config := dbmysql.Config{Host: host, Port: port, Database: strings.TrimSpace(input.Database),
		Username: strings.TrimSpace(input.Username), Password: password, TLSMode: strings.TrimSpace(input.TLSMode)}
	cleanup := func() {}
	certificate := strings.TrimSpace(input.CACertificate)
	if certificate == "" && sameMySQLIdentity(state.MySQL, host, port, input.Database, input.Username) {
		config.TLSCAPath = state.MySQL.TLSCAPath
	} else if certificate != "" {
		path, remove, err := r.writeCACertificate(certificate, persistentCA)
		if err != nil {
			return dbmysql.Config{}, cleanup, err
		}
		config.TLSCAPath = path
		cleanup = remove
	}
	if err := config.Validate(); err != nil {
		cleanup()
		return dbmysql.Config{}, func() {}, errors.Join(ErrInvalidRequest, err)
	}
	return config, cleanup, nil
}

func splitAddress(address string) (string, int, error) {
	address = strings.TrimSpace(address)
	if host, portText, err := net.SplitHostPort(address); err == nil {
		port, parseErr := strconv.Atoi(portText)
		if parseErr != nil {
			return "", 0, errors.Join(ErrInvalidRequest, parseErr)
		}
		return strings.Trim(host, "[]"), port, nil
	}
	if strings.HasPrefix(address, "[") && strings.HasSuffix(address, "]") {
		return strings.Trim(address, "[]"), 3306, nil
	}
	if strings.Count(address, ":") == 0 {
		return address, 3306, nil
	}
	return "", 0, errors.Join(ErrInvalidRequest, errors.New("invalid mysql address"))
}

func sameMySQLIdentity(config dbmysql.Config, host string, port int, databaseName, username string) bool {
	return strings.EqualFold(strings.TrimSpace(config.Host), strings.TrimSpace(host)) && config.Port == port &&
		config.Database == strings.TrimSpace(databaseName) && config.Username == strings.TrimSpace(username)
}

func (r *Runtime) writeCACertificate(certificate string, persistent bool) (string, func(), error) {
	if err := os.MkdirAll(r.dataDir, 0o700); err != nil {
		return "", func() {}, err
	}
	if !persistent {
		file, err := os.CreateTemp(r.dataDir, ".mysql-ca-*.pem")
		if err != nil {
			return "", func() {}, err
		}
		path := file.Name()
		if err := file.Chmod(0o600); err == nil {
			_, err = file.WriteString(certificate)
		}
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			_ = os.Remove(path)
			return "", func() {}, errors.Join(err, closeErr)
		}
		return path, func() { _ = os.Remove(path) }, nil
	}
	digest := sha256.Sum256([]byte(certificate))
	path := filepath.Join(r.dataDir, "mysql-ca-"+hex.EncodeToString(digest[:8])+".pem")
	if existing, readErr := os.ReadFile(path); readErr == nil {
		if string(existing) != certificate {
			return "", func() {}, errors.New("mysql CA content-address collision")
		}
		return path, func() {}, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return "", func() {}, readErr
	}
	temporary, err := os.CreateTemp(r.dataDir, ".mysql-ca-*.tmp")
	if err != nil {
		return "", func() {}, err
	}
	temporaryPath := temporary.Name()
	cleanup := func() { _ = os.Remove(temporaryPath) }
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.WriteString(certificate)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil || closeErr != nil {
		cleanup()
		return "", func() {}, errors.Join(err, closeErr)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		cleanup()
		return "", func() {}, err
	}
	_ = os.Chmod(path, 0o600)
	return path, func() {}, nil
}

func copyConfiguration(ctx context.Context, source, target *sql.DB, manifest schema.Manifest) error {
	if source == nil || target == nil {
		return errors.New("both sqlite and mysql must be available")
	}
	configuration := map[string]bool{
		"settings": true, "model_prices": true, "model_price_context_tiers": true,
		"model_price_service_tiers": true, "api_key_aliases": true,
	}
	for _, table := range manifest.AuthoritativeTables() {
		if !configuration[table.Name] {
			continue
		}
		if err := copyWholeTable(ctx, source, target, table); err != nil {
			return err
		}
	}
	return nil
}

func copyWholeTable(ctx context.Context, source, target *sql.DB, table schema.Table) error {
	columns := make([]string, len(table.Columns))
	quoted := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		columns[index] = column.Name
		quoted[index] = mysqlQuote(column.Name)
	}
	rows, err := source.QueryContext(ctx, "SELECT "+strings.Join(quoted, ",")+" FROM "+sqliteQuote(table.Name))
	if err != nil {
		return fmt.Errorf("read %s: %w", table.Name, err)
	}
	defer rows.Close()
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	updates := make([]string, len(columns))
	for index, column := range columns {
		updates[index] = mysqlQuote(column) + "=VALUES(" + mysqlQuote(column) + ")"
	}
	statement := "INSERT INTO " + mysqlQuote(table.Name) + " (" + strings.Join(quoted, ",") + ") VALUES (" + placeholders + ") ON DUPLICATE KEY UPDATE " + strings.Join(updates, ",")
	prepared, err := tx.PrepareContext(ctx, statement)
	if err != nil {
		return err
	}
	defer prepared.Close()
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return err
		}
		if err := validateRowValues(table, values); err != nil {
			return err
		}
		if _, err := prepared.ExecContext(ctx, values...); err != nil {
			return fmt.Errorf("write %s: %w", table.Name, err)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

func validateRowValues(table schema.Table, values []any) error {
	for index, value := range values {
		column := table.Columns[index]
		if value == nil {
			if !column.Nullable {
				return fmt.Errorf("%s.%s unexpectedly contains NULL", table.Name, column.Name)
			}
			continue
		}
		switch typed := value.(type) {
		case string:
			if !utf8.ValidString(typed) {
				return fmt.Errorf("%s.%s contains invalid UTF-8", table.Name, column.Name)
			}
		case []byte:
			if column.Kind == schema.KindText && !utf8.Valid(typed) {
				return fmt.Errorf("%s.%s contains invalid UTF-8", table.Name, column.Name)
			}
		case float64:
			if math.IsNaN(typed) || math.IsInf(typed, 0) {
				return fmt.Errorf("%s.%s contains a non-finite floating value", table.Name, column.Name)
			}
		case int64, bool, time.Time:
		default:
			return fmt.Errorf("%s.%s has incompatible dynamic type %T", table.Name, column.Name, value)
		}
	}
	return nil
}

func configurationPriceSnapshot(ctx context.Context, db *sql.DB) (string, json.RawMessage, error) {
	if db == nil {
		return "", nil, errors.New("sqlite is unavailable")
	}
	prices, err := modelprice.New(db).LoadAll(ctx)
	if err != nil {
		return "", nil, err
	}
	encoded, err := json.Marshal(prices)
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), encoded, nil
}

func sqliteQuote(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func mysqlQuote(identifier string) string {
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
}
