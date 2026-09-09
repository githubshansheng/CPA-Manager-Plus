package dialect_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/accountaction"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/deadletter"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	storepkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type runtimeFixture struct {
	db             *sql.DB
	deadLetters    deadletter.Repository
	accountActions accountaction.Repository
}

func TestSQLiteRuntimeRepositoryConformance(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "runtime.sqlite"))
	if err != nil {
		t.Fatalf("open SQLite fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runRuntimeRepositoryConformance(t, newRuntimeFixture(db, database.BackendSQLite))
}

func TestMySQLRuntimeRepositoryConformance(t *testing.T) {
	if os.Getenv("CPAMP_MYSQL_INTEGRATION") != "1" {
		t.Skip("set CPAMP_MYSQL_INTEGRATION=1 to run against supported MySQL 8.x (8.0.12 minimum)")
	}
	config := mysqlIntegrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := dbmysql.Test(ctx, config); err != nil {
		t.Fatalf("validate supported MySQL 8.x integration target (8.0.12 minimum; MariaDB is unsupported): %v", err)
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
		t.Fatalf("validate MySQL schema manifest: %v", err)
	}
	if !validation.Valid {
		t.Fatalf("MySQL schema manifest differences: %#v", validation.Differences)
	}
	clearRuntimeTables(t, db)
	t.Cleanup(func() { clearRuntimeTables(t, db) })
	runRuntimeRepositoryConformance(t, newRuntimeFixture(db, database.BackendMySQL))
}

func newRuntimeFixture(db *sql.DB, backend database.BackendKind) runtimeFixture {
	configuredStore := storepkg.NewWithBackend(database.NewSQLBackend(backend, db))
	return runtimeFixture{
		db:             db,
		deadLetters:    configuredStore.DeadLetters,
		accountActions: configuredStore.AccountActions,
	}
}

func runRuntimeRepositoryConformance(t *testing.T, fixture runtimeFixture) {
	t.Helper()
	clearRuntimeTables(t, fixture.db)
	t.Run("dead letters preserve complete rows and explicit ID watermarks", func(t *testing.T) {
		testDeadLetterConformance(t, fixture)
	})
	t.Run("account actions preserve fields identities transitions and concurrency", func(t *testing.T) {
		testAccountActionConformance(t, fixture)
	})
}

func testDeadLetterConformance(t *testing.T, fixture runtimeFixture) {
	ctx := context.Background()
	if err := fixture.deadLetters.Insert(ctx, "", ""); err != nil {
		t.Fatalf("insert empty dead letter: %v", err)
	}
	payload := "{\"类型\":\"失败\",\"detail\":\"" + strings.Repeat("数据🔐 ", 8_000) + "\"}  "
	errText := "上游错误: " + strings.Repeat("超长详情 ", 8_000) + "  "
	if err := fixture.deadLetters.Insert(ctx, payload, errText); err != nil {
		t.Fatalf("insert Unicode long dead letter: %v", err)
	}
	var storedPayload, storedError string
	var createdAtMS int64
	if err := fixture.db.QueryRowContext(ctx, `select payload, error, created_at_ms
		from dead_letter_events where payload = ?`, payload).Scan(
		&storedPayload, &storedError, &createdAtMS,
	); err != nil {
		t.Fatalf("read Unicode long dead letter: %v", err)
	}
	if storedPayload != payload || storedError != errText || createdAtMS <= 0 {
		t.Fatalf("dead letter field mismatch: payload=%d/%d error=%d/%d created=%d",
			len(storedPayload), len(payload), len(storedError), len(errText), createdAtMS)
	}

	const explicitID int64 = 9_000_000_000_000
	if _, err := fixture.db.ExecContext(ctx, `insert into dead_letter_events
		(id, payload, error, created_at_ms) values(?, ?, ?, ?)`, explicitID, "显式 ID", "", 77); err != nil {
		t.Fatalf("insert explicit dead letter ID: %v", err)
	}
	if err := fixture.deadLetters.Insert(ctx, "after explicit ID", "next"); err != nil {
		t.Fatalf("insert after explicit dead letter ID: %v", err)
	}
	var maxID int64
	if err := fixture.db.QueryRowContext(ctx, `select max(id) from dead_letter_events`).Scan(&maxID); err != nil {
		t.Fatalf("read dead letter ID watermark: %v", err)
	}
	count, err := fixture.deadLetters.Count(ctx)
	if err != nil || count != 4 || maxID <= explicitID {
		t.Fatalf("dead letter count=%d maxID=%d err=%v", count, maxID, err)
	}
	var explicitPayload, explicitError string
	var explicitCreated int64
	if err := fixture.db.QueryRowContext(ctx, `select payload, error, created_at_ms
		from dead_letter_events where id = ?`, explicitID).Scan(
		&explicitPayload, &explicitError, &explicitCreated,
	); err != nil || explicitPayload != "显式 ID" || explicitError != "" || explicitCreated != 77 {
		t.Fatalf("explicit dead letter payload=%q error=%q created=%d err=%v",
			explicitPayload, explicitError, explicitCreated, err)
	}
}

func testAccountActionConformance(t *testing.T, fixture runtimeFixture) {
	ctx := context.Background()
	longReason := "凭据需要人工复核 🔐 " + strings.Repeat("完整原因 ", 8_000) + "结束"
	longEvidence := "{\"source\":\"巡检\",\"detail\":\"" + strings.Repeat("证据界 ", 8_000) + "\"}"
	first, err := fixture.accountActions.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType: model.AccountActionTypeReview, Provider: " X_AI ", AuthFileName: " 账户.json ",
		AuthIndex: " auth-一 ", AccountSnapshot: "用户@example.com", AccountIDSnapshot: "账户-一",
		AuthLabel: "主账户 🔐", ReasonCode: "permission_denied", Reason: longReason,
		EvidenceJSON: longEvidence, SeenAtMS: 1_000,
	})
	if err != nil {
		t.Fatalf("insert complete account action: %v", err)
	}
	if first.ID <= 0 || first.Provider != "xai" || first.AuthFileName != "账户.json" ||
		first.AuthIndex != "auth-一" || first.AccountSnapshot != "用户@example.com" ||
		first.AccountIDSnapshot != "账户-一" || first.AuthLabel != "主账户 🔐" ||
		first.Reason != longReason || first.EvidenceJSON != longEvidence || first.HitCount != 1 ||
		first.FirstSeenAtMS != 1_000 || first.LastSeenAtMS != 1_000 || first.CreatedAtMS <= 0 {
		t.Fatalf("complete account action mismatch: id=%d provider=%q file=%q auth=%q account=%q/%q label=%q reason=%d/%d evidence=%d/%d hits=%d seen=%d/%d created=%d",
			first.ID, first.Provider, first.AuthFileName, first.AuthIndex, first.AccountSnapshot,
			first.AccountIDSnapshot, first.AuthLabel, len(first.Reason), len(longReason),
			len(first.EvidenceJSON), len(longEvidence), first.HitCount, first.FirstSeenAtMS,
			first.LastSeenAtMS, first.CreatedAtMS)
	}

	second, err := fixture.accountActions.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType: model.AccountActionTypeReview, Provider: "xai", AuthFileName: "账户.json",
		AuthIndex: "auth-一", ReasonCode: "permission_denied", AutoDisableEligible: true,
		SeenAtMS: 2_000,
	})
	if err != nil {
		t.Fatalf("merge account action: %v", err)
	}
	if second.ID != first.ID || second.HitCount != 2 || !second.AutoDisableEligible ||
		second.AccountSnapshot != first.AccountSnapshot || second.AccountIDSnapshot != first.AccountIDSnapshot ||
		second.AuthLabel != first.AuthLabel || second.Reason != longReason || second.EvidenceJSON != longEvidence {
		t.Fatalf("merged account action mismatch: id=%d/%d hits=%d eligible=%t account=%t/%t label=%t reason=%d/%d evidence=%d/%d",
			second.ID, first.ID, second.HitCount, second.AutoDisableEligible,
			second.AccountSnapshot == first.AccountSnapshot,
			second.AccountIDSnapshot == first.AccountIDSnapshot,
			second.AuthLabel == first.AuthLabel, len(second.Reason), len(longReason),
			len(second.EvidenceJSON), len(longEvidence))
	}
	third, err := fixture.accountActions.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType: model.AccountActionTypeReview, Provider: "xai", AuthFileName: "账户.json",
		AuthIndex: "auth-一", ReasonCode: "permission_denied", SeenAtMS: 3_000,
	})
	if err != nil || third.ID != first.ID || third.HitCount != 3 || !third.AutoDisableEligible {
		t.Fatalf("eligibility downgrade guard item=%#v err=%v", third, err)
	}

	fallback, err := fixture.accountActions.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType: model.AccountActionTypeReauth, Provider: "grok", AuthFileName: "fallback.json",
		AccountSnapshot: "fallback@example.com", ReasonCode: "revoked", SeenAtMS: 4_000,
	})
	if err != nil {
		t.Fatalf("insert fallback identity: %v", err)
	}
	upgraded, err := fixture.accountActions.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType: model.AccountActionTypeReauth, Provider: "x-ai", AuthFileName: "fallback.json",
		AuthIndex: "stable-auth", AccountSnapshot: "fallback@example.com", AccountIDSnapshot: "stable-account",
		ReasonCode: "revoked", SeenAtMS: 5_000,
	})
	if err != nil || upgraded.ID != fallback.ID || upgraded.AuthIndex != "stable-auth" ||
		upgraded.AccountIDSnapshot != "stable-account" || upgraded.Provider != "xai" {
		t.Fatalf("fallback upgrade item=%#v original=%#v err=%v", upgraded, fallback, err)
	}

	differentReason, err := fixture.accountActions.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType: model.AccountActionTypeReview, Provider: "xai", AuthFileName: "账户.json",
		AuthIndex: "auth-一", ReasonCode: "different_reason", SeenAtMS: 6_000,
	})
	if err != nil || differentReason.ID == first.ID {
		t.Fatalf("different reason merged item=%#v err=%v", differentReason, err)
	}
	providerIDs := make([]int64, 0, 2)
	for _, provider := range []string{"codex", "xai"} {
		item, err := fixture.accountActions.Upsert(ctx, model.AccountActionCandidateUpsert{
			ActionType: model.AccountActionTypeDelete, Provider: provider, AuthFileName: "provider.json",
			AccountIDSnapshot: "shared-account", ReasonCode: "shared", SeenAtMS: 7_000,
		})
		if err != nil {
			t.Fatalf("insert provider identity %s: %v", provider, err)
		}
		providerIDs = append(providerIDs, item.ID)
	}
	if providerIDs[0] == providerIDs[1] {
		t.Fatalf("provider-specific fallback identities merged into %d", providerIDs[0])
	}

	items, err := fixture.accountActions.List(ctx, model.AccountActionStatusPending, 2)
	count, countErr := fixture.accountActions.Count(ctx, model.AccountActionStatusPending)
	if err != nil || countErr != nil || len(items) != 2 || count != 5 {
		t.Fatalf("account action list=%d count=%d listErr=%v countErr=%v", len(items), count, err, countErr)
	}
	if err := fixture.accountActions.RecordFailure(ctx, first.ID, " 失败详情 🔐 "); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	failed, ok, err := fixture.accountActions.Get(ctx, first.ID)
	if err != nil || !ok || failed.LastError != "失败详情 🔐" {
		t.Fatalf("failed account action=%#v found=%v err=%v", failed, ok, err)
	}
	if err := fixture.accountActions.MarkAutoDisabled(ctx, first.ID, 8_000); err != nil {
		t.Fatalf("mark auto-disabled: %v", err)
	}
	if err := fixture.accountActions.MarkAutoDisabled(ctx, 9_000_000_000_000, 8_000); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("mark missing account action error=%v", err)
	}
	disabled, ok, err := fixture.accountActions.Get(ctx, first.ID)
	if err != nil || !ok || disabled.AutoDisabledAtMS != 8_000 || disabled.LastError != "" {
		t.Fatalf("auto-disabled account action=%#v found=%v err=%v", disabled, ok, err)
	}
	resolved, err := fixture.accountActions.UpdatePendingStatus(ctx, first.ID, model.AccountActionStatusResolved)
	if err != nil || resolved.Status != model.AccountActionStatusResolved {
		t.Fatalf("resolve account action=%#v err=%v", resolved, err)
	}
	if _, err := fixture.accountActions.UpdatePendingStatus(ctx, first.ID, model.AccountActionStatusIgnored); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second pending transition error=%v", err)
	}
	ignored, err := fixture.accountActions.UpdateStatus(ctx, differentReason.ID, model.AccountActionStatusIgnored)
	if err != nil || ignored.Status != model.AccountActionStatusIgnored {
		t.Fatalf("unconditional status transition=%#v err=%v", ignored, err)
	}
	if _, found, err := fixture.accountActions.Get(ctx, -1); err != nil || found {
		t.Fatalf("invalid ID found=%v err=%v", found, err)
	}

	testAccountActionPhysicalNullAndID(t, fixture)
	testConcurrentAccountActionUpsert(t, fixture)
}

