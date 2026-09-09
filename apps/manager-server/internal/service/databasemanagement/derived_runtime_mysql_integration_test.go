package databasemanagement

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
)

func TestMySQLDerivedRebuildPlanStatementsExecute(t *testing.T) {
	if os.Getenv("CPAMP_MYSQL_INTEGRATION") != "1" {
		t.Skip("set CPAMP_MYSQL_INTEGRATION=1 to run")
	}
	port, err := strconv.Atoi(os.Getenv("CPAMP_MYSQL_TEST_PORT"))
	if err != nil {
		t.Fatalf("parse CPAMP_MYSQL_TEST_PORT: %v", err)
	}
	config := dbmysql.Config{
		Host:     os.Getenv("CPAMP_MYSQL_TEST_HOST"),
		Port:     port,
		Database: os.Getenv("CPAMP_MYSQL_TEST_DATABASE"),
		Username: os.Getenv("CPAMP_MYSQL_TEST_USERNAME"),
		Password: os.Getenv("CPAMP_MYSQL_TEST_PASSWORD"),
		TLSMode:  dbmysql.TLSDisabled,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := dbmysql.Open(ctx, config)
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	defer db.Close()
	if err := schema.Ensure(ctx, db); err != nil {
		t.Fatalf("ensure mysql schema: %v", err)
	}

	// A negative watermark leaves every source SELECT empty while still asking
	// MySQL to parse and resolve every CTE and window expression. Run all DML in
	// a transaction so this compatibility check never replaces existing derived
	// rows in a shared integration schema. MySQL 8.0.12 reports Error 1054 on the
	// account-model step when group_key is not qualified by scored.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin derived rebuild compatibility transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	steps := mysqlDerivedRebuildPlan(
		derivedRebuildWatermark{UsageEventID: -1, PricingRevision: "integration-test"},
		time.Now().UnixMilli(),
	)
	for _, step := range steps {
		for index, statement := range step.Statements {
			if _, err := tx.ExecContext(ctx, statement.Query, statement.Args...); err != nil {
				t.Fatalf("execute derived rebuild %s statement %d: %v", step.Table, index+1, err)
			}
		}
	}

	unique := strconv.FormatInt(time.Now().UnixNano(), 36)
	physicalFile := "Derived-Codex-" + unique + ".JSON"
	authIndex := "AUTH-" + unique
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_events (
		event_hash,timestamp_ms,timestamp,model,created_at_ms,
		provider,auth_provider_snapshot,auth_file_snapshot,source,auth_index,
		auth_account_id_snapshot,account_snapshot
	) VALUES
		(?,1,'weak','gpt-test',1,'codex','codex',?,?,?,'','member@example.com'),
		(?,2,'strong','gpt-test',2,'codex','codex',?,?,?,'workspace-a','member@example.com')`,
		"derived-codex-weak-"+unique, physicalFile, physicalFile, authIndex,
		"derived-codex-strong-"+unique, physicalFile, physicalFile, authIndex,
	); err != nil {
		t.Fatalf("insert non-empty Codex derived fixture: %v", err)
	}
	var latestID int64
	if err := tx.QueryRowContext(ctx, "SELECT MAX(id) FROM usage_events").Scan(&latestID); err != nil {
		t.Fatal(err)
	}
	evidenceStep := mysqlCodexLegacyIdentityEvidenceStep(derivedRebuildWatermark{UsageEventID: latestID})
	for index, statement := range evidenceStep.Statements {
		if _, err := tx.ExecContext(ctx, statement.Query, statement.Args...); err != nil {
			t.Fatalf("execute non-empty Codex evidence statement %d: %v", index+1, err)
		}
	}
	var evidenceRows int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_codex_legacy_identity_evidence_v1
		WHERE physical_file=LOWER(?) AND auth_index=LOWER(?)`, physicalFile, authIndex).Scan(&evidenceRows); err != nil {
		t.Fatal(err)
	}
	if evidenceRows != 2 {
		t.Fatalf("non-empty Codex evidence rows=%d, want 2", evidenceRows)
	}
	stateStep := mysqlMonitoringStateStep(
		derivedRebuildWatermark{UsageEventID: latestID, PricingRevision: "integration-test"},
		time.Now().UnixMilli(),
	)
	for index, statement := range stateStep.Statements {
		if _, err := tx.ExecContext(ctx, statement.Query, statement.Args...); err != nil {
			t.Fatalf("execute non-empty monitoring state statement %d: %v", index+1, err)
		}
	}
	var revision, status string
	var coverageID int64
	if err := tx.QueryRowContext(ctx, `SELECT structure_revision,status,coverage_event_id
		FROM usage_monitoring_rollup_state WHERE rollup_name=?`,
		usageevent.CodexLegacyIdentityRollupName).Scan(&revision, &status, &coverageID); err != nil {
		t.Fatal(err)
	}
	if revision != usageevent.CodexLegacyIdentityEvidenceRevision || status != "ready" || coverageID != latestID {
		t.Fatalf("non-empty Codex evidence state revision=%q status=%q coverage=%d, want 1/ready/%d",
			revision, status, coverageID, latestID)
	}
}
