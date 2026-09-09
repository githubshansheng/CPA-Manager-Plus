package databasemanagement

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
)

func TestMySQLDerivedRebuildPlanCoversManifestAndUsesMySQLShape(t *testing.T) {
	watermark := derivedRebuildWatermark{UsageEventID: 123, PricingRevision: "price-revision"}
	steps := mysqlDerivedRebuildPlan(watermark, 456)
	got := make(map[string]bool, len(steps))
	for _, step := range steps {
		if got[step.Table] {
			t.Fatalf("duplicate rebuild step for %s", step.Table)
		}
		got[step.Table] = true
		if len(step.Statements) == 0 || step.Statements[0].Query != "DELETE FROM "+mysqlQuote(step.Table) {
			t.Fatalf("%s does not start with an idempotent replacement delete: %#v", step.Table, step.Statements)
		}
		for _, statement := range step.Statements {
			upper := strings.ToUpper(statement.Query)
			for _, forbidden := range []string{" ON CONFLICT", "INSERT OR ", "JSON_EACH(", "CREATE VIRTUAL TABLE"} {
				if strings.Contains(upper, forbidden) {
					t.Errorf("%s contains SQLite-only SQL %q", step.Table, forbidden)
				}
			}
			if countSQLPlaceholders(statement.Query) != len(statement.Args) {
				t.Errorf("%s placeholder count=%d args=%d", step.Table, countSQLPlaceholders(statement.Query), len(statement.Args))
			}
		}
		if len(step.Statements) > 1 &&
			!strings.HasPrefix(strings.TrimSpace(strings.ToUpper(step.Statements[1].Query)), "INSERT INTO "+strings.ToUpper(step.Table)) {
			t.Errorf("%s rebuild does not insert into its own target: %s", step.Table, step.Statements[1].Query)
		}
	}

	want := map[string]bool{}
	for _, table := range schema.Current().Tables {
		if table.Class == schema.ClassDerived {
			want[table.Name] = true
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("derived rebuild coverage differs\n got: %v\nwant: %v", got, want)
	}
}

func TestMySQLDerivedInsertListsPreserveEveryManifestColumn(t *testing.T) {
	steps := mysqlDerivedRebuildPlan(
		derivedRebuildWatermark{UsageEventID: 123, PricingRevision: "price-revision"}, 456,
	)
	byName := make(map[string]mysqlDerivedRebuildStep, len(steps))
	for _, step := range steps {
		byName[step.Table] = step
	}
	for _, table := range schema.Current().Tables {
		if table.Class != schema.ClassDerived {
			continue
		}
		step := byName[table.Name]
		if table.Name == "usage_rollup_rebuild_state" {
			if len(step.Statements) != 1 {
				t.Fatalf("%s must be cleared after a completed rebuild", table.Name)
			}
			continue
		}
		if len(step.Statements) != 2 {
			t.Fatalf("%s has no replacement insert", table.Name)
		}
		got := insertColumnNames(t, step.Statements[1].Query)
		want := make([]string, 0, len(table.Columns)+1)
		if table.Name == "usage_monitoring_event_search_v1" {
			// MySQL materializes SQLite FTS's implicit content_rowid.
			want = append(want, "event_id")
		}
		for _, column := range table.Columns {
			want = append(want, column.Name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s insert columns differ\n got: %v\nwant: %v", table.Name, got, want)
		}
	}
}

func insertColumnNames(t *testing.T, query string) []string {
	t.Helper()
	open := strings.Index(query, "(")
	if open < 0 {
		t.Fatalf("query has no insert column list: %s", query)
	}
	closeOffset := strings.Index(query[open+1:], ")")
	if closeOffset < 0 {
		t.Fatalf("query has no complete insert column list: %s", query)
	}
	close := open + 1 + closeOffset
	raw := strings.Split(query[open+1:close], ",")
	columns := make([]string, len(raw))
	for index, column := range raw {
		columns[index] = strings.TrimSpace(column)
	}
	return columns
}

func TestMySQLMonitoringProjectionPreservesSearchDocumentBoundaryAndUsesSessionCollation(t *testing.T) {
	step := mysqlMonitoringProjectionStep(derivedRebuildWatermark{UsageEventID: 9}, 10)
	query := step.Statements[1].Query
	for _, fragment := range []string{
		"CHAR(31 USING utf8mb4)",
		"e.client_ip",
		"e.x_forwarded_for",
		"e.user_agent",
		"e.fail_summary",
		"e.header_trace_id",
		"REGEXP_LIKE",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("projection SQL is missing %q", fragment)
		}
	}
	if strings.Contains(query, "utf8mb4_0900_bin") {
		t.Fatal("projection SQL hard-codes a collation unavailable on MySQL 8.0.12")
	}
	for _, fragment := range []string{"LEFT(e.fail_summary", "LEFT(e.client_ip", "LEFT(e.header_", "SUBSTRING(e.fail_summary"} {
		if strings.Contains(query, fragment) {
			t.Fatalf("projection SQL truncates an authoritative search field with %q", fragment)
		}
	}
}

func TestMySQLSearchSemanticReadinessRemainsClosed(t *testing.T) {
	step := mysqlMonitoringSearchStateStep(10)
	if !strings.Contains(step.Statements[1].Query, "VALUES (1,0,?)") {
		t.Fatal("MySQL search must remain unready until its SQLite trigram conformance suite passes")
	}
}

func TestMySQLCodexLegacyIdentityEvidencePreservesPhysicalIdentityContract(t *testing.T) {
	step := mysqlCodexLegacyIdentityEvidenceStep(derivedRebuildWatermark{UsageEventID: 99})
	if step.Table != usageevent.CodexLegacyIdentityEvidenceTable || len(step.Statements) != 2 {
		t.Fatalf("Codex evidence rebuild step = %#v", step)
	}
	query := step.Statements[1].Query
	for _, fragment := range []string{
		"LOWER(e.auth_file_snapshot)",
		"LOWER(e.source)",
		"LOWER(e.auth_index)",
		"UNHEX(SHA2(CAST(JSON_ARRAY(",
		"e.auth_file_snapshot IS NULL",
		"e.auth_file_snapshot=''",
		"LOWER(TRIM(e.source))<>LOWER(TRIM(e.account_snapshot))",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("Codex evidence rebuild SQL is missing %q", fragment)
		}
	}
	if got := countSQLPlaceholders(query); got != 6 || len(step.Statements[1].Args) != got {
		t.Fatalf("Codex evidence placeholders=%d args=%d, want 6", got, len(step.Statements[1].Args))
	}

	state := mysqlMonitoringStateStep(derivedRebuildWatermark{UsageEventID: 99, PricingRevision: "prices"}, 100)
	foundName, foundRevision := false, false
	for _, value := range state.Statements[1].Args {
		foundName = foundName || value == usageevent.CodexLegacyIdentityRollupName
		foundRevision = foundRevision || value == usageevent.CodexLegacyIdentityEvidenceRevision
	}
	if !foundName || !foundRevision {
		t.Fatalf("monitoring state args omit Codex identity state: %#v", state.Statements[1].Args)
	}
}

