package usageevent

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/dialect"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

// TestMySQLRepositoryConformance exercises every public production method
// against a real supported MySQL server. It never substitutes SQLite for the
// MySQL dialect. CI enables it for MySQL 8.0.12, 8.0.36, and 8.4.
func TestMySQLRepositoryConformance(t *testing.T) {
	if os.Getenv("CPAMP_MYSQL_INTEGRATION") != "1" {
		t.Skip("set CPAMP_MYSQL_INTEGRATION=1 to run the MySQL repository conformance suite")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db, err := dbmysql.Open(ctx, mysqlUsageEventTestConfig(t))
	if err != nil {
		t.Fatalf("open MySQL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := schema.Ensure(ctx, db); err != nil {
		t.Fatalf("ensure MySQL schema: %v", err)
	}
	repositoryValue, err := NewMySQL(db)
	if err != nil {
		t.Fatalf("construct MySQL usage repository: %v", err)
	}
	repo := repositoryValue

	unique := strconv.FormatInt(time.Now().UnixNano(), 36)
	prefix := "usageevent-mysql-conformance-" + unique
	baseMS := time.Now().UTC().Truncate(time.Hour).UnixMilli()
	latency, ttft, quota := int64(137), int64(41), 87.125
	longRawValue := strings.Repeat("雪", 20_000)
	longFailBody := strings.Repeat("failure-body-", 4_000)
	events := []model.UsageEvent{
		{
			RequestID: "请求-α  ", EventHash: prefix + "-full", TimestampMS: baseMS + 1,
			Timestamp: time.UnixMilli(baseMS + 1).UTC().Format(time.RFC3339Nano), Provider: "openai  ",
			ExecutorType: "codex", Model: "模型-alpha(high)", RequestedModel: "模型-alpha(high)",
			ResolvedModel: "模型-alpha", Endpoint: "/v1/responses/子串", Method: "POST", Path: "/任意/路径",
			ClientIP: "127.0.0.1", XForwardedFor: "2001:db8::1", UserAgent: "客户端/测试",
			AuthType: "bearer", AuthIndex: "auth-α", Source: "source  ", SourceHash: prefix + "-source",
			APIKeyHash: prefix + "-key", AccountSnapshot: "用户@example.invalid", AuthLabelSnapshot: "主账号",
			AuthFileSnapshot: prefix + ".json", AuthProviderSnapshot: "open_ai", AuthProjectIDSnapshot: "项目-α",
			AuthSnapshotAtMS: baseMS, ReasoningEffort: "high", ServiceTier: "priority",
			RequestServiceTier: "priority", ResponseServiceTier: "default", CacheInputMode: "separate",
			InputTokens: 101, OutputTokens: 17, ReasoningTokens: 3, CachedTokens: 11, CacheTokens: 13,
			CacheReadTokens: 7, CacheCreationTokens: 5, TotalTokens: 121, LatencyMS: &latency, TTFTMS: &ttft,
			Failed: true, FailStatusCode: 429, FailSummary: "quota exceeded", FailBody: longFailBody,
			ResponseMetadataJSON: `{"trace":{"primary_trace_id":"trace-full"}}`, HeaderQuotaRecoverAtMS: baseMS + 60_000,
			HeaderQuotaUsedPercent: &quota, HeaderQuotaPlanType: "pro", HeaderErrorKind: "rate_limit",
			HeaderErrorCode: "quota_exceeded", HeaderTraceID: "trace-full",
			RawJSON: `{"note":"` + longRawValue + `","response_headers":{"x-request-id":"trace-full"}}`, CreatedAtMS: baseMS + 2,
		},
		{
			EventHash: prefix + "-empty", TimestampMS: baseMS + 3,
			Timestamp: time.UnixMilli(baseMS + 3).UTC().Format(time.RFC3339Nano), Model: "zero-model",
			CreatedAtMS: baseMS + 4,
		},
		{
			EventHash: prefix + "-max", TimestampMS: baseMS - 86_400_000,
			Timestamp: time.UnixMilli(baseMS - 86_400_000).UTC().Format(time.RFC3339Nano), Model: "max-int-model",
			InputTokens: math.MaxInt64, TotalTokens: 1, CreatedAtMS: baseMS - 86_399_999,
		},
	}
	hashes := []string{events[0].EventHash, events[1].EventHash, events[2].EventHash, prefix + "-concurrent"}
	t.Cleanup(func() { cleanupMySQLUsageEvents(t, db, hashes) })
	result, err := repo.InsertBatch(ctx, events)
	if err != nil || result.Inserted != len(events) {
		t.Fatalf("InsertBatch result=%#v err=%v", result, err)
	}
	assertMySQLFullFields(t, ctx, db, events[0], events[1], events[2], longRawValue)
	duplicate, err := repo.InsertBatch(ctx, events[:1])
	if err != nil || duplicate.Inserted != 0 || duplicate.Skipped != 1 {
		t.Fatalf("duplicate InsertBatch result=%#v err=%v", duplicate, err)
	}
	testConcurrentMySQLInsert(t, ctx, repo, model.UsageEvent{
		EventHash: hashes[3], TimestampMS: baseMS + 5,
		Timestamp: time.UnixMilli(baseMS + 5).UTC().Format(time.RFC3339Nano), Model: "concurrent", CreatedAtMS: baseMS + 6,
	})

	filter := AnalyticsFilter{FromMS: baseMS, ToMS: baseMS + 60_000, SearchQuery: "任意/路", IncludeFailed: true}
	allFilter := AnalyticsFilter{FromMS: baseMS, ToMS: baseMS + 60_000, IncludeFailed: true}
	mustRepositoryCallsSucceed(t, ctx, repo, filter, allFilter, events[0])
	testMySQLCodexLegacyIdentityEvidence(t, ctx, db, prefix, baseMS)
	page, err := repo.EventsPageWithFilter(ctx, filter, 0, 0, 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].EventHash != events[0].EventHash {
		t.Fatalf("literal substring page=%#v err=%v", page, err)
	}
	shortFilter := filter
	shortFilter.SearchQuery = "子"
	shortPage, err := repo.EventsPageWithFilter(ctx, shortFilter, 0, 0, 10)
	if err != nil || len(shortPage.Items) != 1 {
		t.Fatalf("short substring page=%#v err=%v", shortPage, err)
	}
}

func testMySQLCodexLegacyIdentityEvidence(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	prefix string,
	baseMS int64,
) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	physicalFile := prefix + "-Codex.JSON"
	authIndex := prefix + "-AUTH"
	member := prefix + "@example.invalid"
	workspace := prefix + "-workspace"
	if _, err := tx.ExecContext(ctx, `insert into usage_events (
		event_hash,timestamp_ms,timestamp,model,created_at_ms,
		provider,auth_provider_snapshot,auth_file_snapshot,source,auth_index,
		auth_account_id_snapshot,account_snapshot
	) values
		(?,?,?,?,?,'codex','codex',?,?,?,?,?),
		(?,?,?,?,?,'codex','codex',?,?,?,?,?)`,
		prefix+"-identity-weak", baseMS+10, "weak", "gpt-test", baseMS+10,
		physicalFile, physicalFile, authIndex, "", member,
		prefix+"-identity-strong", baseMS+20, "strong", "gpt-test", baseMS+20,
		physicalFile, physicalFile, authIndex, workspace, member,
	); err != nil {
		t.Fatalf("insert MySQL Codex identity evidence: %v", err)
	}
	var latestID int64
	if err := tx.QueryRowContext(ctx, `select max(id) from usage_events`).Scan(&latestID); err != nil {
		t.Fatal(err)
	}
	queryTx := dialect.WrapTx(tx, dialect.MySQL())
	for attempt := 0; attempt < 2; attempt++ {
		if err := UpsertCodexLegacyIdentityEvidenceRange(ctx, queryTx,
			CodexLegacyIdentityEvidenceRevision, 0, latestID); err != nil {
			t.Fatalf("upsert MySQL Codex identity evidence attempt %d: %v", attempt+1, err)
		}
	}
	var groups int
	if err := tx.QueryRowContext(ctx, `select count(*) from usage_codex_legacy_identity_evidence_v1
		where physical_file=? and auth_index=?`, strings.ToLower(physicalFile),
		strings.ToLower(authIndex)).Scan(&groups); err != nil || groups != 2 {
		t.Fatalf("canonical MySQL Codex evidence groups=%d err=%v, want 2", groups, err)
	}
	if _, err := tx.ExecContext(ctx, `insert into usage_monitoring_rollup_state (
		rollup_name,schema_version,structure_revision,status,backfill_last_event_id,
		coverage_event_id,target_event_id,processed_events,updated_at_ms
	) values (?,1,?,'ready',?,?,?,2,?)
	ON DUPLICATE KEY UPDATE schema_version=VALUES(schema_version),
		structure_revision=VALUES(structure_revision),status=VALUES(status),
		backfill_last_event_id=VALUES(backfill_last_event_id),
		coverage_event_id=VALUES(coverage_event_id),target_event_id=VALUES(target_event_id),
		processed_events=VALUES(processed_events),updated_at_ms=VALUES(updated_at_ms)`,
		CodexLegacyIdentityRollupName, CodexLegacyIdentityEvidenceRevision,
		latestID, latestID, latestID, baseMS+30); err != nil {
		t.Fatalf("prepare MySQL Codex identity state: %v", err)
	}
	fields := usageidentity.Fields{
		AuthFileSnapshot: strings.ToLower(physicalFile), AuthIndex: strings.ToLower(authIndex),
		Source: strings.ToLower(physicalFile), AuthProviderSnapshot: "codex",
		AuthAccountIDSnapshot: workspace, AccountSnapshot: member,
	}
	assertAllowed := func(stage string) {
		t.Helper()
		_, allowed, err := ResolveCodexLegacyAccountKey(ctx, queryTx, fields)
		if err != nil || !allowed {
			t.Fatalf("%s MySQL Codex legacy identity allowed=%v err=%v", stage, allowed, err)
		}
	}
	assertAllowed("stored")
	if _, err := tx.ExecContext(ctx, `update usage_monitoring_rollup_state
		set coverage_event_id=? where rollup_name=?`, latestID-1,
		CodexLegacyIdentityRollupName); err != nil {
		t.Fatal(err)
	}
	assertAllowed("stored plus tail")
	if _, err := tx.ExecContext(ctx, `delete from usage_monitoring_rollup_state
		where rollup_name=?`, CodexLegacyIdentityRollupName); err != nil {
		t.Fatal(err)
	}
	assertAllowed("raw fallback")
}

func mustRepositoryCallsSucceed(
	t *testing.T,
	ctx context.Context,
	repo Repository,
	filter AnalyticsFilter,
	allFilter AnalyticsFilter,
	full model.UsageEvent,
) {
	t.Helper()
	check := func(name string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	_, err := repo.ListRecent(ctx, 50_000)
	check("ListRecent", err)
	_, err = repo.ModelUsageSummary(ctx, 50_000)
	check("ModelUsageSummary", err)
	_, err = repo.BackfillResponseMetadata(ctx, 100)
	check("BackfillResponseMetadata", err)
	_, err = repo.Count(ctx)
	check("Count", err)
	_, err = repo.ExportJSONL(ctx)
	check("ExportJSONL", err)
	var output bytes.Buffer
	check("WriteCompatibleUsage", repo.WriteCompatibleUsage(ctx, &output, 50_000))
	output.Reset()
	check("WriteExportJSONL", repo.WriteExportJSONL(ctx, &output, 50_000))
	_, err = repo.AggregateBetween(ctx, allFilter.FromMS, allFilter.ToMS)
	check("AggregateBetween", err)
	_, err = repo.TopModelsBetween(ctx, allFilter.FromMS, allFilter.ToMS, 5)
	check("TopModelsBetween", err)
	_, err = repo.ModelStatsBetween(ctx, allFilter.FromMS, allFilter.ToMS)
	check("ModelStatsBetween", err)
	_, err = repo.RecentFailuresBetween(ctx, allFilter.FromMS, allFilter.ToMS, 5)
	check("RecentFailuresBetween", err)
	_, err = repo.HourlyTimelineBetween(ctx, allFilter.FromMS, allFilter.ToMS)
	check("HourlyTimelineBetween", err)
	_, err = repo.BucketTimelineBetween(ctx, allFilter.FromMS, allFilter.ToMS, 60_000)
	check("BucketTimelineBetween", err)
	_, err = repo.AggregateWithFilter(ctx, filter)
	check("AggregateWithFilter", err)
	_, err = repo.ModelStatsWithFilter(ctx, allFilter, 5)
	check("ModelStatsWithFilter", err)
	_, err = repo.TimelineWithFilter(ctx, allFilter, "hour", time.UTC)
	check("TimelineWithFilter", err)
	_, err = repo.LatencyPercentilesWithFilter(ctx, allFilter, "hour", time.UTC)
	check("LatencyPercentilesWithFilter", err)
	_, err = repo.LatencySummaryWithFilter(ctx, allFilter)
	check("LatencySummaryWithFilter", err)
	_, err = repo.HourlyDistributionWithFilter(ctx, allFilter, time.UTC)
	check("HourlyDistributionWithFilter", err)
	_, err = repo.FilterOptionValuesWithFilter(ctx, allFilter)
	check("FilterOptionValuesWithFilter", err)
	_, err = repo.FilterSelectorValuesWithFilter(ctx, allFilter)
	check("FilterSelectorValuesWithFilter", err)
	_, err = repo.HeatmapWithFilter(ctx, allFilter, time.UTC)
	check("HeatmapWithFilter", err)
	_, err = repo.ChannelModelStatsWithFilter(ctx, allFilter)
	check("ChannelModelStatsWithFilter", err)
	_, err = repo.FailureSourcesWithFilter(ctx, allFilter)
	check("FailureSourcesWithFilter", err)
	_, err = repo.AccountModelStatsWithFilter(ctx, allFilter)
	check("AccountModelStatsWithFilter", err)
	accountKey, _ := usageidentity.AccountKey(usageidentity.Fields{
		AuthFileSnapshot: full.AuthFileSnapshot, AuthIndex: full.AuthIndex,
		AuthProviderSnapshot: full.AuthProviderSnapshot, AuthProjectIDSnapshot: full.AuthProjectIDSnapshot,
		AccountSnapshot: full.AccountSnapshot, AuthLabelSnapshot: full.AuthLabelSnapshot, Source: full.Source,
	})
	_, err = repo.AccountWindowModelStats(ctx, []AccountWindowUsageQuery{{RequestIndex: 1, FromMS: allFilter.FromMS,
		ToMS: allFilter.ToMS, AccountKey: accountKey}})
	check("AccountWindowModelStats", err)
	_, err = repo.RecentAccountRequests(ctx, []LatestAccountRequestQuery{{RequestIndex: 1,
		AuthFileSnapshot: full.AuthFileSnapshot, AuthIndex: full.AuthIndex}}, 5)
	check("RecentAccountRequests", err)
	_, err = repo.CredentialModelStatsWithFilter(ctx, allFilter)
	check("CredentialModelStatsWithFilter", err)
	_, err = repo.CredentialTimelineWithFilter(ctx, allFilter, "hour", time.UTC)
	check("CredentialTimelineWithFilter", err)
	apiFilter := allFilter
	apiFilter.APIKeyHashes = []string{full.APIKeyHash}
	_, err = repo.APIKeyTimelineWithFilter(ctx, apiFilter, "hour", time.UTC)
	check("APIKeyTimelineWithFilter", err)
	_, err = repo.APIKeyModelStatsWithFilter(ctx, apiFilter)
	check("APIKeyModelStatsWithFilter", err)
	_, err = repo.TaskBucketsWithFilter(ctx, allFilter)
	check("TaskBucketsWithFilter", err)
	_, err = repo.RecentFailuresWithFilter(ctx, allFilter, 5)
	check("RecentFailuresWithFilter", err)
	_, err = repo.EventsCountWithFilter(ctx, allFilter)
	check("EventsCountWithFilter", err)
	_, err = repo.LatestHeaderSnapshots(ctx, allFilter.FromMS, 10)
	check("LatestHeaderSnapshots", err)
	_, err = repo.ActiveDaysWithFilter(ctx, allFilter, time.UTC)
	check("ActiveDaysWithFilter", err)
	_, err = repo.ZeroTokenModelsWithFilter(ctx, allFilter)
	check("ZeroTokenModelsWithFilter", err)
}

func assertMySQLFullFields(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	full model.UsageEvent,
	empty model.UsageEvent,
	maximum model.UsageEvent,
	longRawValue string,
) {
	t.Helper()
	var requestID, source, failBody, rawJSON string
	var inputTokens int64
	var quota float64
	if err := db.QueryRowContext(ctx, `select request_id, source, fail_body, raw_json,
		header_quota_used_percent, input_tokens from usage_events where event_hash = ?`, full.EventHash).Scan(
		&requestID, &source, &failBody, &rawJSON, &quota, &inputTokens,
	); err != nil {
		t.Fatalf("read complete MySQL event: %v", err)
	}
	if requestID != full.RequestID || source != full.Source || failBody != full.FailBody ||
		!strings.Contains(rawJSON, longRawValue) || quota != *full.HeaderQuotaUsedPercent || inputTokens != full.InputTokens {
		t.Fatalf("complete field round trip mismatch request=%q source=%q fail=%d raw=%d quota=%v input=%d",
			requestID, source, len(failBody), len(rawJSON), quota, inputTokens)
	}
	var sourceNull, failBodyNull, rawJSONNull bool
	if err := db.QueryRowContext(ctx, `select source is null, fail_body is null, raw_json is null
		from usage_events where event_hash = ?`, empty.EventHash).Scan(&sourceNull, &failBodyNull, &rawJSONNull); err != nil ||
		!sourceNull || !failBodyNull || !rawJSONNull {
		t.Fatalf("NULL semantics source=%v failBody=%v rawJSON=%v err=%v", sourceNull, failBodyNull, rawJSONNull, err)
	}
	if err := db.QueryRowContext(ctx, `select input_tokens from usage_events where event_hash = ?`, maximum.EventHash).
		Scan(&inputTokens); err != nil || inputTokens != math.MaxInt64 {
		t.Fatalf("maximum integer input=%d err=%v", inputTokens, err)
	}
}

func testConcurrentMySQLInsert(t *testing.T, ctx context.Context, repo Repository, event model.UsageEvent) {
	t.Helper()
	const workers = 8
	var wait sync.WaitGroup
	results := make(chan model.InsertResult, workers)
	errors := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := repo.InsertBatch(ctx, []model.UsageEvent{event})
			results <- result
			errors <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	inserted, skipped := 0, 0
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent InsertBatch: %v", err)
		}
	}
	for result := range results {
		inserted += result.Inserted
		skipped += result.Skipped
	}
	if inserted != 1 || skipped != workers-1 {
		t.Fatalf("concurrent idempotency inserted=%d skipped=%d", inserted, skipped)
	}
}

