package dialect_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotacooldown"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

type quotaFixture struct {
	db        *sql.DB
	backend   database.BackendKind
	cooldowns quotacooldown.Repository
	snapshots quotasnapshot.Repository
}

func TestSQLiteQuotaRepositoryConformance(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "quota.sqlite"))
	if err != nil {
		t.Fatalf("open SQLite quota fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runQuotaRepositoryConformance(t, newQuotaFixture(db, database.BackendSQLite))
}

func TestMySQLQuotaRepositoryConformance(t *testing.T) {
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
		t.Fatalf("open MySQL quota fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := schema.Ensure(ctx, db); err != nil {
		t.Fatalf("ensure MySQL quota schema: %v", err)
	}
	validation, err := schema.Validate(ctx, db)
	if err != nil {
		t.Fatalf("validate MySQL quota schema: %v", err)
	}
	if !validation.Valid {
		t.Fatalf("MySQL quota schema differences: %#v", validation.Differences)
	}
	fixture := newQuotaFixture(db, database.BackendMySQL)
	clearQuotaTables(t, fixture.db)
	t.Cleanup(func() { clearQuotaTables(t, fixture.db) })
	runQuotaRepositoryConformance(t, fixture)
}

func newQuotaFixture(db *sql.DB, backend database.BackendKind) quotaFixture {
	return quotaFixture{
		db:        db,
		backend:   backend,
		cooldowns: quotacooldown.NewForBackend(db, backend),
		snapshots: quotasnapshot.NewForBackend(db, backend),
	}
}

func runQuotaRepositoryConformance(t *testing.T, fixture quotaFixture) {
	t.Helper()
	clearQuotaTables(t, fixture.db)
	t.Run("cooldowns preserve fields transitions identities and concurrency", func(t *testing.T) {
		testQuotaCooldownConformance(t, fixture)
	})
	clearQuotaTables(t, fixture.db)
	t.Run("snapshots preserve fields lifecycle idempotency and concurrency", func(t *testing.T) {
		testQuotaSnapshotConformance(t, fixture)
	})
}

func testQuotaCooldownConformance(t *testing.T, fixture quotaFixture) {
	t.Helper()
	ctx := context.Background()
	evidenceBytes, err := json.Marshal(map[string]string{
		"kind":   "配额冷却🔐",
		"detail": strings.Repeat("完整证据界 ", 8_000),
	})
	if err != nil {
		t.Fatal(err)
	}
	longEvidence := string(evidenceBytes)
	first, err := fixture.cooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName: "账户.json", AuthIndex: " auth-一 ", AccountSnapshot: "用户@example.com",
		Provider: " X_AI ", ReasonCode: "weekly_limit", WindowKind: "weekly",
		EvidenceJSON: longEvidence, RecoverAtMS: 2_000, Owner: model.QuotaCooldownOwnerUsage429,
		EventHash: "事件-一", PreDisabledState: true, DisabledAtMS: 200,
	})
	if err != nil {
		t.Fatalf("insert complete quota cooldown: %v", err)
	}
	if first.ID <= 0 || first.AuthFileName != "账户.json" || first.AuthIndex != "auth-一" ||
		first.AccountSnapshot != "用户@example.com" || first.Provider != "xai" ||
		first.ReasonCode != "weekly_limit" || first.WindowKind != "weekly" ||
		first.EvidenceJSON != longEvidence || first.RecoverAtMS != 2_000 ||
		first.Owner != model.QuotaCooldownOwnerUsage429 || first.EventHash != "事件-一" ||
		!first.PreDisabledState || first.DisabledAtMS != 200 || first.Status != model.QuotaCooldownStatusActive ||
		first.CreatedAtMS <= 0 || first.UpdatedAtMS <= 0 {
		t.Fatalf("complete quota cooldown mismatch: %#v", first)
	}
	shorter, err := fixture.cooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName: "账户.json", AuthIndex: "auth-一", AccountSnapshot: "重命名@example.com",
		Provider: "grok", ReasonCode: "shorter", WindowKind: "five_hour",
		EvidenceJSON: `{"shorter":true}`, RecoverAtMS: 1_000,
		Owner: model.QuotaCooldownOwnerUsage429, EventHash: "shorter", DisabledAtMS: 100,
	})
	if err != nil {
		t.Fatalf("merge shorter quota cooldown: %v", err)
	}
	if shorter.ID != first.ID || shorter.AccountSnapshot != "重命名@example.com" ||
		shorter.Provider != "xai" || shorter.RecoverAtMS != 2_000 || shorter.DisabledAtMS != 100 ||
		shorter.ReasonCode != first.ReasonCode || shorter.WindowKind != first.WindowKind ||
		shorter.EvidenceJSON != longEvidence || shorter.EventHash != first.EventHash ||
		shorter.PreDisabledState != first.PreDisabledState {
		t.Fatalf("shorter cooldown changed winning/origin fields: %#v", shorter)
	}
	due, err := fixture.cooldowns.ListDue(ctx, 1_500, 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("premature due cooldowns=%#v err=%v", due, err)
	}
	due, err = fixture.cooldowns.ListDue(ctx, 2_500, 10)
	if err != nil || len(due) != 1 || due[0].ID != first.ID {
		t.Fatalf("due cooldowns=%#v err=%v", due, err)
	}
	if err := fixture.cooldowns.RecordFailure(ctx, first.ID, "同步失败 🔐"); err != nil {
		t.Fatalf("record cooldown failure: %v", err)
	}
	active, err := fixture.cooldowns.ListActive(ctx)
	if err != nil || len(active) != 1 || active[0].LastError != "同步失败 🔐" {
		t.Fatalf("failed active cooldowns=%#v err=%v", active, err)
	}
	if err := fixture.cooldowns.MarkRecovered(ctx, first.ID, 3_000); err != nil {
		t.Fatalf("recover cooldown: %v", err)
	}
	active, err = fixture.cooldowns.ListActive(ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("active cooldowns after recovery=%#v err=%v", active, err)
	}

	const explicitID int64 = 8_000_000_000_000
	if _, err := fixture.db.ExecContext(ctx, `insert into quota_cooldowns (
		id, auth_file_name, auth_index, account_snapshot, provider, reason_code,
		window_kind, evidence_json, recover_at_ms, owner, event_hash,
		pre_disabled_state, status, disabled_at_ms, recovered_at_ms, last_error,
		created_at_ms, updated_at_ms
	) values (?, 'physical-null.json', null, '', null, '', null, null, 10,
		'usage_429', '', 0, 'recovered', 1, 2, '', 3, 4)`, explicitID); err != nil {
		t.Fatalf("insert explicit physical NULL cooldown: %v", err)
	}
	var authIndex, provider, windowKind, evidenceJSON sql.NullString
	var accountSnapshot, reasonCode, eventHash, lastError string
	if err := fixture.db.QueryRowContext(ctx, `select auth_index, account_snapshot, provider,
		reason_code, window_kind, evidence_json, event_hash, last_error
		from quota_cooldowns where id = ?`, explicitID).Scan(
		&authIndex, &accountSnapshot, &provider, &reasonCode,
		&windowKind, &evidenceJSON, &eventHash, &lastError,
	); err != nil {
		t.Fatalf("read physical NULL cooldown: %v", err)
	}
	if authIndex.Valid || provider.Valid || windowKind.Valid || evidenceJSON.Valid ||
		accountSnapshot != "" || reasonCode != "" || eventHash != "" || lastError != "" {
		t.Fatalf("quota cooldown NULL/empty semantics changed: %#v %#v %#v %#v %q %q %q %q",
			authIndex, provider, windowKind, evidenceJSON, accountSnapshot, reasonCode, eventHash, lastError)
	}
	afterExplicit, err := fixture.cooldowns.UpsertActive(ctx, model.QuotaCooldownUpsert{
		AuthFileName: "after-explicit.json", AuthIndex: "new", RecoverAtMS: 4_000,
		Owner: model.QuotaCooldownOwnerUsage429,
	})
	if err != nil || afterExplicit.ID <= explicitID {
		t.Fatalf("cooldown auto ID=%d after explicit ID=%d err=%v", afterExplicit.ID, explicitID, err)
	}

	concurrent := model.QuotaCooldownUpsert{
		AuthFileName: "concurrent.json", AuthIndex: "same", Provider: "codex",
		RecoverAtMS: 5_000, Owner: model.QuotaCooldownOwnerUsage429,
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := fixture.cooldowns.UpsertActive(ctx, concurrent)
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent cooldown upsert: %v", err)
		}
	}
	var concurrentRows int
	if err := fixture.db.QueryRowContext(ctx, `select count(*) from quota_cooldowns
		where auth_file_name = ? and auth_index = ? and status = 'active'`,
		concurrent.AuthFileName, concurrent.AuthIndex).Scan(&concurrentRows); err != nil || concurrentRows != 1 {
		t.Fatalf("concurrent cooldown rows=%d err=%v", concurrentRows, err)
	}
}