func TestMySQLDerivedAggregatesDiscriminateCompleteBinaryKeys(t *testing.T) {
	steps := mysqlDerivedRebuildPlan(
		derivedRebuildWatermark{UsageEventID: 9, PricingRevision: "revision"}, 10,
	)
	requireHash := map[string]bool{
		"usage_account_model_rollups":                true,
		"usage_dashboard_hourly_rollups":             true,
		"usage_hourly_aggregate_v1":                  true,
		"usage_monitoring_account_daily_rollups_v1":  true,
		"usage_monitoring_api_key_daily_rollups_v1":  true,
		"usage_monitoring_selector_daily_rollups_v1": true,
		"usage_pricing_account_rollups_v1":           true,
		"usage_pricing_hourly_rollups_v1":            true,
	}
	for _, step := range steps {
		if !requireHash[step.Table] {
			continue
		}
		query := step.Statements[1].Query
		if !strings.Contains(query, "UNHEX(SHA2(CAST(JSON_ARRAY(") {
			t.Errorf("%s can merge PAD SPACE or max_sort_length-equivalent keys", step.Table)
		}
		delete(requireHash, step.Table)
	}
	if len(requireHash) != 0 {
		t.Fatalf("missing aggregate hash assertions for %v", requireHash)
	}
}

func TestMySQLAccountSnapshotsChooseEarliestNonEmptyByNumericEventID(t *testing.T) {
	step := mysqlAccountModelRollupStep(
		derivedRebuildWatermark{UsageEventID: 9, PricingRevision: "revision"}, 10,
	)
	query := step.Statements[1].Query
	for _, fragment := range []string{
		"MIN(CASE WHEN COALESCE(account_snapshot,'')<>'' THEN id END)",
		"OVER (PARTITION BY scored.group_key)",
		"MAX(CASE WHEN id=first_account_id THEN NULLIF(account_snapshot,'') END)",
	} {
		if !strings.Contains(query, fragment) {
			t.Errorf("account rebuild SQL is missing numeric earliest-value selection %q", fragment)
		}
	}
	if strings.Contains(query, "MIN(CONCAT(") {
		t.Fatal("earliest snapshot selection must not compare LONGTEXT prefixes")
	}
	if count := strings.Count(query, "OVER (PARTITION BY scored.group_key)"); count != 6 {
		t.Fatalf("account rebuild SQL has %d qualified group-key windows, want 6", count)
	}
	if strings.Contains(query, "OVER (PARTITION BY group_key)") {
		t.Fatal("MySQL 8.0.12 cannot resolve a bare upstream CTE alias in a window partition")
	}
}

