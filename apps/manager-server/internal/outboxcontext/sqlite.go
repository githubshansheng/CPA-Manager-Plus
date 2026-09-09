package outboxcontext

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	modernsqlite "modernc.org/sqlite"
)

const (
	contextTable       = "database_authority_write_context"
	contextLifetime    = 15 * time.Minute
	providerContractID = "cpamp-authority-context-v1"
)

var registerSQLiteFunctionsOnce sync.Once

func init() { registerSQLiteFunctions() }

// SQLiteProvider binds SQLite journal triggers to the context established by
// Begin. It must be passed as a pointer because the journal installer supplies
// table/operation metadata separately from DigestExpression.
type SQLiteProvider struct {
	SchemaVersion int
	mu            sync.Mutex
	table         string
	operation     databasemigration.Operation
}

func (p *SQLiteProvider) ContractID() string {
	return providerContractID + ":schema-" + strconv.Itoa(p.schemaVersion())
}

func (*SQLiteProvider) TransactionIDExpression() string {
	return activeContextExpression("transaction_id", "''")
}

func (*SQLiteProvider) MutationIDExpression() string {
	return "cpamp_mutation_id(" + activeContextExpression("transaction_id", "''") + ", " + sequenceExpression() + ")"
}

func (*SQLiteProvider) SequenceExpression() string { return sequenceExpression() }

func (*SQLiteProvider) EpochExpression() string {
	return activeContextExpression("source_epoch", "-1")
}

func (p *SQLiteProvider) RowVersionExpression(table string, operation databasemigration.Operation, _ string) string {
	p.mu.Lock()
	p.table = table
	p.operation = operation
	p.mu.Unlock()
	return "(" + activeContextExpression("row_version_base", "-1") + " + " + sequenceExpression() + ")"
}

func (*SQLiteProvider) NowMSExpression() string {
	return "CAST(unixepoch('subsec') * 1000 AS INTEGER)"
}

func (p *SQLiteProvider) DigestExpression(envelope string) string {
	p.mu.Lock()
	table, operation := p.table, p.operation
	p.mu.Unlock()
	return fmt.Sprintf("cpamp_mutation_digest(%s.mutation_id,%s.transaction_id,%s.sequence_no,'sqlite','mysql',%s.source_epoch,%s,%s,%s.primary_key_json,%s.payload_json,%d,%s.row_version)",
		envelope, envelope, envelope, envelope, quoteString(table), quoteString(string(operation)),
		envelope, envelope, p.schemaVersion(), envelope)
}

func (p *SQLiteProvider) schemaVersion() int {
	if p.SchemaVersion > 0 {
		return p.SchemaVersion
	}
	return 1
}

func activeContextExpression(column, fallback string) string {
	return fmt.Sprintf("COALESCE((SELECT %s FROM %s WHERE id=1 AND enabled=1 AND active=1 AND expires_at_ms >= CAST(unixepoch('subsec') * 1000 AS INTEGER)),%s)",
		column, contextTable, fallback)
}

func sequenceExpression() string {
	txID := activeContextExpression("transaction_id", "''")
	return fmt.Sprintf("COALESCE((SELECT MAX(sequence_no)+1 FROM database_outbox WHERE transaction_id=%s),0)", txID)
}

func quoteString(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

// Ensure creates the inert context row. It does not enable journaling.
func Ensure(ctx context.Context, db *sql.DB) error {
	if err := Prepare(ctx, db); err != nil {
		return err
	}
	return Detect(ctx, db)
}

// Prepare creates or upgrades only the inert local context state. It is safe
// to call before application schema migration because it never activates or
// assumes that journal metadata already exists.
func Prepare(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("authority context requires a database")
	}
	registerSQLiteFunctions()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS database_authority_write_context (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
			active INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0, 1)),
			transaction_id TEXT,
			source_epoch INTEGER NOT NULL DEFAULT 0,
			started_at_ms INTEGER NOT NULL DEFAULT 0,
			expires_at_ms INTEGER NOT NULL DEFAULT 0,
			row_version_base INTEGER NOT NULL DEFAULT 0,
			last_row_version INTEGER NOT NULL DEFAULT 0,
			journal_suspended INTEGER NOT NULL DEFAULT 0 CHECK (journal_suspended IN (0, 1))
		)`,
		`INSERT OR IGNORE INTO database_authority_write_context
			(id, enabled, active, source_epoch, started_at_ms, expires_at_ms, row_version_base, last_row_version)
			VALUES (1, 0, 0, 0, 0, 0, 0, 0)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("ensure authority transaction context: %w", err)
		}
	}
	var suspendedColumn int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('database_authority_write_context')
		WHERE name='journal_suspended'`).Scan(&suspendedColumn); err != nil {
		return err
	}
	if suspendedColumn == 0 {
		if _, err := db.ExecContext(ctx, `ALTER TABLE database_authority_write_context
			ADD COLUMN journal_suspended INTEGER NOT NULL DEFAULT 0 CHECK (journal_suspended IN (0, 1))`); err != nil {
			return fmt.Errorf("upgrade authority transaction context: %w", err)
		}
	}
	return nil
}

// SuspendForSchemaMigration removes journal triggers under a durable marker so
// startup schema upgrades can never be mistaken for application transactions.
// ResumeAfterSchemaMigration must run after a successful migration. A crash
// leaves journal_suspended=1, causing the next startup to retry restoration.
func SuspendForSchemaMigration(ctx context.Context, db *sql.DB) (bool, error) {
	if err := Prepare(ctx, db); err != nil {
		return false, err
	}
	var suspended int
	if err := db.QueryRowContext(ctx, `SELECT journal_suspended FROM database_authority_write_context WHERE id=1`).Scan(&suspended); err != nil {
		return false, err
	}
	var contractTable, contracts, triggers int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='database_journal_contracts'`).Scan(&contractTable); err != nil {
		return false, err
	}
	if contractTable != 0 {
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM database_journal_contracts`).Scan(&contracts); err != nil {
			return false, err
		}
	}
	for _, table := range schema.Current().AuthoritativeTables() {
		for _, trigger := range databasemigration.JournalTriggerNames(table.Name) {
			var count int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&count); err != nil {
				return false, err
			}
			triggers += count
		}
	}
	if suspended == 0 && contracts == 0 && triggers == 0 {
		return false, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, table := range schema.Current().AuthoritativeTables() {
		for _, trigger := range databasemigration.JournalTriggerNames(table.Name) {
			if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS "`+trigger+`"`); err != nil {
				return false, fmt.Errorf("suspend journal trigger %s: %w", trigger, err)
			}
		}
	}
	if contractTable != 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM database_journal_contracts`); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE database_authority_write_context SET
		journal_suspended=1, enabled=0, active=0, transaction_id=NULL, source_epoch=0,
		started_at_ms=0, expires_at_ms=0, row_version_base=0 WHERE id=1`); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	stateFor(db).enabled.Store(false)
	return true, nil
}