func testAccountActionPhysicalNullAndID(t *testing.T, fixture runtimeFixture) {
	ctx := context.Background()
	const explicitID int64 = 9_000_000_000_000
	_, err := fixture.db.ExecContext(ctx, `insert into account_action_candidates (
		id, action_type, status, provider, auth_file_name, auth_index, account_snapshot,
		account_id_snapshot, auth_label, reason_code, reason, auto_disable_eligible,
		auto_disabled_at_ms, evidence_json, last_error, first_seen_at_ms, last_seen_at_ms,
		hit_count, created_at_ms, updated_at_ms
	) values (?, 'review', 'resolved', null, 'physical-null-empty.json', '', null, '', '',
		null, '', 0, null, null, '', 10, 20, 3, 30, 40)`, explicitID)
	if err != nil {
		t.Fatalf("insert explicit account action ID: %v", err)
	}
	var provider, accountSnapshot, reasonCode, evidenceJSON sql.NullString
	var autoDisabledAtMS sql.NullInt64
	var authIndex, accountIDSnapshot, authLabel, reason, lastError string
	if err := fixture.db.QueryRowContext(ctx, `select provider, auth_index, account_snapshot,
		account_id_snapshot, auth_label, reason_code, reason, auto_disabled_at_ms, evidence_json, last_error
		from account_action_candidates where id = ?`, explicitID).Scan(
		&provider, &authIndex, &accountSnapshot, &accountIDSnapshot, &authLabel,
		&reasonCode, &reason, &autoDisabledAtMS, &evidenceJSON, &lastError,
	); err != nil {
		t.Fatalf("read physical NULL/empty fields: %v", err)
	}
	if provider.Valid || accountSnapshot.Valid || reasonCode.Valid || autoDisabledAtMS.Valid || evidenceJSON.Valid ||
		authIndex != "" || accountIDSnapshot != "" || authLabel != "" || reason != "" || lastError != "" {
		t.Fatalf("physical NULL/empty semantics changed: provider=%#v authIndex=%q account=%#v/%q label=%q reason=%#v/%q disabled=%#v evidence=%#v error=%q",
			provider, authIndex, accountSnapshot, accountIDSnapshot, authLabel, reasonCode, reason,
			autoDisabledAtMS, evidenceJSON, lastError)
	}
	item, found, err := fixture.accountActions.Get(ctx, explicitID)
	if err != nil || !found || item.ID != explicitID || item.ActionType != model.AccountActionTypeReview ||
		item.Status != model.AccountActionStatusResolved || item.AuthFileName != "physical-null-empty.json" ||
		item.Provider != "" || item.AuthIndex != "" || item.AccountSnapshot != "" ||
		item.AccountIDSnapshot != "" || item.AuthLabel != "" || item.ReasonCode != "" ||
		item.Reason != "" || item.AutoDisableEligible || item.AutoDisabledAtMS != 0 ||
		item.EvidenceJSON != "" || item.LastError != "" || item.HitCount != 3 ||
		item.FirstSeenAtMS != 10 || item.LastSeenAtMS != 20 || item.CreatedAtMS != 30 || item.UpdatedAtMS != 40 {
		t.Fatalf("explicit account action=%#v found=%v err=%v", item, found, err)
	}
	after, err := fixture.accountActions.Upsert(ctx, model.AccountActionCandidateUpsert{
		ActionType: model.AccountActionTypeDelete, AuthFileName: "after-explicit.json",
		ReasonCode: "watermark", SeenAtMS: 50,
	})
	if err != nil || after.ID <= explicitID {
		t.Fatalf("account action auto ID=%d after explicit ID=%d err=%v", after.ID, explicitID, err)
	}
}

