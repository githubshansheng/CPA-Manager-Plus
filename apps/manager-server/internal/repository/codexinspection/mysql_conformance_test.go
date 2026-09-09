package codexinspection_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	inspectionrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/codexinspection"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	storepkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type inspectionFixture struct {
	db      *sql.DB
	backend database.BackendKind
	repo    inspectionrepo.Repository
}

func TestSQLiteCodexInspectionConformance(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "inspection-conformance.sqlite"))
	if err != nil {
		t.Fatalf("open SQLite fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runInspectionConformance(t, newInspectionFixture(db, database.BackendSQLite))
}

func TestMySQLCodexInspectionConformance(t *testing.T) {
	if os.Getenv("CPAMP_MYSQL_INTEGRATION") != "1" {
		t.Skip("set CPAMP_MYSQL_INTEGRATION=1 to run against supported MySQL 8.x (8.0.12 minimum)")
	}
	config := mysqlInspectionConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := dbmysql.Test(ctx, config); err != nil {
		t.Fatalf("validate official MySQL target (MariaDB is unsupported): %v", err)
	}
	db, err := dbmysql.Open(ctx, config)
	if err != nil {
		t.Fatalf("open MySQL integration target: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := schema.Ensure(ctx, db); err != nil {
		t.Fatalf("ensure MySQL schema: %v", err)
	}
	validation, err := schema.Validate(ctx, db)
	if err != nil {
		t.Fatalf("validate MySQL schema: %v", err)
	}
	if !validation.Valid {
		t.Fatalf("MySQL schema differences: %#v", validation.Differences)
	}
	clearInspectionTables(t, db)
	t.Cleanup(func() { clearInspectionTables(t, db) })
	runInspectionConformance(t, newInspectionFixture(db, database.BackendMySQL))
}

func newInspectionFixture(db *sql.DB, backend database.BackendKind) inspectionFixture {
	configuredStore := storepkg.NewWithBackend(database.NewSQLBackend(backend, db))
	return inspectionFixture{db: db, backend: backend, repo: configuredStore.CodexInspections}
}

func runInspectionConformance(t *testing.T, fixture inspectionFixture) {
	t.Helper()
	clearInspectionTables(t, fixture.db)
	t.Run("complete repository fields and relationships", func(t *testing.T) {
		testInspectionRepositoryConformance(t, fixture)
	})
	clearInspectionTables(t, fixture.db)
	t.Run("lease lifecycle fencing and terminal states", func(t *testing.T) {
		testInspectionLifecycleConformance(t, fixture)
	})
}

func testInspectionRepositoryConformance(t *testing.T, fixture inspectionFixture) {
	ctx := context.Background()
	settingsJSON := "{\"说明\":\"" + strings.Repeat("完整配置🔐", 2_000) + "\"}"
	triggerKey := "计划-" + strings.Repeat("界", 1_000)
	run, err := fixture.repo.CreateRun(ctx, model.CodexInspectionRun{
		TriggerType: model.CodexInspectionTriggerScheduled, TriggerKey: triggerKey,
		Status: model.CodexInspectionStatusCompleted, StartedAtMS: 100, FinishedAtMS: 200,
		TotalFiles: 1, ProbeSetCount: 2, SampledCount: 3, DisabledCount: 4, EnabledCount: 5,
		DeleteCount: 6, DisableCount: 7, EnableCount: 8, ReauthCount: 9, KeepCount: 10,
		Error: "历史错误 " + strings.Repeat("详情", 2_000), SettingsJSON: settingsJSON, CreatedAtMS: 90,
	})
	if err != nil {
		t.Fatalf("create complete historical run: %v", err)
	}
	storedRun, found, err := fixture.repo.GetRun(ctx, run.ID)
	if err != nil || !found || storedRun.TriggerKey != triggerKey || storedRun.Status != model.CodexInspectionStatusCompleted ||
		storedRun.StartedAtMS != 100 || storedRun.FinishedAtMS != 200 || storedRun.TotalFiles != 1 ||
		storedRun.ProbeSetCount != 2 || storedRun.SampledCount != 3 || storedRun.DisabledCount != 4 ||
		storedRun.EnabledCount != 5 || storedRun.DeleteCount != 6 || storedRun.DisableCount != 7 ||
		storedRun.EnableCount != 8 || storedRun.ReauthCount != 9 || storedRun.KeepCount != 10 ||
		storedRun.Error != run.Error || storedRun.SettingsJSON != settingsJSON || storedRun.CreatedAtMS != 90 {
		t.Fatalf("complete run mismatch: id=%d found=%v err=%v trigger=%d/%d settings=%d/%d counts=%d,%d,%d,%d,%d",
			run.ID, found, err, len(storedRun.TriggerKey), len(triggerKey), len(storedRun.SettingsJSON),
			len(settingsJSON), storedRun.TotalFiles, storedRun.ProbeSetCount, storedRun.SampledCount,
			storedRun.DisabledCount, storedRun.KeepCount)
	}

	storedRun.TotalFiles = 11
	storedRun.ProbeSetCount = 12
	storedRun.SampledCount = 13
	storedRun.DisabledCount = 14
	storedRun.EnabledCount = 15
	storedRun.DeleteCount = 16
	storedRun.DisableCount = 17
	storedRun.EnableCount = 18
	storedRun.ReauthCount = 19
	storedRun.KeepCount = 20
	storedRun.Error = "更新后的终态"
	storedRun.SettingsJSON = `{"updated":true}`
	if err := fixture.repo.UpdateRun(ctx, storedRun); err != nil {
		t.Fatalf("update terminal summary: %v", err)
	}
	if err := fixture.repo.UpdateRun(ctx, storedRun); err != nil {
		t.Fatalf("repeat terminal summary update: %v", err)
	}
	transition := storedRun
	transition.Status = model.CodexInspectionStatusFailed
	if err := fixture.repo.UpdateRun(ctx, transition); !errors.Is(err, inspectionrepo.ErrRunStateConflict) {
		t.Fatalf("terminal state regression error=%v", err)
	}
	if latest, ok, err := fixture.repo.GetLatestRunByTrigger(ctx, run.TriggerType, run.TriggerKey); err != nil || !ok || latest.ID != run.ID {
		t.Fatalf("latest trigger run=%#v found=%v err=%v", latest, ok, err)
	}
	if latest, ok, err := fixture.repo.GetLatestRunByTriggerType(ctx, run.TriggerType); err != nil || !ok || latest.ID != run.ID {
		t.Fatalf("latest trigger type run=%#v found=%v err=%v", latest, ok, err)
	}
	if runs, err := fixture.repo.ListRuns(ctx, 1); err != nil || len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("list runs=%#v err=%v", runs, err)
	}

	const explicitID int64 = 8_000_000_000_000
	if _, err := fixture.db.ExecContext(ctx, `insert into codex_inspection_runs
		(id, trigger_type, trigger_key, status, started_at_ms, finished_at_ms, settings_json, created_at_ms, updated_at_ms)
		values(?, 'manual', null, 'completed', 1, 2, '{}', 1, 2)`, explicitID); err != nil {
		t.Fatalf("insert explicit run ID: %v", err)
	}
	afterExplicit, err := fixture.repo.CreateRun(ctx, model.CodexInspectionRun{
		TriggerType: model.CodexInspectionTriggerManual, Status: model.CodexInspectionStatusCompleted,
		StartedAtMS: 3, FinishedAtMS: 4, SettingsJSON: `{}`, CreatedAtMS: 3,
	})
	if err != nil || afterExplicit.ID <= explicitID {
		t.Fatalf("run auto ID=%d after explicit=%d err=%v", afterExplicit.ID, explicitID, err)
	}

	statusCode := 503
	usedPercent := 99.875
	quotaUsed := 42.5
	resultInput := model.CodexInspectionResult{
		RunID: run.ID, AccountKey: "account-" + strings.Repeat("键", 1_000),
		FileName: "凭证.json", DisplayAccount: "用户@example.com", AccountSnapshot: "快照用户",
		AuthIndex: "auth-一", AccountID: "account-一", Provider: "xai", Disabled: true,
		Status: "429", State: "限流", Action: model.CodexInspectionAutoActionDisable,
		ActionReason: "原因 " + strings.Repeat("长文本", 2_000), ActionStatus: model.CodexInspectionActionStatusSuccess,
		ExecutedAction: "disable", ActionError: "动作详情", StatusCode: &statusCode, UsedPercent: &usedPercent,
		IsQuota: true, AutoRecoverEligible: true, Error: "上游错误", PlanType: "team",
		QuotaInventoryObserved: true,
		QuotaWindows: []model.CodexInspectionQuotaWindow{{ID: "primary", LabelKey: "额度", UsedPercent: &quotaUsed,
			ResetAtMS: 123_456, ResetAccuracy: "exact"}},
		ErrorKind: "rate_limit", ErrorDetail: "完整错误详情", CreatedAtMS: 300,
	}
	insertedResult, err := fixture.repo.InsertResult(ctx, resultInput)
	if err != nil {
		t.Fatalf("insert complete result: %v", err)
	}
	results, err := fixture.repo.ListResults(ctx, run.ID)
	if err != nil || len(results) != 1 {
		t.Fatalf("list complete result count=%d err=%v", len(results), err)
	}
	gotResult := results[0]
	if gotResult.ID != insertedResult.ID || gotResult.AccountKey != resultInput.AccountKey ||
		gotResult.FileName != resultInput.FileName || gotResult.DisplayAccount != resultInput.DisplayAccount ||
		gotResult.AccountSnapshot != resultInput.AccountSnapshot || gotResult.AuthIndex != resultInput.AuthIndex ||
		gotResult.AccountID != resultInput.AccountID || gotResult.Provider != resultInput.Provider || !gotResult.Disabled ||
		gotResult.Status != resultInput.Status || gotResult.State != resultInput.State || gotResult.Action != resultInput.Action ||
		gotResult.ActionReason != resultInput.ActionReason || gotResult.ActionStatus != resultInput.ActionStatus ||
		gotResult.ExecutedAction != resultInput.ExecutedAction || gotResult.ActionError != resultInput.ActionError ||
		gotResult.StatusCode == nil || *gotResult.StatusCode != statusCode || gotResult.UsedPercent == nil ||
		*gotResult.UsedPercent != usedPercent || !gotResult.IsQuota || !gotResult.AutoRecoverEligible ||
		gotResult.Error != resultInput.Error || gotResult.PlanType != resultInput.PlanType ||
		!gotResult.QuotaInventoryObserved || len(gotResult.QuotaWindows) != 1 ||
		gotResult.ErrorKind != resultInput.ErrorKind || gotResult.ErrorDetail != resultInput.ErrorDetail ||
		gotResult.CreatedAtMS != resultInput.CreatedAtMS {
		t.Fatalf("complete result mismatch: id=%d/%d key=%d/%d reason=%d/%d quota=%v windows=%d",
			gotResult.ID, insertedResult.ID, len(gotResult.AccountKey), len(resultInput.AccountKey),
			len(gotResult.ActionReason), len(resultInput.ActionReason), gotResult.QuotaInventoryObserved,
			len(gotResult.QuotaWindows))
	}

	updatedResult, err := fixture.repo.InsertResult(ctx, model.CodexInspectionResult{
		RunID: run.ID, AccountKey: resultInput.AccountKey, FileName: "更新.json", DisplayAccount: "更新用户",
		Action: model.CodexInspectionAutoActionNone, CreatedAtMS: 400,
	})
	if err != nil || updatedResult.ID != insertedResult.ID {
		t.Fatalf("upsert result id=%d/%d err=%v", updatedResult.ID, insertedResult.ID, err)
	}
	results, err = fixture.repo.ListResults(ctx, run.ID)
	if err != nil || len(results) != 1 || results[0].FileName != "更新.json" ||
		results[0].QuotaWindowsJSON == "" || !results[0].QuotaInventoryObserved || len(results[0].QuotaWindows) != 1 {
		t.Fatalf("upserted result count=%d quotaPreserved=%v windows=%d err=%v",
			len(results), len(results) == 1 && results[0].QuotaWindowsJSON != "",
			func() int {
				if len(results) == 1 {
					return len(results[0].QuotaWindows)
				}
				return 0
			}(), err)
	}
	testConcurrentInspectionResult(t, fixture, run.ID)

	testInspectionPhysicalNullEmptyAndIDs(t, fixture, run.ID, explicitID)
	testInspectionLogs(t, fixture, run.ID, explicitID)
	testDisableOwnershipConformance(t, fixture)

	if _, err := fixture.db.ExecContext(ctx, `delete from codex_inspection_runs where id = ?`, run.ID); err != nil {
		t.Fatalf("delete run for cascade check: %v", err)
	}
	for _, table := range []string{"codex_inspection_results", "codex_inspection_logs"} {
		var count int
		if err := fixture.db.QueryRowContext(ctx, `select count(*) from `+table+` where run_id = ?`, run.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s cascade count=%d err=%v", table, count, err)
		}
	}
}

func testConcurrentInspectionResult(t *testing.T, fixture inspectionFixture, runID int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	input := model.CodexInspectionResult{
		RunID: runID, AccountKey: "concurrent-account", FileName: "concurrent.json",
		DisplayAccount: "并发账户", Action: model.CodexInspectionAutoActionNone, CreatedAtMS: 450,
	}
	start := make(chan struct{})
	ids := make(chan int64, 2)
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			item, err := fixture.repo.InsertResult(ctx, input)
			ids <- item.ID
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent result upsert: %v", err)
		}
	}
	var firstID int64
	for id := range ids {
		if firstID == 0 {
			firstID = id
		} else if id != firstID {
			t.Fatalf("concurrent result IDs differ: %d/%d", firstID, id)
		}
	}
	var count int
	if err := fixture.db.QueryRowContext(ctx, `select count(*) from codex_inspection_results
		where run_id = ? and account_key = ?`, runID, input.AccountKey).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent result count=%d err=%v", count, err)
	}
}