// ResumeAfterSchemaMigration reinstalls the complete canonical contract after
// SuspendForSchemaMigration. SQLite authority is reactivated only while SQLite
// remains the write primary; a MySQL-primary replica keeps the same fencing
// triggers installed with its local write context disabled.
func ResumeAfterSchemaMigration(ctx context.Context, db *sql.DB) error {
	if err := Prepare(ctx, db); err != nil {
		return err
	}
	var suspended int
	if err := db.QueryRowContext(ctx, `SELECT journal_suspended FROM database_authority_write_context WHERE id=1`).Scan(&suspended); err != nil {
		return err
	}
	if suspended == 0 {
		return Ensure(ctx, db)
	}
	var primary string
	var epoch int64
	if err := db.QueryRowContext(ctx, `SELECT write_primary, epoch FROM database_routing_state WHERE id=1`).Scan(&primary, &epoch); err != nil {
		return fmt.Errorf("restore journal routing fence: %w", err)
	}
	if (primary != "sqlite" && primary != "mysql") || epoch <= 0 {
		return fmt.Errorf("%w: cannot restore journal for %s epoch %d", ErrJournalNotReady, primary, epoch)
	}
	manifest := canonicalMigrationManifest()
	provider := &SQLiteProvider{SchemaVersion: schema.Current().Version}
	installer := databasemigration.SQLiteJournalInstaller{DB: db, Manifest: manifest,
		Provider: provider, SchemaVersion: schema.Current().Version}
	if err := installer.Install(ctx); err != nil {
		return fmt.Errorf("restore authoritative journal: %w", err)
	}
	if primary == "sqlite" {
		if err := Enable(ctx, db, epoch); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `UPDATE database_authority_write_context SET journal_suspended=0 WHERE id=1`); err != nil {
			return err
		}
		return nil
	}
	if _, err := db.ExecContext(ctx, `UPDATE database_authority_write_context SET
		journal_suspended=0, enabled=0, active=0, transaction_id=NULL, source_epoch=0,
		started_at_ms=0, expires_at_ms=0, row_version_base=0 WHERE id=1`); err != nil {
		return fmt.Errorf("restore sqlite replica journal fence: %w", err)
	}
	state := stateFor(db)
	state.enabled.Store(false)
	state.epoch.Store(0)
	return nil
}

// Enable performs a strict readiness audit before repository writes may use
// the installed triggers. A partially instrumented schema is never activated.
func Enable(ctx context.Context, db *sql.DB, epoch int64) error {
	if epoch <= 0 {
		return errors.New("authority context epoch must be positive")
	}
	if err := Ensure(ctx, db); err != nil && !errors.Is(err, ErrJournalNotReady) {
		return err
	}
	state := stateFor(db)
	// Detect may have restored a prior contract. Keep the process fail-closed
	// until this caller's complete audit and expected epoch both succeed.
	state.enabled.Store(false)
	if err := Audit(ctx, db); err != nil {
		return err
	}
	var writePrimary string
	var actualEpoch int64
	if err := db.QueryRowContext(ctx, `SELECT write_primary, epoch FROM database_routing_state WHERE id = 1`).Scan(&writePrimary, &actualEpoch); err != nil {
		return fmt.Errorf("read sqlite routing fence: %w", err)
	}
	if writePrimary != "sqlite" || actualEpoch != epoch {
		return fmt.Errorf("sqlite routing fence is %s epoch %d, want sqlite epoch %d", writePrimary, actualEpoch, epoch)
	}
	if _, err := db.ExecContext(ctx, `UPDATE database_authority_write_context SET
		enabled=1, active=0, transaction_id=NULL, source_epoch=0, started_at_ms=0,
		expires_at_ms=0, row_version_base=0 WHERE id=1`); err != nil {
		return fmt.Errorf("enable authority transaction context: %w", err)
	}
	state.epoch.Store(epoch)
	state.enabled.Store(true)
	return nil
}

// DisableSQLite closes the process and durable authority context without
// removing its complete trigger contract. Callers must hold the global write
// fence; installed triggers continue to reject context-free writes while the
// routing row is moved to another primary and remain available for reverse
// Inbox application.
func DisableSQLite(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("disable sqlite authority: database is required")
	}
	state := stateFor(db)
	state.gateMu.Lock()
	active := state.active
	blocked := state.blocked
	state.gateMu.Unlock()
	if !blocked || active != 0 {
		return errors.New("disable sqlite authority requires an idle global write fence")
	}
	if _, err := db.ExecContext(ctx, `UPDATE database_authority_write_context SET
		enabled=0,active=0,transaction_id=NULL,source_epoch=0,started_at_ms=0,
		expires_at_ms=0,row_version_base=0 WHERE id=1`); err != nil {
		return fmt.Errorf("disable sqlite authority context: %w", err)
	}
	state.enabled.Store(false)
	state.epoch.Store(0)
	return nil
}

