package databasemanagement

import (
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
)

// requireCurrentValidationConfirmation verifies the browser confirmation
// against the encrypted control-plane snapshot.  The control file is the
// authoritative recovery source when SQLite is damaged, so failover remains
// possible without consulting SQLite while still rejecting stale UI state.
func requireCurrentValidationConfirmation(
	state control.State,
	confirmation DangerousOperationConfirmation,
) error {
	migrationID := strings.TrimSpace(confirmation.MigrationID)
	validationToken := strings.TrimSpace(confirmation.ValidationToken)
	currentToken := strings.TrimSpace(state.Migration.ValidationToken)
	phaseReady := state.Migration.Phase == string(databasemigration.PhaseReadyToCutover) &&
		state.Migration.Status == string(databasemigration.StatusRunning)
	phaseCompleted := state.Migration.Phase == string(databasemigration.PhaseCompleted) &&
		state.Migration.Status == string(databasemigration.StatusSucceeded)
	tokenMatches := validationToken != "" && currentToken != "" &&
		subtle.ConstantTimeCompare([]byte(validationToken), []byte(currentToken)) == 1
	if migrationID == "" || migrationID != state.Migration.ID || !tokenMatches ||
		(!phaseReady && !phaseCompleted) {
		return errors.Join(ErrUnsafeOperation,
			errors.New("migration confirmation is missing, stale, or not successfully validated"))
	}
	return nil
}

// requireMigrationReadyForCutover rechecks the durable migration row before
// any routing metadata is changed. The encrypted control snapshot is checked
// first, but it can briefly lag the SQLite metadata if a previous state update
// was interrupted between those two durable writes.
func requireMigrationReadyForCutover(migration databasemigration.Migration) error {
	switch migration.Phase {
	case databasemigration.PhaseReadyToCutover:
		if migration.Status == databasemigration.StatusRunning {
			return nil
		}
	case databasemigration.PhaseCompleted:
		if migration.Status == databasemigration.StatusSucceeded {
			return nil
		}
	}
	return errors.Join(ErrUnsafeOperation,
		errors.New("migration is paused, failed, or not ready to cut over"))
}