func testInspectionPhysicalNullEmptyAndIDs(t *testing.T, fixture inspectionFixture, runID, explicitID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := fixture.db.ExecContext(ctx, `insert into codex_inspection_results (
		id, run_id, account_key, file_name, display_account, account_snapshot, auth_index, account_id,
		provider, status, state, action, action_reason, action_status, executed_action, action_error,
		plan_type, quota_windows_json, error_kind, error_detail, created_at_ms
	) values (?, ?, 'physical-null-empty', 'physical.json', 'physical', null, '', null, '', '', null,
		'none', null, '', null, '', null, '', null, '', 500)`, explicitID, runID); err != nil {
		t.Fatalf("insert physical NULL/empty result: %v", err)
	}
	var accountSnapshot, accountID, state, actionReason, executedAction, planType, errorKind sql.NullString
	var authIndex, provider, status, actionStatus, actionError, quotaJSON, errorDetail string
	if err := fixture.db.QueryRowContext(ctx, `select account_snapshot, auth_index, account_id, provider,
		status, state, action_reason, action_status, executed_action, action_error, plan_type,
		quota_windows_json, error_kind, error_detail from codex_inspection_results where id = ?`, explicitID).Scan(
		&accountSnapshot, &authIndex, &accountID, &provider, &status, &state, &actionReason,
		&actionStatus, &executedAction, &actionError, &planType, &quotaJSON, &errorKind, &errorDetail,
	); err != nil {
		t.Fatalf("read physical NULL/empty result: %v", err)
	}
	if accountSnapshot.Valid || accountID.Valid || state.Valid || actionReason.Valid || executedAction.Valid ||
		planType.Valid || errorKind.Valid || authIndex != "" || provider != "" || status != "" ||
		actionStatus != "" || actionError != "" || quotaJSON != "" || errorDetail != "" {
		t.Fatalf("physical NULL/empty result semantics changed")
	}
	after, err := fixture.repo.InsertResult(ctx, model.CodexInspectionResult{
		RunID: runID, AccountKey: "after-explicit", FileName: "after.json", DisplayAccount: "after",
		Action: model.CodexInspectionAutoActionNone, CreatedAtMS: 600,
	})
	if err != nil || after.ID <= explicitID {
		t.Fatalf("result auto ID=%d after explicit=%d err=%v", after.ID, explicitID, err)
	}
}