func testQuotaSnapshotConformance(t *testing.T, fixture quotaFixture) {
	t.Helper()
	ctx := context.Background()
	const explicitObservationID int64 = 7_000_000_000_000
	if _, err := fixture.db.ExecContext(ctx, `insert into account_quota_observations (
		id, observation_hash, account_key, provider, source, source_observation_id,
		inventory_scope_key, inventory_mode, observed_at_ms, window_count,
		lifecycle_applied, created_at_ms
	) values (?, 'explicit-observation', 'explicit', 'codex', 'inspection', null,
		'codex:rate-limits', 'partial', 1, 0, 0, 1)`, explicitObservationID); err != nil {
		t.Fatalf("insert explicit quota observation ID: %v", err)
	}

	cycleStart, cycleEnd, duration := int64(1_000), int64(3_601_000), int64(3_600)
	used, remaining, usedValue, limitValue := 12.5, 87.5, 125.25, 1_000.5
	resetCredits := int64(9)
	modelIDsBytes, _ := json.Marshal([]string{"模型-A", strings.Repeat("超长模型🔐", 4_000)})
	resetJSONBytes, _ := json.Marshal(map[string]string{"detail": strings.Repeat("额度界 ", 8_000)})
	modelIDsJSON := string(modelIDsBytes)
	resetJSON := string(resetJSONBytes)
	scope := quotasnapshot.ScopeFingerprint("models", "模型组-A", []string{"模型-A", strings.Repeat("超长模型🔐", 4_000)})
	write := quotaWrite("full-observation", "账户-配额", "codex", "weekly", 2_000)
	write.Observation.SourceObservationID = "来源-一"
	write.Snapshots[0].WindowKind = "weekly"
	write.Snapshots[0].WindowMode = "fixed"
	write.Snapshots[0].ModelScopeKind = "models"
	write.Snapshots[0].ModelScopeKey = "模型组-A"
	write.Snapshots[0].ModelIDsJSON = modelIDsJSON
	write.Snapshots[0].ScopeFingerprint = scope
	write.Snapshots[0].ContentHash = "内容-一"
	write.Snapshots[0].SourceObservationID = "来源-一"
	write.Snapshots[0].BoundaryAccuracy = "exact"
	write.Snapshots[0].CycleStartMS = &cycleStart
	write.Snapshots[0].CycleEndMS = &cycleEnd
	write.Snapshots[0].DurationSeconds = &duration
	write.Snapshots[0].UsedPercent = &used
	write.Snapshots[0].RemainingPercent = &remaining
	write.Snapshots[0].UsedValue = &usedValue
	write.Snapshots[0].LimitValue = &limitValue
	write.Snapshots[0].QuotaUnit = "请求🔐"
	write.Snapshots[0].ResetCreditsAvailable = &resetCredits
	write.Snapshots[0].ResetCreditsJSON = resetJSON
	write.Snapshots[0].PlanType = "企业版"
	if err := fixture.snapshots.InsertObservationWrites(ctx, []model.AccountQuotaObservationWrite{write}); err != nil {
		t.Fatalf("insert complete quota snapshot: %v", err)
	}
	if err := fixture.snapshots.InsertObservationWrites(ctx, []model.AccountQuotaObservationWrite{write}); err != nil {
		t.Fatalf("repeat quota snapshot observation: %v", err)
	}
	var observationID int64
	var observationRows, snapshotRows int
	if err := fixture.db.QueryRowContext(ctx, `select id from account_quota_observations
		where observation_hash = ?`, write.Observation.ObservationHash).Scan(&observationID); err != nil {
		t.Fatalf("read quota observation ID: %v", err)
	}
	if observationID <= explicitObservationID {
		t.Fatalf("quota observation auto ID=%d after explicit ID=%d", observationID, explicitObservationID)
	}
	if err := fixture.db.QueryRowContext(ctx, `select count(*) from account_quota_observations
		where observation_hash = ?`, write.Observation.ObservationHash).Scan(&observationRows); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRowContext(ctx, `select count(*) from account_quota_snapshots
		where observation_id = ?`, observationID).Scan(&snapshotRows); err != nil {
		t.Fatal(err)
	}
	if observationRows != 1 || snapshotRows != 1 {
		t.Fatalf("repeated observation rows=%d snapshots=%d, want 1/1", observationRows, snapshotRows)
	}
	candidates, err := fixture.snapshots.ListCandidates(ctx, "账户-配额", "codex", 10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("complete snapshot candidates=%#v err=%v", candidates, err)
	}
	got := candidates[0]
	if got.ObservationID != observationID || got.AccountKey != "账户-配额" || got.Provider != "codex" ||
		got.ProviderWindowID != "weekly" || got.WindowKind != "weekly" || got.WindowMode != "fixed" ||
		got.ModelScopeKind != "models" || got.ModelScopeKey != "模型组-A" || got.ModelIDsJSON != modelIDsJSON ||
		got.ScopeFingerprint != scope || got.ContentHash != "内容-一" || got.Source != "inspection" ||
		got.SourceObservationID != "来源-一" || got.ObservedAtMS != 2_000 || got.BoundaryAccuracy != "exact" ||
		!equalInt64(got.CycleStartMS, cycleStart) || !equalInt64(got.CycleEndMS, cycleEnd) ||
		!equalInt64(got.DurationSeconds, duration) || !equalFloat64(got.UsedPercent, used) ||
		!equalFloat64(got.RemainingPercent, remaining) || !equalFloat64(got.UsedValue, usedValue) ||
		!equalFloat64(got.LimitValue, limitValue) || got.QuotaUnit != "请求🔐" ||
		!equalInt64(got.ResetCreditsAvailable, resetCredits) || got.ResetCreditsJSON != resetJSON ||
		got.PlanType != "企业版" || got.CreatedAtMS != 2_000 {
		t.Fatalf("complete quota snapshot mismatch: %#v", got)
	}
	assertQuotaLifecycleTransition(t, fixture, write, cycleEnd, duration)
	assertQuotaSnapshotPhysicalNulls(t, fixture, observationID)
	assertConcurrentQuotaLifecycle(t, fixture)
}

