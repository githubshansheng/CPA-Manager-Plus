package databasemanagement

import (
	"errors"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
)

func TestDangerousOperationConfirmationRequiresExactTargetMigrationAndToken(t *testing.T) {
	valid := DangerousOperationConfirmation{Target: database.BackendMySQL,
		MigrationID: "migration-1", ValidationToken: "validation-1"}
	if err := valid.Validate(database.BackendMySQL); err != nil {
		t.Fatalf("valid confirmation rejected: %v", err)
	}
	cases := []DangerousOperationConfirmation{
		{Target: database.BackendSQLite, MigrationID: valid.MigrationID,
			ValidationToken: valid.ValidationToken},
		{Target: valid.Target, ValidationToken: valid.ValidationToken},
		{Target: valid.Target, MigrationID: valid.MigrationID},
	}
	for index, candidate := range cases {
		if err := candidate.Validate(database.BackendMySQL); !errors.Is(err, ErrUnsafeOperation) {
			t.Errorf("unsafe confirmation %d error=%v", index, err)
		}
	}
	tooLong := valid
	tooLong.ValidationToken = strings.Repeat("x", 513)
	if err := tooLong.Validate(database.BackendMySQL); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("oversized confirmation error=%v", err)
	}
}

func TestCurrentValidationConfirmationRejectsStaleControlPlaneState(t *testing.T) {
	state := control.DefaultState()
	state.Migration = control.MigrationRef{ID: "migration-1",
		Phase:  string(databasemigration.PhaseCompleted),
		Status: string(databasemigration.StatusSucceeded), ValidationToken: "validation-1"}
	valid := DangerousOperationConfirmation{Target: database.BackendSQLite,
		MigrationID: state.Migration.ID, ValidationToken: state.Migration.ValidationToken}
	if err := requireCurrentValidationConfirmation(state, valid); err != nil {
		t.Fatalf("current confirmation rejected: %v", err)
	}

	ready := state
	ready.Migration.Phase = string(databasemigration.PhaseReadyToCutover)
	ready.Migration.Status = string(databasemigration.StatusRunning)
	if err := requireCurrentValidationConfirmation(ready, valid); err != nil {
		t.Fatalf("ready-to-cutover confirmation rejected: %v", err)
	}

	for name, mutate := range map[string]func(*control.State, *DangerousOperationConfirmation){
		"migration": func(_ *control.State, confirmation *DangerousOperationConfirmation) {
			confirmation.MigrationID = "migration-stale"
		},
		"token": func(_ *control.State, confirmation *DangerousOperationConfirmation) {
			confirmation.ValidationToken = "validation-stale"
		},
		"phase": func(candidate *control.State, _ *DangerousOperationConfirmation) {
			candidate.Migration.Phase = string(databasemigration.PhaseValidate)
			candidate.Migration.Status = string(databasemigration.StatusRunning)
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidateState := state
			candidateConfirmation := valid
			mutate(&candidateState, &candidateConfirmation)
			if err := requireCurrentValidationConfirmation(candidateState,
				candidateConfirmation); !errors.Is(err, ErrUnsafeOperation) {
				t.Fatalf("stale confirmation error=%v", err)
			}
		})
	}
}

func TestMigrationReadyForCutoverRequiresMatchingDurableStatus(t *testing.T) {
	for _, valid := range []databasemigration.Migration{
		{Phase: databasemigration.PhaseReadyToCutover, Status: databasemigration.StatusRunning},
		{Phase: databasemigration.PhaseCompleted, Status: databasemigration.StatusSucceeded},
	} {
		if err := requireMigrationReadyForCutover(valid); err != nil {
			t.Fatalf("valid migration state %s/%s rejected: %v", valid.Phase, valid.Status, err)
		}
	}

	for _, invalid := range []databasemigration.Migration{
		{Phase: databasemigration.PhaseReadyToCutover, Status: databasemigration.StatusPaused},
		{Phase: databasemigration.PhaseReadyToCutover, Status: databasemigration.StatusFailed},
		{Phase: databasemigration.PhaseValidate, Status: databasemigration.StatusRunning},
		{Phase: databasemigration.PhaseCompleted, Status: databasemigration.StatusRunning},
	} {
		if err := requireMigrationReadyForCutover(invalid); !errors.Is(err, ErrUnsafeOperation) {
			t.Fatalf("unsafe migration state %s/%s error=%v", invalid.Phase, invalid.Status, err)
		}
	}
}