func testInspectionLogs(t *testing.T, fixture inspectionFixture, runID, explicitID int64) {
	t.Helper()
	ctx := context.Background()
	detailJSON := "{\"说明\":\"" + strings.Repeat("日志详情🔐", 2_000) + "\"}"
	entry, err := fixture.repo.InsertLog(ctx, model.CodexInspectionLog{
		RunID: runID, Level: "warning", Message: "日志 " + strings.Repeat("完整", 2_000),
		DetailJSON: detailJSON, CreatedAtMS: 700,
	})
	if err != nil {
		t.Fatalf("insert complete log: %v", err)
	}
	logs, err := fixture.repo.ListLogs(ctx, runID)
	if err != nil || len(logs) != 1 || logs[0].ID != entry.ID || logs[0].DetailJSON != detailJSON ||
		logs[0].Level != entry.Level || logs[0].Message != entry.Message || logs[0].CreatedAtMS != 700 {
		t.Fatalf("complete log count=%d detail=%d/%d err=%v", len(logs),
			func() int {
				if len(logs) == 1 {
					return len(logs[0].DetailJSON)
				}
				return 0
			}(), len(detailJSON), err)
	}
	if _, err := fixture.db.ExecContext(ctx, `insert into codex_inspection_logs
		(id, run_id, level, message, detail_json, created_at_ms) values(?, ?, 'info', '', null, 701)`,
		explicitID, runID); err != nil {
		t.Fatalf("insert explicit log ID: %v", err)
	}
	after, err := fixture.repo.InsertLog(ctx, model.CodexInspectionLog{
		RunID: runID, Level: "info", Message: "after", CreatedAtMS: 702,
	})
	if err != nil || after.ID <= explicitID {
		t.Fatalf("log auto ID=%d after explicit=%d err=%v", after.ID, explicitID, err)
	}
	var detail sql.NullString
	if err := fixture.db.QueryRowContext(ctx, `select detail_json from codex_inspection_logs where id = ?`, explicitID).Scan(&detail); err != nil || detail.Valid {
		t.Fatalf("explicit log detail=%#v err=%v", detail, err)
	}
}