var ErrJournalNotReady = errors.New("authoritative sqlite journal is not ready")

// ErrReplicationWatermarkChanged is returned when local cache maintenance can
// no longer prove that every authoritative mutation which existed at
// validation time has reached the target. Callers must stop the batch and run
// the normal replication/validation gates again.
var ErrReplicationWatermarkChanged = errors.New("sqlite replication watermark changed during cache maintenance")

// Audit proves that every canonical authoritative table has its persisted
// contract and all four INSERT/DELETE/UPDATE triggers.
func Audit(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("journal audit requires a database")
	}
	var missing []string
	for _, table := range schema.Current().AuthoritativeTables() {
		var contracts int
		err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM database_journal_contracts WHERE table_name=?`, table.Name).Scan(&contracts)
		if err != nil {
			return fmt.Errorf("inspect journal contract for %s: %w", table.Name, err)
		}
		if contracts != 1 {
			missing = append(missing, table.Name+":contract")
		}
		for _, trigger := range databasemigration.JournalTriggerNames(table.Name) {
			var count int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				missing = append(missing, table.Name+":"+trigger)
			}
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("%w: missing %s", ErrJournalNotReady, strings.Join(missing, ", "))
	}
	return nil
}

// Detect reactivates an already-installed journal after a process restart. An
// incomplete installation is reported and remains disabled.
func Detect(ctx context.Context, db *sql.DB) error {
	var suspended int
	if err := db.QueryRowContext(ctx, `SELECT journal_suspended FROM database_authority_write_context WHERE id=1`).Scan(&suspended); err != nil {
		return err
	}
	if suspended != 0 {
		return fmt.Errorf("%w: journal restoration is pending", ErrJournalNotReady)
	}
	var contractTable int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='database_journal_contracts'`).Scan(&contractTable); err != nil {
		return err
	}
	if contractTable == 0 {
		return nil
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM database_journal_contracts`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	if err := Audit(ctx, db); err != nil {
		return err
	}
	var primary string
	var epoch int64
	if err := db.QueryRowContext(ctx, `SELECT write_primary, epoch FROM database_routing_state WHERE id=1`).Scan(&primary, &epoch); err != nil {
		return err
	}
	if primary != "sqlite" || epoch <= 0 {
		return fmt.Errorf("%w: installed sqlite journal has incompatible routing fence", ErrJournalNotReady)
	}
	if _, err := db.ExecContext(ctx, `UPDATE database_authority_write_context SET enabled=1,
		active=0, transaction_id=NULL, source_epoch=0, started_at_ms=0, expires_at_ms=0,
		row_version_base=0 WHERE id=1`); err != nil {
		return err
	}
	state := stateFor(db)
	state.epoch.Store(epoch)
	state.enabled.Store(true)
	return nil
}

func canonicalMigrationManifest() databasemigration.StaticManifest {
	current := schema.Current()
	authoritative := current.AuthoritativeTables()
	known := make(map[string]bool, len(authoritative))
	for _, table := range authoritative {
		known[table.Name] = true
	}
	result := make(databasemigration.StaticManifest, 0, len(authoritative))
	for _, table := range authoritative {
		spec := databasemigration.TableSpec{Name: table.Name}
		for _, column := range table.Columns {
			nullable := column.Nullable && column.PrimaryKeyPosition == 0
			spec.Columns = append(spec.Columns, databasemigration.ColumnSpec{Name: column.Name,
				Nullable: nullable, LogicalType: string(column.Kind), DefaultSQL: column.Default})
			if column.PrimaryKeyPosition > 0 {
				spec.PrimaryKey = append(spec.PrimaryKey, column.Name)
			}
		}
		dependencies := map[string]bool{}
		for _, foreignKey := range table.ForeignKeys {
			if foreignKey.RefTable != table.Name && known[foreignKey.RefTable] && !dependencies[foreignKey.RefTable] {
				spec.Dependencies = append(spec.Dependencies, foreignKey.RefTable)
				dependencies[foreignKey.RefTable] = true
			}
		}
		result = append(result, spec)
	}
	return result
}

type dbState struct {
	enabled atomic.Bool
	epoch   atomic.Int64
	backend atomic.Int32
	gateMu  sync.Mutex
	active  int
	blocked bool
	changed chan struct{}
}

var states sync.Map

func stateFor(db *sql.DB) *dbState {
	value, _ := states.LoadOrStore(db, newDBState())
	return value.(*dbState)
}

func newDBState() *dbState { return &dbState{changed: make(chan struct{})} }

func (state *dbState) notifyLocked() {
	close(state.changed)
	state.changed = make(chan struct{})
}

func (state *dbState) begin(ctx context.Context) error {
	for {
		state.gateMu.Lock()
		if !state.blocked {
			state.active++
			state.gateMu.Unlock()
			return nil
		}
		changed := state.changed
		state.gateMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (state *dbState) end() {
	state.gateMu.Lock()
	if state.active <= 0 {
		state.gateMu.Unlock()
		panic("outboxcontext: unbalanced authority transaction activity")
	}
	state.active--
	state.notifyLocked()
	state.gateMu.Unlock()
}

// AcquireWriteFence blocks new authoritative Begin/Exec calls and waits for
// every already-started authoritative transaction to finish. The returned
// release is safe to call more than once.
func AcquireWriteFence(ctx context.Context, db *sql.DB) (func(), error) {
	if db == nil {
		return nil, errors.New("write fence requires a database")
	}
	state := stateFor(db)
	for {
		state.gateMu.Lock()
		if !state.blocked {
			state.blocked = true
			state.notifyLocked()
			for state.active > 0 {
				changed := state.changed
				state.gateMu.Unlock()
				select {
				case <-ctx.Done():
					state.gateMu.Lock()
					state.blocked = false
					state.notifyLocked()
					state.gateMu.Unlock()
					return nil, ctx.Err()
				case <-changed:
				}
				state.gateMu.Lock()
			}
			state.gateMu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					state.gateMu.Lock()
					state.blocked = false
					state.notifyLocked()
					state.gateMu.Unlock()
				})
			}, nil
		}
		changed := state.changed
		state.gateMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// AcquireWriteOperation joins the same process-local gate used by authority
// transactions without opening a SQL transaction. Derived-state workers use
// it so final validation, cutover and cache replacement can pause all database
// mutations, including tables that intentionally do not emit Outbox rows.
func AcquireWriteOperation(ctx context.Context, db *sql.DB) (func(), error) {
	if db == nil {
		return nil, errors.New("write operation requires a database")
	}
	state := stateFor(db)
	if err := state.begin(ctx); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(state.end) }, nil
}

type Tx struct {
	*sql.Tx
	journal bool
	closed  bool
	state   *dbState
	backend authorityBackend
}

// LocalCacheMaintenance is a deliberately narrow transaction for pruning the
// SQLite recent-data cache. Journal triggers are removed and restored inside
// the same SQLite transaction, so local DELETEs (including FK cascades) cannot
// erase MySQL history through the Outbox. Callers cannot issue INSERT/UPDATE
// through this type.
type LocalCacheMaintenance struct {
	tx         *sql.Tx
	state      *dbState
	triggerSQL []string
	closed     bool
}

// ReplicaApply is the only write-capable journal-suppressed SQLite surface.
// It exists for MySQL->SQLite Inbox application after an administrator has
// fenced MySQL as the write primary. Trigger DDL, authoritative mutations,
// Inbox records and row-version tombstones share one SQLite transaction.
type ReplicaApply struct {
	maintenance *LocalCacheMaintenance
}

// UsageCleanupPreparation reports bounded derived work that must finish before
// an old usage_events row can be removed without exposing stale selector data.
type UsageCleanupPreparation struct {
	DeletedSelectorRows int64
	HasCandidates       bool
	Ready               bool
}

func BeginLocalCacheMaintenance(ctx context.Context, db *sql.DB) (*LocalCacheMaintenance, error) {
	return beginJournalSuppressedSQLite(ctx, db, true)
}

// BeginReplicaApply may run while SQLite authority journaling is disabled.
// The complete trigger contract must still be installed so rollback or commit
// can never leave the local cache without its fencing triggers.
func BeginReplicaApply(ctx context.Context, db *sql.DB) (*ReplicaApply, error) {
	maintenance, err := beginJournalSuppressedSQLite(ctx, db, false)
	if err != nil {
		return nil, err
	}
	return &ReplicaApply{maintenance: maintenance}, nil
}

func beginJournalSuppressedSQLite(
	ctx context.Context,
	db *sql.DB,
	requireAuthorityEnabled bool,
) (*LocalCacheMaintenance, error) {
	if db == nil {
		return nil, errors.New("local cache maintenance requires a database")
	}
	state := stateFor(db)
	if requireAuthorityEnabled && !state.enabled.Load() {
		return nil, ErrJournalNotReady
	}
	if err := state.begin(ctx); err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		state.end()
		return nil, err
	}
	maintenance := &LocalCacheMaintenance{tx: tx, state: state}
	for _, table := range schema.Current().AuthoritativeTables() {
		for _, trigger := range databasemigration.JournalTriggerNames(table.Name) {
			var statement string
			if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&statement); err != nil {
				tx.Rollback()
				state.end()
				return nil, fmt.Errorf("%w: cache maintenance trigger %s: %v", ErrJournalNotReady, trigger, err)
			}
			maintenance.triggerSQL = append(maintenance.triggerSQL, statement)
			if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+trigger+`"`); err != nil {
				tx.Rollback()
				state.end()
				return nil, fmt.Errorf("drop cache maintenance trigger %s: %w", trigger, err)
			}
		}
	}
	return maintenance, nil
}