func assertQuotaLifecycleTransition(
	t *testing.T,
	fixture quotaFixture,
	first model.AccountQuotaObservationWrite,
	firstEnd, duration int64,
) {
	t.Helper()
	ctx := context.Background()
	nextStart := firstEnd
	nextEnd := firstEnd + duration*1_000
	used := 1.0
	next := quotaWrite("next-cycle", first.Observation.AccountKey, first.Observation.Provider, "weekly", firstEnd+10)
	next.Observation.SourceObservationID = "来源-二"
	next.Snapshots[0] = first.Snapshots[0]
	next.Snapshots[0].SourceObservationID = "来源-二"
	next.Snapshots[0].ObservedAtMS = firstEnd + 10
	next.Snapshots[0].CreatedAtMS = firstEnd + 10
	next.Snapshots[0].ContentHash = "内容-二"
	next.Snapshots[0].CycleStartMS = &nextStart
	next.Snapshots[0].CycleEndMS = &nextEnd
	next.Snapshots[0].DurationSeconds = &duration
	next.Snapshots[0].UsedPercent = &used
	if err := fixture.snapshots.InsertObservationWrites(ctx, []model.AccountQuotaObservationWrite{next}); err != nil {
		t.Fatalf("advance quota lifecycle: %v", err)
	}
	states, err := fixture.snapshots.ListWindowStates(ctx, first.Observation.AccountKey, first.Observation.Provider)
	if err != nil || len(states) != 1 || states[0].CurrentCycle == nil || states[0].PreviousCycle == nil {
		t.Fatalf("quota lifecycle states=%#v err=%v", states, err)
	}
	if states[0].Availability != "active" || states[0].Generation != 1 ||
		states[0].CurrentCycle.ActualStartMS != nextStart ||
		states[0].PreviousCycle.ActualEndMS == nil || *states[0].PreviousCycle.ActualEndMS != firstEnd ||
		states[0].PreviousCycle.EndReason != "scheduled" {
		t.Fatalf("quota lifecycle transition mismatch: %#v", states[0])
	}
	var cycles int
	if err := fixture.db.QueryRowContext(ctx, `select count(*) from account_quota_cycles
		where activation_id = ?`, states[0].ActivationID).Scan(&cycles); err != nil || cycles != 2 {
		t.Fatalf("quota cycle history=%d err=%v", cycles, err)
	}
}