func testDisableOwnershipConformance(t *testing.T, fixture inspectionFixture) {
	t.Helper()
	ctx := context.Background()
	fileName := "凭证-" + strings.Repeat("档", 1_000) + ".json"
	stable := model.CodexInspectionDisableOwnership{
		FileName: fileName, Provider: " X_AI ", AuthIndex: " auth-一 ",
		AccountID: "account-一", AccountSnapshot: "must-be-cleared", DisabledAtMS: 800,
	}
	if err := fixture.repo.UpsertDisableOwnership(ctx, stable); err != nil {
		t.Fatalf("insert ownership: %v", err)
	}
	stable.Provider = "xai"
	stable.AuthIndex = "auth-一"
	stable.AccountSnapshot = ""
	stable.DisabledAtMS = 801
	if err := fixture.repo.UpsertDisableOwnership(ctx, stable); err != nil {
		t.Fatalf("update ownership: %v", err)
	}
	wildcard := model.CodexInspectionDisableOwnership{FileName: "wildcard.json", DisabledAtMS: 802}
	if err := fixture.repo.UpsertDisableOwnerships(ctx, []model.CodexInspectionDisableOwnership{wildcard}); err != nil {
		t.Fatalf("insert wildcard ownership: %v", err)
	}
	items, err := fixture.repo.ListDisableOwnership(ctx)
	if err != nil || len(items) != 2 {
		t.Fatalf("ownership count=%d err=%v", len(items), err)
	}
	var storedStable model.CodexInspectionDisableOwnership
	for _, item := range items {
		if item.FileName == fileName {
			storedStable = item
		}
	}
	if storedStable.Provider != "xai" || storedStable.AuthIndex != "auth-一" ||
		storedStable.AccountID != "account-一" || storedStable.AccountSnapshot != "" ||
		storedStable.DisabledAtMS != 801 || storedStable.UpdatedAtMS <= 0 {
		t.Fatalf("stored ownership mismatch: provider=%q auth=%q account=%q snapshot=%q disabled=%d updated=%d",
			storedStable.Provider, storedStable.AuthIndex, storedStable.AccountID,
			storedStable.AccountSnapshot, storedStable.DisabledAtMS, storedStable.UpdatedAtMS)
	}
	provider, authIndex, accountID := "x_ai", "auth-一", "account-一"
	revoked, err := fixture.repo.RevokeDisableOwnership(ctx, []model.CodexInspectionDisableOwnershipTarget{{
		FileName: fileName, Provider: &provider, AuthIndex: &authIndex, AccountID: &accountID,
	}}, false)
	if err != nil || len(revoked) != 1 {
		t.Fatalf("revoke ownership count=%d err=%v", len(revoked), err)
	}
	if err := fixture.repo.RestoreDisableOwnership(ctx, revoked); err != nil {
		t.Fatalf("restore ownership: %v", err)
	}
	if err := fixture.repo.RestoreDisableOwnership(ctx, []model.CodexInspectionDisableOwnership{{
		FileName: fileName, Provider: "xai", AuthIndex: "auth-一", AccountID: "account-一", DisabledAtMS: 999,
	}}); err != nil {
		t.Fatalf("idempotent restore ownership: %v", err)
	}
	items, err = fixture.repo.ListDisableOwnership(ctx)
	if err != nil {
		t.Fatalf("list restored ownership: %v", err)
	}
	for _, item := range items {
		if item.FileName == fileName && item.DisabledAtMS != 801 {
			t.Fatalf("idempotent restore overwrote disabled timestamp: %d", item.DisabledAtMS)
		}
	}
	if err := fixture.repo.DeleteDisableOwnership(ctx, model.CodexInspectionDisableOwnershipTarget{FileName: wildcard.FileName}); err != nil {
		t.Fatalf("delete wildcard ownership: %v", err)
	}

	concurrent := model.CodexInspectionDisableOwnership{
		FileName: "concurrent.json", Provider: "codex", AuthIndex: "same", DisabledAtMS: 900,
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errs <- fixture.repo.UpsertDisableOwnership(ctx, concurrent)
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ownership upsert: %v", err)
		}
	}
	var count int
	if err := fixture.db.QueryRowContext(ctx, `select count(*) from codex_inspection_disable_ownership
		where file_name = ? and provider = ? and auth_index = ?`, concurrent.FileName,
		concurrent.Provider, concurrent.AuthIndex).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent ownership count=%d err=%v", count, err)
	}
}