func (apply *ReplicaApply) ApplyInboxGroup(
	ctx context.Context,
	repository *databasemigration.SQLRepository,
	group databasemigration.MutationGroup,
	applier databasemigration.MutationApplier,
) (databasemigration.InboxApplyResult, error) {
	if apply == nil || apply.maintenance == nil || apply.maintenance.tx == nil || apply.maintenance.closed {
		return databasemigration.InboxApplyResult{}, sql.ErrTxDone
	}
	if repository == nil {
		return databasemigration.InboxApplyResult{}, errors.New("replica apply requires an Inbox repository")
	}
	return repository.ApplyInboxGroupTx(ctx, apply.maintenance.tx, group, applier)
}

func (apply *ReplicaApply) RefreshCacheCoverage(
	ctx context.Context,
	watermark int64,
) (databasemigration.CacheCoverage, error) {
	if apply == nil || apply.maintenance == nil {
		return databasemigration.CacheCoverage{}, sql.ErrTxDone
	}
	return apply.maintenance.RefreshCacheCoverage(ctx, watermark, false)
}

func (apply *ReplicaApply) Commit() error {
	if apply == nil || apply.maintenance == nil {
		return sql.ErrTxDone
	}
	return apply.maintenance.Commit()
}

func (apply *ReplicaApply) Rollback() error {
	if apply == nil || apply.maintenance == nil {
		return sql.ErrTxDone
	}
	return apply.maintenance.Rollback()
}