func assertQuotaSnapshotPhysicalNulls(t *testing.T, fixture quotaFixture, _ int64) {
	t.Helper()
	ctx := context.Background()
	var modelScopeKey, modelIDsJSON, sourceObservationID, quotaUnit, resetJSON, planType sql.NullString
	var cycleStart, usedPercent, resetCredits sql.NullInt64
	nullWrite := quotaWrite("null-optionals", "null-account", "codex", "rolling", 20_000)
	nullWrite.Snapshots[0].WindowMode = "rolling"
	if err := fixture.snapshots.InsertObservationWrites(ctx, []model.AccountQuotaObservationWrite{nullWrite}); err != nil {
		t.Fatalf("insert NULL optional quota snapshot: %v", err)
	}
	if err := fixture.db.QueryRowContext(ctx, `select model_scope_key, model_ids_json,
		source_observation_id, quota_unit, reset_credits_json, plan_type,
		cycle_start_ms, used_percent, reset_credits_available
		from account_quota_snapshots where account_key = ?`, "null-account").Scan(
		&modelScopeKey, &modelIDsJSON, &sourceObservationID, &quotaUnit, &resetJSON,
		&planType, &cycleStart, &usedPercent, &resetCredits,
	); err != nil {
		t.Fatalf("read NULL optional quota snapshot: %v", err)
	}
	if modelScopeKey.Valid || modelIDsJSON.Valid || sourceObservationID.Valid || quotaUnit.Valid ||
		resetJSON.Valid || planType.Valid || cycleStart.Valid || usedPercent.Valid || resetCredits.Valid {
		t.Fatalf("quota snapshot optional values were not NULL: %#v %#v %#v %#v %#v %#v %#v %#v %#v",
			modelScopeKey, modelIDsJSON, sourceObservationID, quotaUnit, resetJSON, planType,
			cycleStart, usedPercent, resetCredits)
	}
}