func TestMySQLDerivedAggregatesDoNotSortAuthorityOnlyPayloads(t *testing.T) {
	account := mysqlAccountModelRollupStep(
		derivedRebuildWatermark{UsageEventID: 9, PricingRevision: "revision"}, 10,
	).Statements[1].Query
	monitoring := mysqlMonitoringAccountDailyStep(
		derivedRebuildWatermark{UsageEventID: 9, PricingRevision: "revision"}, 10,
	).Statements[1].Query
	for name, query := range map[string]string{"account": account, "monitoring": monitoring} {
		if strings.Contains(query, "SELECT e.*") {
			t.Fatalf("%s derived query carries every usage_events field through its sort", name)
		}
		for _, payload := range []string{"e.raw_json", "e.fail_body"} {
			if strings.Contains(query, payload) {
				t.Fatalf("%s derived query materializes unused authority payload %s", name, payload)
			}
		}
		for _, required := range []string{"e.timestamp_ms", "e.input_tokens", "e.output_tokens"} {
			if !strings.Contains(query, required) {
				t.Errorf("%s derived query projection is missing %s", name, required)
			}
		}
	}
}

func countSQLPlaceholders(query string) int {
	count := 0
	quoted := false
	for index := 0; index < len(query); index++ {
		switch query[index] {
		case '\'':
			if quoted && index+1 < len(query) && query[index+1] == '\'' {
				index++
				continue
			}
			quoted = !quoted
		case '?':
			if !quoted {
				count++
			}
		}
	}
	return count
}

func TestRunDerivedRebuildStepPersistsOneWatermarkAndAdvancesToValidate(t *testing.T) {
	repository := newFakeDerivedProgressRepository()
	executor := &fakeDerivedExecutor{tables: []string{"derived_a", "derived_b"}}
	migration := repository.migration
	ctx := context.Background()

	advanced := false
	for attempts := 0; attempts < 10 && !advanced; attempts++ {
		next, done, err := runDerivedRebuildStep(ctx, repository, migration, executor)
		if err != nil {
			t.Fatal(err)
		}
		migration = next
		advanced = done
	}
	if !advanced || migration.Phase != databasemigration.PhaseValidate {
		t.Fatalf("migration did not advance to validate: %#v", migration)
	}
	if executor.captures != 1 {
		t.Fatalf("watermark captures=%d, want 1", executor.captures)
	}
	if !reflect.DeepEqual(executor.rebuilt, []string{"derived_a", "derived_b"}) {
		t.Fatalf("rebuilt tables=%v", executor.rebuilt)
	}
	first := repository.progress["derived_a"].SourceWatermark
	second := repository.progress["derived_b"].SourceWatermark
	if !jsonEqual(first, second) {
		t.Fatalf("derived tables used different source watermarks: %s vs %s", first, second)
	}
}