// DeleteWhere deletes only from a canonical authoritative table. The caller
// supplies a parameterized predicate without comments or additional SQL.
func (maintenance *LocalCacheMaintenance) DeleteWhere(
	ctx context.Context,
	table string,
	predicate string,
	args ...any,
) (sql.Result, error) {
	if maintenance == nil || maintenance.tx == nil || maintenance.closed {
		return nil, sql.ErrTxDone
	}
	if !cacheMaintenanceTable(table) {
		return nil, fmt.Errorf("local cache maintenance table %q is not eligible business cache data", table)
	}
	predicate = strings.TrimSpace(predicate)
	if !safeDeletePredicate(predicate) {
		return nil, errors.New("local cache maintenance requires one safe delete predicate")
	}
	return maintenance.tx.ExecContext(ctx, `DELETE FROM "`+table+`" WHERE `+predicate, args...)
}

// DeleteDerivedWhere removes the cache-window projection corresponding to an
// authoritative delete. Only canonical SQLite-derived tables are accepted.
func (maintenance *LocalCacheMaintenance) DeleteDerivedWhere(
	ctx context.Context,
	table string,
	predicate string,
	args ...any,
) (sql.Result, error) {
	if maintenance == nil || maintenance.tx == nil || maintenance.closed {
		return nil, sql.ErrTxDone
	}
	if !derivedSQLiteTable(table) {
		return nil, fmt.Errorf("local cache maintenance table %q is not derived SQLite data", table)
	}
	predicate = strings.TrimSpace(predicate)
	if !safeDeletePredicate(predicate) {
		return nil, errors.New("local cache maintenance requires one safe delete predicate")
	}
	return maintenance.tx.ExecContext(ctx, `DELETE FROM "`+table+`" WHERE `+predicate, args...)
}

// SaveCacheCoverage atomically publishes coverage for the authoritative and
// derived rows deleted by this maintenance transaction. The internal row ID
// is fixed to one and no arbitrary internal SQL is exposed.
func (maintenance *LocalCacheMaintenance) SaveCacheCoverage(
	ctx context.Context,
	coverage databasemigration.CacheCoverage,
) error {
	if maintenance == nil || maintenance.tx == nil || maintenance.closed {
		return sql.ErrTxDone
	}
	if coverage.EarliestAtMS < 0 || coverage.LatestAtMS < 0 || coverage.EarliestID < 0 ||
		coverage.LatestID < 0 || coverage.Watermark < 0 || coverage.UpdatedAtMS < 0 ||
		coverage.EarliestAtMS > 0 && coverage.LatestAtMS > 0 && coverage.EarliestAtMS > coverage.LatestAtMS ||
		coverage.EarliestID > 0 && coverage.LatestID > 0 && coverage.EarliestID > coverage.LatestID {
		return errors.New("local cache coverage is invalid")
	}
	complete := 0
	if coverage.Complete {
		complete = 1
	}
	_, err := maintenance.tx.ExecContext(ctx, `INSERT INTO database_cache_coverage
		(id,earliest_at_ms,latest_at_ms,earliest_id,latest_id,watermark,complete,updated_at_ms)
		VALUES(1,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET earliest_at_ms=excluded.earliest_at_ms,
		latest_at_ms=excluded.latest_at_ms,earliest_id=excluded.earliest_id,
		latest_id=excluded.latest_id,watermark=excluded.watermark,
		complete=excluded.complete,updated_at_ms=excluded.updated_at_ms`,
		coverage.EarliestAtMS, coverage.LatestAtMS, coverage.EarliestID, coverage.LatestID,
		coverage.Watermark, complete, coverage.UpdatedAtMS)
	return err
}

// AssertReplicationWatermark rechecks the cleanup safety boundary inside the
// same SQLite transaction which performs the deletes. It rejects pending
// mutations and any source watermark which advanced beyond the externally
// validated watermark.
func (maintenance *LocalCacheMaintenance) AssertReplicationWatermark(
	ctx context.Context,
	validatedWatermark int64,
) error {
	if maintenance == nil || maintenance.tx == nil || maintenance.closed {
		return sql.ErrTxDone
	}
	if validatedWatermark < 0 {
		return errors.New("validated replication watermark cannot be negative")
	}
	var pending, sourceWatermark int64
	if err := maintenance.tx.QueryRowContext(ctx, `SELECT
		COUNT(CASE WHEN applied_at_ms = 0 THEN 1 END),
		COALESCE(MAX(outbox_id), 0)
		FROM database_outbox`).Scan(&pending, &sourceWatermark); err != nil {
		return fmt.Errorf("inspect cache maintenance replication watermark: %w", err)
	}
	if pending != 0 || sourceWatermark > validatedWatermark {
		return fmt.Errorf("%w: pending=%d source=%d validated=%d",
			ErrReplicationWatermarkChanged, pending, sourceWatermark, validatedWatermark)
	}
	return nil
}