func testConcurrentAccountActionUpsert(t *testing.T, fixture runtimeFixture) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	input := model.AccountActionCandidateUpsert{
		ActionType: model.AccountActionTypeReauth, Provider: "codex", AuthFileName: "concurrent.json",
		AuthIndex: "same-auth", ReasonCode: "concurrent", Reason: "并发合并", SeenAtMS: 60,
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := fixture.accountActions.Upsert(ctx, input)
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent account action upsert: %v", err)
		}
	}
	var rows, hits int
	if err := fixture.db.QueryRowContext(ctx, `select count(*), coalesce(sum(hit_count), 0)
		from account_action_candidates where auth_file_name = ? and auth_index = ? and reason_code = ?`,
		input.AuthFileName, input.AuthIndex, input.ReasonCode).Scan(&rows, &hits); err != nil {
		t.Fatalf("read concurrent account action result: %v", err)
	}
	if rows != 1 || hits != 2 {
		t.Fatalf("concurrent account action rows=%d hits=%d, want 1/2", rows, hits)
	}
}

func clearRuntimeTables(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, table := range []string{"account_action_candidates", "dead_letter_events"} {
		if _, err := db.ExecContext(ctx, `delete from `+table); err != nil {
			t.Fatalf("runtime fixture cleanup %s: %v", table, err)
		}
	}
}
