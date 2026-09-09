package databasemanagement

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
)

const reinitializeMySQLSchemaOperation = "mysql_schema_reinitialize"

// ReinitializeMySQLSchema intentionally does not copy configuration or
// history, enable replication, or change routing. It only makes the already
// configured MySQL schema an empty canonical target and advances the control
// generation after full structural validation succeeds.
func (r *Runtime) ReinitializeMySQLSchema(
	ctx context.Context,
	input MySQLSchemaReinitializeMutation,
) (Status, error) {
	if err := input.Validate(); err != nil {
		return Status{}, err
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()

	state, err := r.requireGeneration(input.ExpectedGeneration)
	if err != nil {
		return Status{}, err
	}
	// Evaluate every non-destructive gate before even ensuring the metadata
	// repository: on a recovery path that repository can itself be MySQL-backed.
	if err := validateMySQLSchemaReinitializeSafety(state, input); err != nil {
		return Status{}, err
	}
	repository, err := r.metadataRepository(ctx, true)
	if err != nil {
		return Status{}, err
	}
	if replay, ok, err := replayStatus(ctx, repository, reinitializeMySQLSchemaOperation,
		input, input.IdempotencyKey); err != nil || ok {
		return replay, err
	}
	preflight, err := dbmysql.Test(ctx, state.MySQL)
	if err != nil {
		return Status{}, fmt.Errorf("preflight mysql schema reinitialization: %w", err)
	}
	if err := preflight.RequireMigrationCapabilities(); err != nil {
		return Status{}, errors.Join(ErrInvalidRequest, err)
	}
	opened, err := dbmysql.Open(ctx, state.MySQL)
	if err != nil {
		return Status{}, fmt.Errorf("open mysql schema reinitialization target: %w", err)
	}
	keepOpened := false
	defer func() {
		if !keepOpened {
			_ = opened.Close()
		}
	}()
	if err := schema.Reinitialize(ctx, opened, state.MySQL.Database); err != nil {
		return Status{}, err
	}
	if r.validateBackend != nil {
		candidate := database.NewSQLBackend(database.BackendMySQL, opened)
		if err := r.validateBackend(ctx, candidate); err != nil {
			return Status{}, errors.Join(ErrInvalidRequest,
				fmt.Errorf("reinitialized mysql repository contract: %w", err))
		}
	}

	next, err := r.control.Update(input.ExpectedGeneration, func(current *control.State) error {
		// Recheck all persistent gates at the generation CAS boundary. Runtime's
		// operation mutex serializes this process, while the encrypted store CAS
		// rejects a concurrent recovery/controller process.
		return validateMySQLSchemaReinitializeSafety(*current, input)
	})
	if err != nil {
		return Status{}, mapControlError(err)
	}
	status, err := r.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	status.Generation = next.Generation
	if err := storeStatusReplay(ctx, repository, reinitializeMySQLSchemaOperation,
		input, input.IdempotencyKey, status); err != nil {
		return Status{}, err
	}
	// Keep the old pool alive through replay persistence: in recovery mode the
	// idempotency repository itself may be backed by that MySQL pool.
	r.replaceMySQL(database.NewSQLBackend(database.BackendMySQL, opened))
	keepOpened = true
	return status, nil
}

func validateMySQLSchemaReinitializeSafety(
	state control.State,
	input MySQLSchemaReinitializeMutation,
) error {
	if strings.TrimSpace(state.MySQL.Host) == "" {
		return errors.Join(ErrInvalidRequest, errors.New("mysql is not configured"))
	}
	if strings.TrimSpace(input.ConfirmDatabase) != state.MySQL.Database {
		return errors.Join(ErrUnsafeOperation,
			fmt.Errorf("confirmed database %q does not match configured database %q",
				strings.TrimSpace(input.ConfirmDatabase), state.MySQL.Database))
	}
	if state.ReplicationEnabled {
		return errors.Join(ErrUnsafeOperation,
			errors.New("mysql schema cannot be reinitialized while replication is enabled"))
	}
	if migrationIsActive(state.Migration) {
		return errors.Join(ErrUnsafeOperation,
			errors.New("mysql schema cannot be reinitialized while a database migration is active"))
	}
	if state.Failover.Status == "switching" {
		return errors.Join(ErrUnsafeOperation,
			errors.New("mysql schema cannot be reinitialized while a write failover is active"))
	}
	if state.WritePrimary == database.BackendMySQL ||
		state.BusinessReadPrimary == database.BackendMySQL ||
		state.SystemReadPrimary == database.BackendMySQL {
		return errors.Join(ErrUnsafeOperation,
			errors.New("mysql schema cannot be reinitialized while mysql is an active read or write primary"))
	}
	return nil
}

func migrationIsActive(migration control.MigrationRef) bool {
	if strings.TrimSpace(migration.ID) == "" {
		return false
	}
	switch databasemigration.RunStatus(migration.Status) {
	case databasemigration.StatusCanceled, databasemigration.StatusFailed,
		databasemigration.StatusSucceeded:
		return false
	default:
		return true
	}
}