// PrepareUsageEventCleanup performs the bounded selector cleanup required
// before pruning raw usage rows. While selector rows are being cleared the
// metadata schema version is deliberately unavailable, forcing readers onto
// the authoritative raw path. No raw event is deleted by this method.
func (maintenance *LocalCacheMaintenance) PrepareUsageEventCleanup(
	ctx context.Context,
	cutoffMS int64,
	limit int,
) (UsageCleanupPreparation, error) {
	if maintenance == nil || maintenance.tx == nil || maintenance.closed {
		return UsageCleanupPreparation{}, sql.ErrTxDone
	}
	if cutoffMS <= 0 || limit <= 0 {
		return UsageCleanupPreparation{}, errors.New("usage cache cleanup requires positive cutoff and limit")
	}
	var migrationStatus string
	if err := maintenance.tx.QueryRowContext(ctx, `SELECT status FROM usage_data_migrations
		WHERE name='usage_cache_accounting_v2'`).Scan(&migrationStatus); err != nil {
		return UsageCleanupPreparation{}, fmt.Errorf("read usage accounting migration state: %w", err)
	}
	if migrationStatus != "completed" && migrationStatus != "clearing" {
		return UsageCleanupPreparation{}, fmt.Errorf(
			"usage cache cleanup requires stable accounting derivations, found %q", migrationStatus)
	}
	var hasCandidates int
	if err := maintenance.tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM usage_events WHERE timestamp_ms < ? LIMIT 1
	)`, cutoffMS).Scan(&hasCandidates); err != nil {
		return UsageCleanupPreparation{}, fmt.Errorf("inspect usage cleanup candidates: %w", err)
	}
	if hasCandidates == 0 {
		return UsageCleanupPreparation{Ready: true}, nil
	}

	result, err := maintenance.tx.ExecContext(ctx, `UPDATE usage_monitoring_rollup_state SET
		schema_version=0, structure_revision='', status='rebuilding',
		backfill_last_event_id=0, coverage_event_id=0,
		target_event_id=(SELECT COALESCE(MAX(id),0) FROM usage_events),
		processed_events=0, finished_at_ms=NULL, last_error=NULL
		WHERE rollup_name='metadata_v1'`)
	if err := requireOneCacheState(result, err, "usage metadata projection"); err != nil {
		return UsageCleanupPreparation{}, fmt.Errorf("mark usage metadata projection unavailable: %w", err)
	}

	const dayMS int64 = 24 * 60 * 60 * 1000
	selectorBoundary := cutoffMS - cutoffMS%dayMS
	if cutoffMS%dayMS != 0 {
		selectorBoundary += dayMS
	}
	result, err = maintenance.tx.ExecContext(ctx, `DELETE FROM usage_monitoring_selector_daily_rollups_v1
		WHERE rowid IN (SELECT rowid FROM usage_monitoring_selector_daily_rollups_v1
			WHERE bucket_ms < ? ORDER BY bucket_ms, rowid LIMIT ?)`, selectorBoundary, limit)
	if err != nil {
		return UsageCleanupPreparation{}, fmt.Errorf("clear usage selector cache window: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return UsageCleanupPreparation{}, fmt.Errorf("count cleared usage selector rows: %w", err)
	}
	var remaining int
	if err := maintenance.tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM usage_monitoring_selector_daily_rollups_v1 WHERE bucket_ms < ? LIMIT 1
	)`, selectorBoundary).Scan(&remaining); err != nil {
		return UsageCleanupPreparation{}, fmt.Errorf("inspect usage selector cache window: %w", err)
	}
	return UsageCleanupPreparation{DeletedSelectorRows: deleted, HasCandidates: true, Ready: remaining == 0}, nil
}