func cleanupMySQLUsageEvents(t *testing.T, db *sql.DB, hashes []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	encoded := `["` + strings.Join(hashes, `","`) + `"]`
	statements := []string{
		`delete h from usage_monitoring_header_latest_v1 h join usage_events e on e.id=h.event_id
			where e.event_hash in (select value from json_table(?, '$[*]' columns(value longtext path '$')) as hashes)`,
		`delete p from usage_monitoring_event_projection_v1 p join usage_events e on e.id=p.event_id
			where e.event_hash in (select value from json_table(?, '$[*]' columns(value longtext path '$')) as hashes)`,
		`delete from usage_event_identity_ledger where event_hash in
			(select value from json_table(?, '$[*]' columns(value longtext path '$')) as hashes)`,
		`delete from usage_events where event_hash in
			(select value from json_table(?, '$[*]' columns(value longtext path '$')) as hashes)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement, encoded); err != nil {
			t.Errorf("cleanup MySQL usage conformance fixture: %v", err)
		}
	}
}

func mysqlUsageEventTestConfig(t *testing.T) dbmysql.Config {
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
		t.Fatal(fmt.Errorf("CPAMP_MYSQL_TEST_HOST/PORT/DATABASE/USERNAME/PASSWORD are required"))
	}
	return config
}