func testInspectionLifecycleConformance(t *testing.T, fixture inspectionFixture) {
	ctx := context.Background()
	repositories := []inspectionrepo.Repository{
		fixture.repo,
		inspectionrepo.NewForBackend(fixture.db, fixture.backend),
	}
	start := make(chan struct{})
	results := make([]inspectionrepo.AcquireRunResult, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for index := range repositories {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errs[index] = repositories[index].AcquireRun(ctx,
				lifecycleRun(model.CodexInspectionTriggerManual, "concurrent"),
				"owner-"+strconv.Itoa(index), time.Minute)
		}(index)
	}
	close(start)
	wait.Wait()
	winner := -1
	activeErrors := 0
	for index, err := range errs {
		if err == nil {
			winner = index
		} else if errors.Is(err, inspectionrepo.ErrLeaseAlreadyActive) {
			activeErrors++
		} else {
			t.Fatalf("concurrent acquire %d error=%v", index, err)
		}
	}
	if winner < 0 || activeErrors != 1 {
		t.Fatalf("concurrent acquire winner=%d activeErrors=%d errors=%v", winner, activeErrors, errs)
	}
	ownerID := "owner-" + strconv.Itoa(winner)
	run := results[winner].Run
	if err := fixture.repo.HeartbeatRun(ctx, run.ID, ownerID, time.Minute); err != nil {
		t.Fatalf("immediate heartbeat: %v", err)
	}
	run.TotalFiles = 10
	run.SampledCount = 9
	if err := fixture.repo.UpdateRunProgress(ctx, run, ownerID); err != nil {
		t.Fatalf("update run progress: %v", err)
	}
	if err := fixture.repo.UpdateRunProgress(ctx, run, "wrong-owner"); !errors.Is(err, inspectionrepo.ErrLeaseLost) {
		t.Fatalf("wrong-owner progress error=%v", err)
	}
	lease, active, err := fixture.repo.GetActiveLease(ctx, time.Now().UnixMilli())
	if err != nil || !active || lease.RunID != run.ID || lease.OwnerID != ownerID || lease.LeaseExpiresAtMS <= lease.HeartbeatAtMS {
		t.Fatalf("active lease=%#v active=%v err=%v", lease, active, err)
	}
	if changed, err := fixture.repo.MarkRunCancelling(ctx, run.ID, ownerID, "用户取消"); err != nil || !changed {
		t.Fatalf("mark cancelling changed=%v err=%v", changed, err)
	}
	if changed, err := fixture.repo.MarkRunCancelling(ctx, run.ID, ownerID, "重复取消"); err != nil || !changed {
		t.Fatalf("repeat cancelling changed=%v err=%v", changed, err)
	}
	run.Status = model.CodexInspectionStatusCompleted
	run.FinishedAtMS = time.Now().UnixMilli()
	if err := fixture.repo.FinalizeRun(ctx, run, ownerID, &model.CodexInspectionLog{
		RunID: run.ID, Level: "success", Message: "done", Detail: map[string]any{"phase": "final"},
	}); err != nil {
		t.Fatalf("finalize cancelling run: %v", err)
	}
	stored, found, err := fixture.repo.GetRun(ctx, run.ID)
	if err != nil || !found || stored.Status != model.CodexInspectionStatusCancelled || stored.Error != "用户取消" {
		t.Fatalf("cancelled terminal run=%#v found=%v err=%v", stored, found, err)
	}
	if err := fixture.repo.FinalizeRun(ctx, run, ownerID, nil); !errors.Is(err, inspectionrepo.ErrLeaseLost) {
		t.Fatalf("repeat finalization error=%v", err)
	}
	if _, active, err := fixture.repo.GetActiveLease(ctx, time.Now().UnixMilli()); err != nil || active {
		t.Fatalf("lease after finalization active=%v err=%v", active, err)
	}

	expired, err := fixture.repo.AcquireRun(ctx, lifecycleRun(model.CodexInspectionTriggerManual, "expired"), "expire-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire expiring run: %v", err)
	}
	if _, err := fixture.db.ExecContext(ctx, `update codex_inspection_leases set heartbeat_at_ms = 0, lease_expires_at_ms = 0 where id = 1`); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	replacement, err := fixture.repo.AcquireRun(ctx, lifecycleRun(model.CodexInspectionTriggerManual, "replacement"), "expire-b", time.Minute)
	if err != nil || replacement.RecoveredRun == nil || replacement.RecoveredRun.ID != expired.Run.ID ||
		replacement.RecoveredRun.Status != model.CodexInspectionStatusInterrupted {
		t.Fatalf("replacement run=%#v recovered=%#v err=%v", replacement.Run, replacement.RecoveredRun, err)
	}
	replacement.Run.Status = model.CodexInspectionStatusCompleted
	if err := fixture.repo.FinalizeRun(ctx, replacement.Run, "expire-b", nil); err != nil {
		t.Fatalf("finalize replacement: %v", err)
	}

	forced, err := fixture.repo.AcquireRun(ctx, lifecycleRun(model.CodexInspectionTriggerManual, "force"), "force-owner", time.Minute)
	if err != nil {
		t.Fatalf("acquire force-finalize run: %v", err)
	}
	if _, err := fixture.db.ExecContext(ctx, `update codex_inspection_leases set lease_expires_at_ms = 0 where id = 1`); err != nil {
		t.Fatalf("expire force-finalize lease: %v", err)
	}
	forced.Run.Status = model.CodexInspectionStatusCompleted
	if err := fixture.repo.ForceFinalizeRun(ctx, forced.Run, "wrong-owner", nil); !errors.Is(err, inspectionrepo.ErrLeaseLost) {
		t.Fatalf("wrong-owner force finalization error=%v", err)
	}
	if err := fixture.repo.ForceFinalizeRun(ctx, forced.Run, "force-owner", &model.CodexInspectionLog{
		RunID: forced.Run.ID, Level: "success", Message: "forced",
	}); err != nil {
		t.Fatalf("force finalization: %v", err)
	}
	if stored, found, err := fixture.repo.GetRun(ctx, forced.Run.ID); err != nil || !found || stored.Status != model.CodexInspectionStatusCompleted {
		t.Fatalf("forced terminal run=%#v found=%v err=%v", stored, found, err)
	}

	res, err := fixture.db.ExecContext(ctx, `insert into codex_inspection_runs
		(trigger_type, trigger_key, status, started_at_ms, settings_json, created_at_ms, updated_at_ms)
		values('manual', 'orphan', 'running', 1000, '{}', 1000, 1000)`)
	if err != nil {
		t.Fatalf("insert orphan active run: %v", err)
	}
	orphanID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("orphan active run ID: %v", err)
	}
	recovered, err := fixture.repo.RecoverStaleRuns(ctx, 2_000, "startup recovery")
	if err != nil || len(recovered) != 1 || recovered[0].ID != orphanID ||
		recovered[0].Status != model.CodexInspectionStatusInterrupted {
		t.Fatalf("recovered stale runs=%#v err=%v", recovered, err)
	}

	scheduled, err := fixture.repo.AcquireRun(ctx,
		lifecycleRun(model.CodexInspectionTriggerScheduled, "scheduled-once"), "schedule-owner", time.Minute)
	if err != nil {
		t.Fatalf("acquire scheduled run: %v", err)
	}
	scheduled.Run.Status = model.CodexInspectionStatusCompleted
	if err := fixture.repo.FinalizeRun(ctx, scheduled.Run, "schedule-owner", nil); err != nil {
		t.Fatalf("finalize scheduled run: %v", err)
	}
	if _, err := fixture.repo.AcquireRun(ctx,
		lifecycleRun(model.CodexInspectionTriggerScheduled, "scheduled-once"), "schedule-owner-2", time.Minute); !errors.Is(err, inspectionrepo.ErrTriggerAlreadyExists) {
		t.Fatalf("duplicate scheduled trigger error=%v", err)
	}
	if _, active, err := fixture.repo.GetActiveLease(ctx, time.Now().UnixMilli()); err != nil || active {
		t.Fatalf("duplicate scheduled trigger stranded lease active=%v err=%v", active, err)
	}
}