func TestRunDerivedRebuildStepRetriesCommittedTableAfterProgressFailure(t *testing.T) {
	repository := newFakeDerivedProgressRepository()
	repository.failNextSave = true
	executor := &fakeDerivedExecutor{tables: []string{"derived_a"}}
	ctx := context.Background()

	migration, _, err := runDerivedRebuildStep(ctx, repository, repository.migration, executor)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runDerivedRebuildStep(ctx, repository, migration, executor)
	if !errors.Is(err, errFakeProgressSave) {
		t.Fatalf("save error=%v, want %v", err, errFakeProgressSave)
	}
	next, _, err := runDerivedRebuildStep(ctx, repository, migration, executor)
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation != migration.Generation+1 || len(executor.rebuilt) != 2 {
		t.Fatalf("retry was not idempotently replayed: generation=%d rebuilt=%v", next.Generation, executor.rebuilt)
	}
}

type fakeDerivedExecutor struct {
	tables   []string
	captures int
	rebuilt  []string
}

func (e *fakeDerivedExecutor) Tables() []string { return append([]string(nil), e.tables...) }

func (e *fakeDerivedExecutor) CaptureWatermark(context.Context) (derivedRebuildWatermark, error) {
	e.captures++
	return derivedRebuildWatermark{UsageEventID: 42, PricingRevision: "revision"}, nil
}

func (e *fakeDerivedExecutor) RebuildTable(_ context.Context, table string, _ derivedRebuildWatermark) (int64, error) {
	e.rebuilt = append(e.rebuilt, table)
	return int64(len(e.rebuilt)), nil
}

var errFakeProgressSave = errors.New("fake progress save failed")

type fakeDerivedProgressRepository struct {
	migration    databasemigration.Migration
	progress     map[string]databasemigration.TableProgress
	failNextSave bool
}

func newFakeDerivedProgressRepository() *fakeDerivedProgressRepository {
	return &fakeDerivedProgressRepository{
		migration: databasemigration.Migration{
			ID: "migration-1", Generation: 1,
			Phase: databasemigration.PhaseRebuildDerived, Status: databasemigration.StatusRunning,
		},
		progress: map[string]databasemigration.TableProgress{},
	}
}

func (r *fakeDerivedProgressRepository) TableProgress(_ context.Context, _ string, table string) (databasemigration.TableProgress, bool, error) {
	progress, exists := r.progress[table]
	return progress, exists, nil
}

func (r *fakeDerivedProgressRepository) InitializeTableProgress(
	_ context.Context,
	migrationID, table string,
	expectedGeneration int64,
	watermark json.RawMessage,
	batchSize int,
) (databasemigration.Migration, databasemigration.TableProgress, error) {
	if expectedGeneration != r.migration.Generation {
		return databasemigration.Migration{}, databasemigration.TableProgress{}, databasemigration.ErrGenerationConflict
	}
	progress := databasemigration.TableProgress{
		MigrationID: migrationID, Table: table,
		SourceWatermark: append(json.RawMessage(nil), watermark...), BatchSize: batchSize,
	}
	r.progress[table] = progress
	r.migration.Generation++
	return r.migration, progress, nil
}

func (r *fakeDerivedProgressRepository) SaveTableProgress(
	_ context.Context,
	_ string,
	expectedGeneration int64,
	progress databasemigration.TableProgress,
) (databasemigration.Migration, error) {
	if r.failNextSave {
		r.failNextSave = false
		return databasemigration.Migration{}, errFakeProgressSave
	}
	if expectedGeneration != r.migration.Generation {
		return databasemigration.Migration{}, databasemigration.ErrGenerationConflict
	}
	r.progress[progress.Table] = progress
	r.migration.Generation++
	return r.migration, nil
}

func (r *fakeDerivedProgressRepository) AdvanceMigration(
	_ context.Context,
	_ string,
	expectedGeneration int64,
	next databasemigration.MigrationPhase,
) (databasemigration.Migration, error) {
	if expectedGeneration != r.migration.Generation {
		return databasemigration.Migration{}, databasemigration.ErrGenerationConflict
	}
	r.migration.Phase = next
	r.migration.Generation++
	return r.migration, nil
}