func assertConcurrentQuotaLifecycle(t *testing.T, fixture quotaFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	writes := []model.AccountQuotaObservationWrite{
		quotaWrite("concurrent-old", "concurrent-account", "codex", "rolling", 30_000),
		quotaWrite("concurrent-new", "concurrent-account", "codex", "rolling", 31_000),
	}
	start := make(chan struct{})
	errs := make(chan error, len(writes))
	var wait sync.WaitGroup
	for index := range writes {
		write := writes[index]
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errs <- fixture.snapshots.InsertObservationWrites(ctx, []model.AccountQuotaObservationWrite{write})
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent quota lifecycle write: %v", err)
		}
	}
	var observations, windows, activeActivations int
	if err := fixture.db.QueryRowContext(ctx, `select count(*) from account_quota_observations
		where account_key = ?`, "concurrent-account").Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRowContext(ctx, `select count(*) from account_quota_windows
		where account_key = ?`, "concurrent-account").Scan(&windows); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRowContext(ctx, `select count(*)
		from account_quota_window_activations activation
		join account_quota_windows quota_window on quota_window.id = activation.window_id
		where quota_window.account_key = ? and activation.deactivated_at_ms is null`,
		"concurrent-account").Scan(&activeActivations); err != nil {
		t.Fatal(err)
	}
	if observations != 2 || windows != 1 || activeActivations != 1 {
		t.Fatalf("concurrent quota lifecycle observations/windows/active=%d/%d/%d, want 2/1/1",
			observations, windows, activeActivations)
	}
}

func quotaWrite(hash, accountKey, provider, providerWindowID string, observedAtMS int64) model.AccountQuotaObservationWrite {
	scope := quotasnapshot.ScopeFingerprint("all", "", nil)
	return model.AccountQuotaObservationWrite{
		Observation: model.AccountQuotaObservation{
			ObservationHash: hash, AccountKey: accountKey, Provider: provider,
			Source: "inspection", InventoryScopeKey: quotasnapshot.InventoryScopeKey(provider),
			InventoryMode: "partial", ObservedAtMS: observedAtMS, WindowCount: 1,
			CreatedAtMS: observedAtMS,
		},
		Snapshots: []model.AccountQuotaSnapshot{{
			AccountKey: accountKey, Provider: provider, ProviderWindowID: providerWindowID,
			WindowKind: "unknown", WindowMode: "unknown", ModelScopeKind: "all",
			ScopeFingerprint: scope, ContentHash: fmt.Sprintf("content:%s", hash),
			InventoryScopeKey: quotasnapshot.InventoryScopeKey(provider), Source: "inspection",
			ObservedAtMS: observedAtMS, BoundaryAccuracy: "unknown", CreatedAtMS: observedAtMS,
		}},
	}
}

func equalInt64(value *int64, want int64) bool {
	return value != nil && *value == want
}

func equalFloat64(value *float64, want float64) bool {
	return value != nil && *value == want
}

func clearQuotaTables(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	statements := []string{
		`delete from account_quota_snapshots`,
		`update account_quota_cycles set parent_cycle_id = null`,
		`delete from account_quota_cycles`,
		`delete from account_quota_window_activations`,
		`delete from account_quota_windows`,
		`delete from account_quota_observations`,
		`delete from quota_cooldowns`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("clear quota fixture with %q: %v", statement, err)
		}
	}
}