func lifecycleRun(triggerType, triggerKey string) model.CodexInspectionRun {
	settings := model.DefaultCodexInspectionConfig()
	return model.CodexInspectionRun{
		TriggerType: triggerType, TriggerKey: triggerKey, Status: model.CodexInspectionStatusRunning,
		Settings: settings, SettingsJSON: model.MarshalCodexInspectionSettings(settings),
	}
}

func clearInspectionTables(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, table := range []string{
		"codex_inspection_logs", "codex_inspection_results", "codex_inspection_leases",
		"codex_inspection_runs", "codex_inspection_disable_ownership",
	} {
		if _, err := db.ExecContext(ctx, `delete from `+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
}

func mysqlInspectionConfig(t *testing.T) dbmysql.Config {
	t.Helper()
	port, err := strconv.Atoi(os.Getenv("CPAMP_MYSQL_TEST_PORT"))
	if err != nil {
		t.Fatal("CPAMP_MYSQL_TEST_PORT must be a valid port")
	}
	config := dbmysql.Config{
		Host: os.Getenv("CPAMP_MYSQL_TEST_HOST"), Port: port,
		Database: os.Getenv("CPAMP_MYSQL_TEST_DATABASE"), Username: os.Getenv("CPAMP_MYSQL_TEST_USERNAME"),
		Password: os.Getenv("CPAMP_MYSQL_TEST_PASSWORD"), TLSMode: dbmysql.TLSDisabled,
	}
	if config.Host == "" || config.Database == "" || config.Username == "" || config.Password == "" {
		t.Fatal("CPAMP_MYSQL_TEST_HOST/PORT/DATABASE/USERNAME/PASSWORD are required")
	}
	return config
}
