package databasemigration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
)

type Clock func() time.Time

type SQLRepository struct {
	db      *sql.DB
	dialect Dialect
	now     Clock
}

func NewSQLRepository(db *sql.DB, dialect Dialect) *SQLRepository {
	return &SQLRepository{db: db, dialect: dialect, now: time.Now}
}

func (r *SQLRepository) WithClock(now Clock) *SQLRepository {
	copy := *r
	copy.now = now
	return &copy
}

func (r *SQLRepository) EnsureSchema(ctx context.Context) error {
	if r.db == nil {
		return errors.New("database migration repository requires a database")
	}
	autoID := "INTEGER PRIMARY KEY AUTOINCREMENT"
	outboxIndexes := ""
	migrationEventIndexes := ""
	mysqlCollation := ""
	if r.dialect == DialectMySQL {
		var version, comment string
		if err := r.db.QueryRowContext(ctx, `SELECT @@version, @@version_comment`).Scan(&version, &comment); err != nil {
			return fmt.Errorf("inspect mysql migration repository version: %w", err)
		}
		var err error
		mysqlCollation, err = database.MySQLStorageCollation(version, comment)
		if err != nil {
			return err
		}
		autoID = "BIGINT PRIMARY KEY AUTO_INCREMENT"
		outboxIndexes = `,
			INDEX database_outbox_pending_idx (applied_at_ms, outbox_id),
			INDEX database_outbox_group_idx (transaction_id, sequence_no)`
		migrationEventIndexes = `,
			INDEX database_migration_events_migration_idx (migration_id, event_id)`
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS database_routing_state (
			id INTEGER PRIMARY KEY,
			generation BIGINT NOT NULL,
			write_primary VARCHAR(16) NOT NULL,
			business_read VARCHAR(16) NOT NULL,
			system_read VARCHAR(16) NOT NULL,
			epoch BIGINT NOT NULL,
			updated_at_ms BIGINT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS database_migrations (
			id VARCHAR(64) PRIMARY KEY,
			idempotency_key VARCHAR(128) NOT NULL UNIQUE,
			generation BIGINT NOT NULL,
			source_backend VARCHAR(16) NOT NULL,
			target_backend VARCHAR(16) NOT NULL,
			phase VARCHAR(32) NOT NULL,
			status VARCHAR(16) NOT NULL,
			batch_size INTEGER NOT NULL,
			frozen_price_hash VARCHAR(128) NOT NULL,
			final_outbox_watermark BIGINT NOT NULL,
			validation_json LONGTEXT NULL,
			validation_token VARCHAR(128) NOT NULL,
			created_at_ms BIGINT NOT NULL,
			updated_at_ms BIGINT NOT NULL,
			finished_at_ms BIGINT NOT NULL,
			last_error LONGTEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS database_migration_tables (
			migration_id VARCHAR(64) NOT NULL,
			table_name VARCHAR(128) NOT NULL,
			checkpoint_json LONGTEXT NULL,
			source_watermark_json LONGTEXT NULL,
			rows_copied BIGINT NOT NULL,
			bytes_copied BIGINT NOT NULL,
			requests BIGINT NOT NULL,
			batch_size INTEGER NOT NULL,
			completed INTEGER NOT NULL,
			updated_at_ms BIGINT NOT NULL,
			PRIMARY KEY (migration_id, table_name)
		)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS database_migration_events (
			event_id %s,
			migration_id VARCHAR(64) NOT NULL,
			event_type VARCHAR(32) NOT NULL,
			previous_phase VARCHAR(32) NOT NULL,
			previous_status VARCHAR(16) NOT NULL,
			phase VARCHAR(32) NOT NULL,
			status VARCHAR(16) NOT NULL,
			generation BIGINT NOT NULL,
			table_name VARCHAR(128) NOT NULL,
			error_text LONGTEXT NOT NULL,
			created_at_ms BIGINT NOT NULL%s
		)`, autoID, migrationEventIndexes),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS database_outbox (
			outbox_id %s,
			mutation_id VARCHAR(96) NOT NULL UNIQUE,
			mutation_digest VARCHAR(128) NOT NULL,
			transaction_id VARCHAR(96) NOT NULL,
			sequence_no INTEGER NOT NULL,
			source_backend VARCHAR(16) NOT NULL,
			target_backend VARCHAR(16) NOT NULL,
			source_epoch BIGINT NOT NULL,
			table_name VARCHAR(128) NOT NULL,
			mutation_operation VARCHAR(16) NOT NULL,
			primary_key_json LONGTEXT NOT NULL,
			payload_json LONGTEXT NULL,
			schema_version INTEGER NOT NULL,
			row_version BIGINT NOT NULL,
			payload_bytes BIGINT NOT NULL,
			created_at_ms BIGINT NOT NULL,
			applied_at_ms BIGINT NOT NULL,
			UNIQUE (transaction_id, sequence_no)%s
		)`, autoID, outboxIndexes),
		`CREATE TABLE IF NOT EXISTS database_inbox (
			mutation_id VARCHAR(96) PRIMARY KEY,
			mutation_digest VARCHAR(128) NOT NULL,
			transaction_id VARCHAR(96) NOT NULL,
			sequence_no INTEGER NOT NULL,
			source_backend VARCHAR(16) NOT NULL,
			source_epoch BIGINT NOT NULL,
			outbox_id BIGINT NOT NULL,
			applied_at_ms BIGINT NOT NULL,
			UNIQUE (transaction_id, sequence_no)
		)`,
		`CREATE TABLE IF NOT EXISTS database_inbox_sources (
			source_backend VARCHAR(16) PRIMARY KEY,
			accepted_epoch BIGINT NOT NULL,
			watermark BIGINT NOT NULL,
			updated_at_ms BIGINT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS database_row_versions (
			table_name VARCHAR(128) NOT NULL,
			primary_key_hash VARCHAR(64) NOT NULL,
			primary_key_json LONGTEXT NOT NULL,
			source_epoch BIGINT NOT NULL,
			row_version BIGINT NOT NULL,
			mutation_id VARCHAR(96) NOT NULL,
			mutation_digest VARCHAR(128) NOT NULL,
			updated_at_ms BIGINT NOT NULL,
			PRIMARY KEY (table_name, primary_key_hash)
		)`,
		`CREATE TABLE IF NOT EXISTS database_replication_state (
			direction VARCHAR(64) PRIMARY KEY,
			source_backend VARCHAR(16) NOT NULL,
			target_backend VARCHAR(16) NOT NULL,
			epoch BIGINT NOT NULL,
			source_watermark BIGINT NOT NULL,
			target_watermark BIGINT NOT NULL,
			backlog_rows BIGINT NOT NULL,
			backlog_bytes BIGINT NOT NULL,
			oldest_backlog_at_ms BIGINT NOT NULL,
			throughput_rows_per_sec DOUBLE NOT NULL,
			retries BIGINT NOT NULL,
			heartbeat_at_ms BIGINT NOT NULL,
			last_progress_at_ms BIGINT NOT NULL,
			last_success_at_ms BIGINT NOT NULL,
			last_error LONGTEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS database_cache_policy (
			id INTEGER PRIMARY KEY,
			generation BIGINT NOT NULL,
			enabled INTEGER NOT NULL,
			retention_days INTEGER NOT NULL,
			batch_size INTEGER NOT NULL,
			updated_at_ms BIGINT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS database_cache_coverage (
			id INTEGER PRIMARY KEY,
			earliest_at_ms BIGINT NOT NULL,
			latest_at_ms BIGINT NOT NULL,
			earliest_id BIGINT NOT NULL,
			latest_id BIGINT NOT NULL,
			watermark BIGINT NOT NULL,
			complete INTEGER NOT NULL,
			updated_at_ms BIGINT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS database_maintenance_tasks (
			id VARCHAR(64) PRIMARY KEY,
			idempotency_key VARCHAR(128) NOT NULL UNIQUE,
			generation BIGINT NOT NULL,
			kind VARCHAR(16) NOT NULL,
			status VARCHAR(16) NOT NULL,
			retention_days INTEGER NOT NULL,
			validation_token VARCHAR(128) NOT NULL,
			preview_json LONGTEXT NULL,
			current_table VARCHAR(128) NOT NULL,
			processed_rows BIGINT NOT NULL,
			created_at_ms BIGINT NOT NULL,
			updated_at_ms BIGINT NOT NULL,
			finished_at_ms BIGINT NOT NULL,
			last_error LONGTEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS database_operation_idempotency (
			idempotency_key VARCHAR(128) PRIMARY KEY,
			operation_name VARCHAR(128) NOT NULL,
			request_hash VARCHAR(128) NOT NULL,
			result_json LONGTEXT NOT NULL,
			result_generation BIGINT NOT NULL,
			created_at_ms BIGINT NOT NULL,
			updated_at_ms BIGINT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS database_journal_contracts (
			table_name VARCHAR(128) PRIMARY KEY,
			manifest_hash VARCHAR(128) NOT NULL,
			provider_contract VARCHAR(128) NOT NULL,
			installed_at_ms BIGINT NOT NULL
		)`,
	}
	// MySQL before 8.0.13 does not support CREATE INDEX IF NOT EXISTS. Supported
	// server versions are newer, but MySQL still does not implement that form.
	if r.dialect != DialectMySQL {
		statements = append(statements,
			`CREATE INDEX IF NOT EXISTS database_outbox_pending_idx ON database_outbox(applied_at_ms, outbox_id)`,
			`CREATE INDEX IF NOT EXISTS database_outbox_group_idx ON database_outbox(transaction_id, sequence_no)`,
			`CREATE INDEX IF NOT EXISTS database_migration_events_migration_idx ON database_migration_events(migration_id, event_id)`,
		)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range statements {
		if r.dialect == DialectMySQL && strings.HasPrefix(strings.TrimSpace(statement), "CREATE TABLE") {
			statement += ` ENGINE=InnoDB DEFAULT CHARACTER SET ` + database.MySQLCharacterSet +
				` COLLATE ` + mysqlCollation
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize database migration schema: %w", err)
		}
	}
	nowMS := r.now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO database_routing_state
		(id, generation, write_primary, business_read, system_read, epoch, updated_at_ms)
		SELECT 1, 1, ?, ?, ?, 1, ? WHERE NOT EXISTS
		(SELECT 1 FROM database_routing_state WHERE id = 1)`, BackendSQLite, BackendSQLite, BackendSQLite, nowMS); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO database_cache_policy
		(id, generation, enabled, retention_days, batch_size, updated_at_ms)
		SELECT 1, 1, ?, ?, ?, ? WHERE NOT EXISTS
		(SELECT 1 FROM database_cache_policy WHERE id = 1)`, boolInt(DefaultCleanupEnabled),
		DefaultRetentionDays, DefaultBatchSize, nowMS); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO database_cache_coverage
		(id, earliest_at_ms, latest_at_ms, earliest_id, latest_id, watermark, complete, updated_at_ms)
		SELECT 1, 0, 0, 0, 0, 0, 0, ? WHERE NOT EXISTS
		(SELECT 1 FROM database_cache_coverage WHERE id = 1)`, nowMS); err != nil {
		return err
	}
	// Existing installations predate the append-only event table. Seed one
	// snapshot per task so the history API is immediately useful after upgrade.
	if _, err := tx.ExecContext(ctx, `INSERT INTO database_migration_events (
		migration_id, event_type, previous_phase, previous_status, phase, status,
		generation, table_name, error_text, created_at_ms)
		SELECT migrations.id, 'snapshot', '', '', migrations.phase, migrations.status,
			migrations.generation, '', migrations.last_error, migrations.updated_at_ms
		FROM database_migrations AS migrations
		WHERE NOT EXISTS (
			SELECT 1 FROM database_migration_events AS events
			WHERE events.migration_id = migrations.id
		)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLRepository) Routing(ctx context.Context) (RoutingState, error) {
	return scanRouting(r.db.QueryRowContext(ctx, `SELECT generation, write_primary,
		business_read, system_read, epoch, updated_at_ms FROM database_routing_state WHERE id = 1`))
}

func (r *SQLRepository) CompareAndSwapRouting(ctx context.Context, expectedGeneration, expectedEpoch int64, change RoutingChange) (RoutingState, error) {
	if !validBackend(change.WritePrimary) || !validBackend(change.BusinessRead) || !validBackend(change.SystemRead) {
		return RoutingState{}, errors.New("routing change contains an invalid backend")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return RoutingState{}, err
	}
	defer tx.Rollback()
	current, err := scanRouting(tx.QueryRowContext(ctx, `SELECT generation, write_primary,
		business_read, system_read, epoch, updated_at_ms FROM database_routing_state WHERE id = 1`))
	if err != nil {
		return RoutingState{}, err
	}
	if current.Generation != expectedGeneration {
		return RoutingState{}, &ConflictError{Kind: ErrGenerationConflict, Expected: expectedGeneration, Actual: current.Generation}
	}
	if current.Epoch != expectedEpoch {
		return RoutingState{}, &ConflictError{Kind: ErrEpochFenced, Expected: expectedEpoch, Actual: current.Epoch}
	}
	nextEpoch := current.Epoch
	if change.WritePrimary != current.WritePrimary {
		nextEpoch++
	}
	nowMS := r.now().UnixMilli()
	result, err := tx.ExecContext(ctx, `UPDATE database_routing_state SET generation = ?,
		write_primary = ?, business_read = ?, system_read = ?, epoch = ?, updated_at_ms = ?
		WHERE id = 1 AND generation = ? AND epoch = ?`, current.Generation+1, change.WritePrimary,
		change.BusinessRead, change.SystemRead, nextEpoch, nowMS, current.Generation, current.Epoch)
	if err != nil {
		return RoutingState{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return RoutingState{}, ErrGenerationConflict
	}
	if err := tx.Commit(); err != nil {
		return RoutingState{}, err
	}
	return RoutingState{Generation: current.Generation + 1, WritePrimary: change.WritePrimary,
		BusinessRead: change.BusinessRead, SystemRead: change.SystemRead, Epoch: nextEpoch, UpdatedAtMS: nowMS}, nil
}

func (r *SQLRepository) AssertWriteEpoch(ctx context.Context, backend Backend, epoch int64) error {
	state, err := r.Routing(ctx)
	if err != nil {
		return err
	}
	if state.WritePrimary != backend || state.Epoch != epoch {
		return &ConflictError{Kind: ErrEpochFenced, Expected: epoch, Actual: state.Epoch}
	}
	return nil
}

func (r *SQLRepository) CreateMigration(ctx context.Context, request CreateMigrationRequest) (Migration, bool, error) {
	if request.IdempotencyKey == "" || !validBackend(request.Source) || !validBackend(request.Target) || request.Source == request.Target {
		return Migration{}, false, errors.New("migration requires an idempotency key and distinct valid backends")
	}
	if request.BatchSize <= 0 {
		request.BatchSize = DefaultBatchSize
	}
	if request.ID == "" {
		request.ID = newID("migration")
	}
	if request.FrozenPriceHash == "" || len(request.FrozenPriceBook) == 0 || !json.Valid(request.FrozenPriceBook) {
		return Migration{}, false, errors.New("migration requires a valid frozen price-book snapshot")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Migration{}, false, err
	}
	defer tx.Rollback()
	existing, err := scanMigration(tx.QueryRowContext(ctx, migrationSelect+` WHERE idempotency_key = ?`, request.IdempotencyKey))
	if err == nil {
		if existing.Source != request.Source || existing.Target != request.Target ||
			existing.FrozenPriceHash != request.FrozenPriceHash || existing.BatchSize != request.BatchSize ||
			string(existing.FrozenPriceBook) != string(request.FrozenPriceBook) {
			return Migration{}, false, ErrIdempotencyConflict
		}
		return existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Migration{}, false, err
	}
	routing, err := scanRouting(tx.QueryRowContext(ctx, `SELECT generation, write_primary,
		business_read, system_read, epoch, updated_at_ms FROM database_routing_state WHERE id = 1`))
	if err != nil {
		return Migration{}, false, err
	}
	if routing.Generation != request.ExpectedGeneration {
		return Migration{}, false, &ConflictError{Kind: ErrGenerationConflict, Expected: request.ExpectedGeneration, Actual: routing.Generation}
	}
	nowMS := r.now().UnixMilli()
	migration := Migration{ID: request.ID, IdempotencyKey: request.IdempotencyKey, Generation: 1,
		Source: request.Source, Target: request.Target, Phase: PhaseEnableDualWrite, Status: StatusRunning,
		BatchSize: request.BatchSize, FrozenPriceHash: request.FrozenPriceHash,
		FrozenPriceBook: append(json.RawMessage(nil), request.FrozenPriceBook...), CreatedAtMS: nowMS, UpdatedAtMS: nowMS}
	persisted, err := encodeMigrationState(migration)
	if err != nil {
		return Migration{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO database_migrations (id, idempotency_key, generation,
		source_backend, target_backend, phase, status, batch_size, frozen_price_hash,
		final_outbox_watermark, validation_json, validation_token, created_at_ms, updated_at_ms,
		finished_at_ms, last_error) VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, 0, ?, '', ?, ?, 0, '')`,
		migration.ID, migration.IdempotencyKey, migration.Source, migration.Target, migration.Phase,
		migration.Status, migration.BatchSize, migration.FrozenPriceHash, persisted, nowMS, nowMS)
	if err != nil {
		return Migration{}, false, err
	}
	if err := appendMigrationEvent(ctx, tx, nil, migration, "created"); err != nil {
		return Migration{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Migration{}, false, err
	}
	return migration, true, nil
}

const migrationSelect = `SELECT id, idempotency_key, generation, source_backend,
	target_backend, phase, status, batch_size, frozen_price_hash, final_outbox_watermark,
	validation_json, validation_token, created_at_ms, updated_at_ms, finished_at_ms, last_error
	FROM database_migrations`

func (r *SQLRepository) Migration(ctx context.Context, id string) (Migration, error) {
	migration, err := scanMigration(r.db.QueryRowContext(ctx, migrationSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Migration{}, ErrNotFound
	}
	return migration, err
}

func (r *SQLRepository) ListMigrations(ctx context.Context, limit int) ([]Migration, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, migrationSelect+` ORDER BY created_at_ms DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	migrations := make([]Migration, 0, limit)
	for rows.Next() {
		migration, scanErr := scanMigration(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		migrations = append(migrations, migration)
	}
	return migrations, rows.Err()
}

func (r *SQLRepository) MigrationEvents(ctx context.Context, migrationID string, limit int) ([]MigrationEvent, error) {
	if strings.TrimSpace(migrationID) == "" {
		return nil, errors.New("migration id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := r.db.QueryContext(ctx, `SELECT event_id, migration_id, event_type,
		previous_phase, previous_status, phase, status, generation, table_name, error_text, created_at_ms
		FROM database_migration_events WHERE migration_id = ?
		ORDER BY event_id DESC LIMIT ?`, migrationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]MigrationEvent, 0, limit)
	for rows.Next() {
		var event MigrationEvent
		if err := rows.Scan(&event.ID, &event.MigrationID, &event.EventType,
			&event.PreviousPhase, &event.PreviousStatus, &event.Phase, &event.Status,
			&event.Generation, &event.Table, &event.Error, &event.CreatedAtMS); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	return events, nil
}

// RecordMigrationError appends an operation failure without changing the
// migration state. Administrative operations such as final validation can
// fail before a state transition is safe; retaining the attempt here keeps
// those failures visible after the HTTP response or browser notification is
// gone, while still allowing the administrator to retry the same phase.
func (r *SQLRepository) RecordMigrationError(
	ctx context.Context,
	migrationID string,
	eventType string,
	cause error,
) error {
	if strings.TrimSpace(migrationID) == "" {
		return errors.New("migration id is required")
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" || len(eventType) > 32 {
		return errors.New("migration error event type is required and must not exceed 32 characters")
	}
	if cause == nil {
		return errors.New("migration error cause is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := scanMigration(tx.QueryRowContext(ctx, migrationSelect+` WHERE id = ?`, migrationID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO database_migration_events (
		migration_id, event_type, previous_phase, previous_status, phase, status,
		generation, table_name, error_text, created_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?)`,
		current.ID, eventType, current.Phase, current.Status, current.Phase,
		current.Status, current.Generation, cause.Error(), r.now().UnixMilli())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLRepository) PauseMigration(ctx context.Context, id string, expectedGeneration int64) (Migration, error) {
	return r.transitionMigration(ctx, id, expectedGeneration, "paused", func(current *Migration) error {
		if err := ValidateMigrationTransition(*current, current.Phase, StatusPaused); err != nil {
			return err
		}
		current.Status = StatusPaused
		return nil
	})
}

func (r *SQLRepository) ResumeMigration(ctx context.Context, id string, expectedGeneration int64) (Migration, error) {
	return r.transitionMigration(ctx, id, expectedGeneration, "resumed", func(current *Migration) error {
		if err := ValidateMigrationTransition(*current, current.Phase, StatusRunning); err != nil {
			return err
		}
		current.Status = StatusRunning
		current.LastError = ""
		return nil
	})
}

func (r *SQLRepository) CancelMigration(ctx context.Context, id string, expectedGeneration int64) (Migration, error) {
	return r.transitionMigration(ctx, id, expectedGeneration, "canceled", func(current *Migration) error {
		if err := ValidateMigrationTransition(*current, current.Phase, StatusCanceled); err != nil {
			return err
		}
		current.Status = StatusCanceled
		current.FinishedAtMS = r.now().UnixMilli()
		return nil
	})
}

func (r *SQLRepository) FailMigration(ctx context.Context, id string, expectedGeneration int64, cause error) (Migration, error) {
	return r.transitionMigration(ctx, id, expectedGeneration, "failed", func(current *Migration) error {
		if err := ValidateMigrationTransition(*current, current.Phase, StatusFailed); err != nil {
			return err
		}
		current.Status = StatusFailed
		if cause != nil {
			current.LastError = cause.Error()
		}
		return nil
	})
}

func (r *SQLRepository) AdvanceMigration(ctx context.Context, id string, expectedGeneration int64, next MigrationPhase) (Migration, error) {
	return r.transitionMigration(ctx, id, expectedGeneration, "phase_advanced", func(current *Migration) error {
		if current.Status != StatusRunning || !CanAdvancePhase(current.Phase, next) || next == PhaseReadyToCutover || next == PhaseCompleted {
			return fmt.Errorf("%w: cannot advance %s/%s to %s", ErrInvalidTransition, current.Phase, current.Status, next)
		}
		current.Phase = next
		return nil
	})
}

func (r *SQLRepository) RecordValidation(ctx context.Context, id string, expectedGeneration int64, result ValidationResult) (Migration, error) {
	return r.transitionMigration(ctx, id, expectedGeneration, "validation_recorded", func(current *Migration) error {
		if (current.Phase != PhaseValidate && current.Phase != PhaseReadyToCutover) || current.Status != StatusRunning {
			return fmt.Errorf("%w: validation requires a running validate or ready_to_cutover phase", ErrInvalidTransition)
		}
		result.MigrationID = current.ID
		result.FrozenPriceHash = current.FrozenPriceHash
		result.Passed = ValidationPassed(result)
		if result.ValidatedAtMS == 0 {
			result.ValidatedAtMS = r.now().UnixMilli()
		}
		result.Token = ""
		if result.Passed {
			result.Token = ValidationToken(result)
		}
		current.Validation = &result
		current.ValidationToken = result.Token
		current.FinalOutboxWatermark = result.FinalOutboxWatermark
		if result.Passed {
			current.Phase = PhaseReadyToCutover
			current.LastError = ""
		} else {
			current.Phase = PhaseValidate
			current.LastError = "migration validation failed"
		}
		return nil
	})
}

func (r *SQLRepository) CompleteCutover(ctx context.Context, id string, expectedGeneration int64, validationToken string) (Migration, error) {
	return r.transitionMigration(ctx, id, expectedGeneration, "completed", func(current *Migration) error {
		if validationToken == "" || validationToken != current.ValidationToken {
			return fmt.Errorf("%w: validation token does not match", ErrInvalidTransition)
		}
		if err := ValidateMigrationTransition(*current, PhaseCompleted, StatusSucceeded); err != nil {
			return err
		}
		current.Phase = PhaseCompleted
		current.Status = StatusSucceeded
		current.FinishedAtMS = r.now().UnixMilli()
		return nil
	})
}

func (r *SQLRepository) transitionMigration(ctx context.Context, id string, expectedGeneration int64, eventType string, mutate func(*Migration) error) (Migration, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Migration{}, err
	}
	defer tx.Rollback()
	current, err := scanMigration(tx.QueryRowContext(ctx, migrationSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Migration{}, ErrNotFound
	}
	if err != nil {
		return Migration{}, err
	}
	if current.Generation != expectedGeneration {
		return Migration{}, &ConflictError{Kind: ErrGenerationConflict, Expected: expectedGeneration, Actual: current.Generation}
	}
	previous := current
	if err := mutate(&current); err != nil {
		return Migration{}, err
	}
	current.Generation++
	current.UpdatedAtMS = r.now().UnixMilli()
	var validation any
	if len(current.FrozenPriceBook) > 0 || current.Validation != nil {
		encoded, err := encodeMigrationState(current)
		if err != nil {
			return Migration{}, err
		}
		validation = encoded
	}
	result, err := tx.ExecContext(ctx, `UPDATE database_migrations SET generation = ?, phase = ?,
		status = ?, final_outbox_watermark = ?, validation_json = ?, validation_token = ?,
		updated_at_ms = ?, finished_at_ms = ?, last_error = ? WHERE id = ? AND generation = ?`,
		current.Generation, current.Phase, current.Status, current.FinalOutboxWatermark, validation,
		current.ValidationToken, current.UpdatedAtMS, current.FinishedAtMS, current.LastError, current.ID, expectedGeneration)
	if err != nil {
		return Migration{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Migration{}, ErrGenerationConflict
	}
	if err := appendMigrationEvent(ctx, tx, &previous, current, eventType); err != nil {
		return Migration{}, err
	}
	if err := tx.Commit(); err != nil {
		return Migration{}, err
	}
	return current, nil
}

func appendMigrationEvent(
	ctx context.Context,
	tx *sql.Tx,
	previous *Migration,
	current Migration,
	eventType string,
) error {
	var previousPhase MigrationPhase
	var previousStatus RunStatus
	if previous != nil {
		previousPhase = previous.Phase
		previousStatus = previous.Status
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO database_migration_events (
		migration_id, event_type, previous_phase, previous_status, phase, status,
		generation, table_name, error_text, created_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?)`,
		current.ID, eventType, previousPhase, previousStatus, current.Phase,
		current.Status, current.Generation, current.LastError, current.UpdatedAtMS)
	return err
}

func appendMigrationTableEvent(
	ctx context.Context,
	tx *sql.Tx,
	migration Migration,
	eventType string,
	table string,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO database_migration_events (
		migration_id, event_type, previous_phase, previous_status, phase, status,
		generation, table_name, error_text, created_at_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?)`,
		migration.ID, eventType, migration.Phase, migration.Status, migration.Phase,
		migration.Status, migration.Generation, table, migration.UpdatedAtMS)
	return err
}

func scanRouting(row interface{ Scan(...any) error }) (RoutingState, error) {
	var state RoutingState
	err := row.Scan(&state.Generation, &state.WritePrimary, &state.BusinessRead, &state.SystemRead, &state.Epoch, &state.UpdatedAtMS)
	return state, err
}

func scanMigration(row interface{ Scan(...any) error }) (Migration, error) {
	var migration Migration
	var validation sql.NullString
	err := row.Scan(&migration.ID, &migration.IdempotencyKey, &migration.Generation, &migration.Source,
		&migration.Target, &migration.Phase, &migration.Status, &migration.BatchSize,
		&migration.FrozenPriceHash, &migration.FinalOutboxWatermark, &validation,
		&migration.ValidationToken, &migration.CreatedAtMS, &migration.UpdatedAtMS,
		&migration.FinishedAtMS, &migration.LastError)
	if err != nil {
		return Migration{}, err
	}
	if validation.Valid && validation.String != "" {
		if err := decodeMigrationState([]byte(validation.String), &migration); err != nil {
			return Migration{}, fmt.Errorf("decode migration validation state: %w", err)
		}
	}
	return migration, nil
}

const migrationStateFormat = 1

type persistedMigrationState struct {
	Format          int               `json:"format"`
	FrozenPriceBook json.RawMessage   `json:"frozenPriceBook,omitempty"`
	Validation      *ValidationResult `json:"validation,omitempty"`
}

func encodeMigrationState(migration Migration) (string, error) {
	encoded, err := json.Marshal(persistedMigrationState{Format: migrationStateFormat,
		FrozenPriceBook: migration.FrozenPriceBook, Validation: migration.Validation})
	return string(encoded), err
}

func decodeMigrationState(encoded []byte, migration *Migration) error {
	var envelope persistedMigrationState
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return err
	}
	if envelope.Format == migrationStateFormat {
		if len(envelope.FrozenPriceBook) == 0 || !json.Valid(envelope.FrozenPriceBook) {
			return errors.New("migration price-book snapshot is missing or invalid")
		}
		migration.FrozenPriceBook = append(json.RawMessage(nil), envelope.FrozenPriceBook...)
		migration.Validation = envelope.Validation
		return nil
	}
	// Backward compatibility for migrations created before the envelope was
	// introduced. They can be inspected/canceled, but final validation will
	// refuse them because no durable frozen price book exists.
	var legacy ValidationResult
	if err := json.Unmarshal(encoded, &legacy); err != nil {
		return err
	}
	migration.Validation = &legacy
	return nil
}

func newID(prefix string) string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(fmt.Sprintf("secure random identifier unavailable: %v", err))
	}
	return prefix + "-" + hex.EncodeToString(value[:])
}