// MarkUsageDerivedDirty fences every aggregate which can contain contributions
// from pruned raw events. Readers already interpret these states as raw
// fallbacks. The metadata projection is restored only after the raw cutoff is
// fully clear; its worker then rebuilds from retained events.
func (maintenance *LocalCacheMaintenance) MarkUsageDerivedDirty(
	ctx context.Context,
	cutoffMS int64,
) (bool, error) {
	if maintenance == nil || maintenance.tx == nil || maintenance.closed {
		return false, sql.ErrTxDone
	}
	if cutoffMS <= 0 {
		return false, errors.New("usage cache cleanup requires a positive cutoff")
	}
	var latestID int64
	var remaining int
	if err := maintenance.tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0),
		EXISTS(SELECT 1 FROM usage_events WHERE timestamp_ms < ? LIMIT 1)
		FROM usage_events`, cutoffMS).Scan(&latestID, &remaining); err != nil {
		return false, fmt.Errorf("inspect retained usage cache: %w", err)
	}
	if _, err := maintenance.tx.ExecContext(ctx, `DELETE FROM usage_cache_accounting_v2_changes`); err != nil {
		return false, fmt.Errorf("clear staged usage accounting changes: %w", err)
	}
	result, err := maintenance.tx.ExecContext(ctx, `UPDATE usage_data_migrations SET
		status='clearing', last_event_id=?, target_event_id=?, changed_rows=0, applied_rows=0,
		updated_at_ms=?, finished_at_ms=NULL, last_error=NULL
		WHERE name='usage_cache_accounting_v2'`, latestID, latestID, time.Now().UnixMilli())
	if err := requireOneCacheState(result, err, "usage accounting migration"); err != nil {
		return false, fmt.Errorf("mark usage rollups clearing: %w", err)
	}
	result, err = maintenance.tx.ExecContext(ctx, `UPDATE usage_hourly_aggregate_state SET
		status='clearing', backfill_last_event_id=0, coverage_event_id=0, target_event_id=?,
		processed_events=0, min_bucket_ms=NULL, max_bucket_ms=NULL,
		finished_at_ms=NULL, last_error=NULL WHERE aggregate_name='hourly_core'`, latestID)
	if err := requireOneCacheState(result, err, "usage hourly aggregate"); err != nil {
		return false, fmt.Errorf("mark usage hourly aggregate clearing: %w", err)
	}
	result, err = maintenance.tx.ExecContext(ctx, `UPDATE usage_pricing_rollup_state SET
		status='clearing', backfill_last_event_id=0, coverage_event_id=0, target_event_id=?,
		processed_events=0, min_bucket_ms=NULL, max_bucket_ms=NULL,
		finished_at_ms=NULL, last_error=NULL WHERE rollup_name='pricing_v1'`, latestID)
	if err := requireOneCacheState(result, err, "usage pricing aggregate"); err != nil {
		return false, fmt.Errorf("mark usage pricing aggregate clearing: %w", err)
	}
	result, err = maintenance.tx.ExecContext(ctx, `UPDATE usage_monitoring_rollup_state SET
		status='clearing', backfill_last_event_id=0, coverage_event_id=0, target_event_id=?,
		processed_events=0, finished_at_ms=NULL, last_error=NULL
		WHERE rollup_name='stats_v1'`, latestID)
	if err := requireOneCacheState(result, err, "usage monitoring aggregate"); err != nil {
		return false, fmt.Errorf("mark usage monitoring aggregate clearing: %w", err)
	}
	if remaining == 0 {
		result, err = maintenance.tx.ExecContext(ctx, `UPDATE usage_monitoring_rollup_state SET
			schema_version=COALESCE((SELECT schema_version FROM usage_monitoring_rollup_state
				WHERE rollup_name='projection_v1'),1),
			structure_revision='', status='rebuilding', backfill_last_event_id=0,
			coverage_event_id=0, target_event_id=?, processed_events=0,
			finished_at_ms=NULL, last_error=NULL WHERE rollup_name='metadata_v1'`, latestID)
		if err := requireOneCacheState(result, err, "usage metadata projection"); err != nil {
			return false, fmt.Errorf("schedule usage metadata rebuild: %w", err)
		}
	}
	return remaining == 0, nil
}

func requireOneCacheState(result sql.Result, err error, state string) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%s state row count is %d, want 1", state, affected)
	}
	return nil
}

// RefreshCacheCoverage derives the published range from retained usage_events
// inside the maintenance transaction, avoiding a delete/metadata race.
func (maintenance *LocalCacheMaintenance) RefreshCacheCoverage(
	ctx context.Context,
	watermark int64,
	complete bool,
) (databasemigration.CacheCoverage, error) {
	if maintenance == nil || maintenance.tx == nil || maintenance.closed {
		return databasemigration.CacheCoverage{}, sql.ErrTxDone
	}
	if watermark < 0 {
		return databasemigration.CacheCoverage{}, errors.New("cache coverage watermark cannot be negative")
	}
	coverage := databasemigration.CacheCoverage{Watermark: watermark, Complete: complete,
		UpdatedAtMS: time.Now().UnixMilli()}
	if err := maintenance.tx.QueryRowContext(ctx, `SELECT
		COALESCE(MIN(timestamp_ms),0), COALESCE(MAX(timestamp_ms),0),
		COALESCE(MIN(id),0), COALESCE(MAX(id),0) FROM usage_events`).Scan(
		&coverage.EarliestAtMS, &coverage.LatestAtMS, &coverage.EarliestID, &coverage.LatestID,
	); err != nil {
		return databasemigration.CacheCoverage{}, fmt.Errorf("calculate retained usage cache coverage: %w", err)
	}
	if err := maintenance.SaveCacheCoverage(ctx, coverage); err != nil {
		return databasemigration.CacheCoverage{}, err
	}
	return coverage, nil
}

func (maintenance *LocalCacheMaintenance) Commit() error {
	if maintenance == nil || maintenance.tx == nil || maintenance.closed {
		return sql.ErrTxDone
	}
	maintenance.closed = true
	defer maintenance.state.end()
	for _, statement := range maintenance.triggerSQL {
		if _, err := maintenance.tx.Exec(statement); err != nil {
			_ = maintenance.tx.Rollback()
			return fmt.Errorf("restore cache maintenance journal trigger: %w", err)
		}
	}
	return maintenance.tx.Commit()
}

func (maintenance *LocalCacheMaintenance) Rollback() error {
	if maintenance == nil || maintenance.tx == nil || maintenance.closed {
		return sql.ErrTxDone
	}
	maintenance.closed = true
	defer maintenance.state.end()
	return maintenance.tx.Rollback()
}

func cacheMaintenanceTable(name string) bool {
	switch name {
	case "settings", "model_prices", "model_price_context_tiers", "model_price_service_tiers", "api_key_aliases":
		return false
	}
	for _, table := range schema.Current().AuthoritativeTables() {
		if table.Name == name {
			return true
		}
	}
	return false
}

func derivedSQLiteTable(name string) bool {
	for _, table := range schema.Current().Tables {
		if table.Name == name {
			return table.Class == schema.ClassDerived && !table.MySQLOnly
		}
	}
	return false
}

func safeDeletePredicate(predicate string) bool {
	return predicate != "" && !strings.Contains(predicate, ";") && !strings.Contains(predicate, "--") &&
		!strings.Contains(predicate, "/*") && !strings.Contains(predicate, "*/")
}

func Begin(ctx context.Context, db *sql.DB, options *sql.TxOptions) (*Tx, error) {
	if db == nil {
		return nil, errors.New("authority transaction requires a database")
	}
	state := stateFor(db)
	if err := state.begin(ctx); err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, options)
	if err != nil {
		state.end()
		return nil, err
	}
	if !state.enabled.Load() || options != nil && options.ReadOnly {
		return &Tx{Tx: tx, state: state}, nil
	}
	if authorityBackend(state.backend.Load()) == authorityBackendMySQL {
		return beginMySQLAuthority(ctx, tx, state)
	}
	epoch := state.epoch.Load()
	var actualPrimary string
	var actualEpoch int64
	if err := tx.QueryRowContext(ctx, `SELECT write_primary, epoch FROM database_routing_state WHERE id=1`).Scan(&actualPrimary, &actualEpoch); err != nil {
		tx.Rollback()
		state.end()
		return nil, fmt.Errorf("read authority transaction epoch: %w", err)
	}
	if actualPrimary != "sqlite" || actualEpoch != epoch {
		tx.Rollback()
		state.end()
		return nil, fmt.Errorf("sqlite authority epoch fenced: expected %d, found %s epoch %d", epoch, actualPrimary, actualEpoch)
	}
	groupID, err := randomGroupID()
	if err != nil {
		tx.Rollback()
		state.end()
		return nil, err
	}
	nowMS := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE database_authority_write_context SET
		active=1, transaction_id=?, source_epoch=?, started_at_ms=?, expires_at_ms=?,
		row_version_base=CASE WHEN last_row_version >= ? THEN last_row_version+1 ELSE ? END
		WHERE id=1 AND enabled=1 AND active=0`, groupID, epoch, nowMS,
		nowMS+contextLifetime.Milliseconds(), nowMS*1000, nowMS*1000)
	if err != nil {
		tx.Rollback()
		state.end()
		return nil, fmt.Errorf("establish authority transaction context: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		tx.Rollback()
		state.end()
		return nil, errors.New("authority transaction context is missing or already active")
	}
	return &Tx{Tx: tx, journal: true, state: state, backend: authorityBackendSQLite}, nil
}

