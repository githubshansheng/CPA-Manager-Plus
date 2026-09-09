package databasemanagement

import (
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
)

func TestValidationProgressReportsRowFractionWithoutResettingSteps(t *testing.T) {
	runtime := &Runtime{}
	finish := runtime.beginValidationProgress("migration-1", 8)
	defer finish()
	runtime.updateValidationProgress("migration-1", databasemigration.ValidationProgress{
		Stage: databasemigration.ValidationStageTableSnapshot, Table: "usage_events",
		Side: databasemigration.ValidationSourceSide, CompletedSteps: 2, TotalSteps: 8,
	})
	runtime.updateValidationProgress("migration-1", databasemigration.ValidationProgress{
		Stage: databasemigration.ValidationStageTableSnapshot, Table: "usage_events",
		Side: databasemigration.ValidationSourceSide, ProcessedRows: 50, TotalRows: 100,
	})
	snapshot, active := runtime.validationProgressSnapshot("migration-1")
	if !active {
		t.Fatal("validation progress unexpectedly inactive")
	}
	status := validationProgressStatus(snapshot)
	if status == nil || status.CompletedSteps != 2 || status.TotalSteps != 8 {
		t.Fatalf("progress status = %#v", status)
	}
	if status.ProgressPercent <= 25 || status.ProgressPercent >= 38 {
		t.Fatalf("row fraction was not reflected in progress: %v", status.ProgressPercent)
	}
	finish()
	if _, active := runtime.validationProgressSnapshot("migration-1"); active {
		t.Fatal("finished validation left an active progress record")
	}
}