func (tx *Tx) Commit() error {
	if tx == nil || tx.Tx == nil || tx.closed {
		return sql.ErrTxDone
	}
	tx.closed = true
	defer tx.state.end()
	if tx.journal && tx.backend == authorityBackendMySQL {
		if err := clearMySQLAuthorityContext(tx.Tx); err != nil {
			_ = tx.Tx.Rollback()
			return err
		}
		return tx.Tx.Commit()
	}
	if tx.journal {
		if _, err := tx.Exec(`UPDATE database_authority_write_context SET
			last_row_version=CASE WHEN COALESCE((SELECT MAX(row_version) FROM database_outbox
				WHERE transaction_id=database_authority_write_context.transaction_id),0) > last_row_version
				THEN (SELECT MAX(row_version) FROM database_outbox
					WHERE transaction_id=database_authority_write_context.transaction_id)
				ELSE last_row_version END,
			active=0, transaction_id=NULL, source_epoch=0, started_at_ms=0,
			expires_at_ms=0, row_version_base=0 WHERE id=1 AND active=1`); err != nil {
			_ = tx.Tx.Rollback()
			return fmt.Errorf("close authority transaction context: %w", err)
		}
	}
	return tx.Tx.Commit()
}

func (tx *Tx) Rollback() error {
	if tx == nil || tx.Tx == nil || tx.closed {
		return sql.ErrTxDone
	}
	tx.closed = true
	defer tx.state.end()
	var clearErr error
	if tx.journal && tx.backend == authorityBackendMySQL {
		clearErr = clearMySQLAuthorityContext(tx.Tx)
	}
	return errors.Join(clearErr, tx.Tx.Rollback())
}

func Exec(ctx context.Context, db *sql.DB, query string, args ...any) (sql.Result, error) {
	if !stateFor(db).enabled.Load() {
		state := stateFor(db)
		if err := state.begin(ctx); err != nil {
			return nil, err
		}
		defer state.end()
		return db.ExecContext(ctx, query, args...)
	}
	tx, err := Begin(ctx, db, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func randomGroupID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate authority transaction id: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func registerSQLiteFunctions() {
	registerSQLiteFunctionsOnce.Do(func() {
		modernsqlite.MustRegisterDeterministicScalarFunction("cpamp_mutation_id", 2,
			func(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
				if len(args) != 2 {
					return nil, errors.New("cpamp_mutation_id requires transaction and sequence")
				}
				hash := sha256.Sum256([]byte(fmt.Sprint(args[0]) + ":" + fmt.Sprint(args[1])))
				return hex.EncodeToString(hash[:]), nil
			})
		modernsqlite.MustRegisterDeterministicScalarFunction("cpamp_mutation_digest", 12,
			func(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
				if len(args) != 12 {
					return nil, errors.New("cpamp_mutation_digest requires the complete mutation envelope")
				}
				sequence, err := intValue(args[2])
				if err != nil {
					return nil, err
				}
				epoch, err := intValue(args[5])
				if err != nil {
					return nil, err
				}
				schemaVersion, err := intValue(args[10])
				if err != nil {
					return nil, err
				}
				rowVersion, err := intValue(args[11])
				if err != nil {
					return nil, err
				}
				mutation := databasemigration.Mutation{
					ID: fmt.Sprint(args[0]), TransactionID: fmt.Sprint(args[1]), Sequence: int(sequence),
					Source: databasemigration.Backend(fmt.Sprint(args[3])), Target: databasemigration.Backend(fmt.Sprint(args[4])),
					SourceEpoch: epoch, Table: fmt.Sprint(args[6]), Operation: databasemigration.Operation(fmt.Sprint(args[7])),
					PrimaryKey: []byte(fmt.Sprint(args[8])), Payload: []byte(fmt.Sprint(args[9])),
					SchemaVersion: int(schemaVersion), RowVersion: rowVersion,
				}
				return databasemigration.MutationDigest(mutation), nil
			})
	})
}

func intValue(value driver.Value) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case float64:
		return int64(typed), nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	default:
		return 0, fmt.Errorf("expected integer sqlite value, got %T", value)
	}
}

var _ databasemigration.SQLiteJournalSQLProvider = (*SQLiteProvider)(nil)
