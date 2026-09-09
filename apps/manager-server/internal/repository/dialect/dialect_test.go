package dialect

import (
	"errors"
	"strings"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func TestUpsertClauseUsesBackendGrammar(t *testing.T) {
	sqlite := SQLite().UpsertClause([]string{"key"}, []string{"value", "updated_at_ms"})
	if sqlite != ` ON CONFLICT ("key") DO UPDATE SET "value" = excluded."value", "updated_at_ms" = excluded."updated_at_ms"` {
		t.Fatalf("SQLite upsert clause = %q", sqlite)
	}
	mysql := MySQL().UpsertClause([]string{"key"}, []string{"value", "updated_at_ms"})
	if mysql != " ON DUPLICATE KEY UPDATE `value` = VALUES(`value`), `updated_at_ms` = VALUES(`updated_at_ms`)" {
		t.Fatalf("MySQL upsert clause = %q", mysql)
	}
	if strings.Contains(mysql, " AS new") {
		t.Fatalf("MySQL upsert requires the 8.0.19 row-alias grammar: %q", mysql)
	}
}

func TestForUpdateIsMySQLOnly(t *testing.T) {
	if got := SQLite().ForUpdate(); got != "" {
		t.Fatalf("SQLite ForUpdate() = %q", got)
	}
	if got := MySQL().ForUpdate(); got != " FOR UPDATE" {
		t.Fatalf("MySQL ForUpdate() = %q", got)
	}
}

func TestNowMillisExpressionUsesBackendClock(t *testing.T) {
	if got := SQLite().NowMillisExpression(); got != "cast(unixepoch('subsec') * 1000 as integer)" {
		t.Fatalf("SQLite NowMillisExpression() = %q", got)
	}
	if got := MySQL().NowMillisExpression(); got != "CAST(UNIX_TIMESTAMP(CURRENT_TIMESTAMP(3)) * 1000 AS SIGNED)" {
		t.Fatalf("MySQL NowMillisExpression() = %q", got)
	}
}

func TestInsertDoNothingClauseUsesBackendGrammar(t *testing.T) {
	conflicts := []string{"file_name", "provider"}
	if got := SQLite().InsertDoNothingClause(conflicts, "file_name"); got !=
		` ON CONFLICT ("file_name", "provider") DO NOTHING` {
		t.Fatalf("SQLite InsertDoNothingClause() = %q", got)
	}
	if got := MySQL().InsertDoNothingClause(conflicts, "file_name"); got !=
		" ON DUPLICATE KEY UPDATE `file_name` = `file_name`" {
		t.Fatalf("MySQL InsertDoNothingClause() = %q", got)
	}
}

func TestMaxWithParameterUsesBackendScalarFunction(t *testing.T) {
	if got := SQLite().MaxWithParameter("enabled"); got != `max("enabled", ?)` {
		t.Fatalf("SQLite MaxWithParameter() = %q", got)
	}
	if got := MySQL().MaxWithParameter("enabled"); got != "GREATEST(`enabled`, ?)" {
		t.Fatalf("MySQL MaxWithParameter() = %q", got)
	}
}

func TestMinWithParameterUsesBackendScalarFunction(t *testing.T) {
	if got := SQLite().MinWithParameter("disabled_at_ms"); got != `min("disabled_at_ms", ?)` {
		t.Fatalf("SQLite MinWithParameter() = %q", got)
	}
	if got := MySQL().MinWithParameter("disabled_at_ms"); got != "LEAST(`disabled_at_ms`, ?)" {
		t.Fatalf("MySQL MinWithParameter() = %q", got)
	}
}

func TestMySQLRetryableWriteConflictsAreConservative(t *testing.T) {
	for _, number := range []uint16{1062, 1205, 1213} {
		if !MySQL().IsRetryableWriteConflict(&mysqldriver.MySQLError{Number: number}) {
			t.Fatalf("MySQL error %d was not retryable", number)
		}
	}
	if MySQL().IsRetryableWriteConflict(&mysqldriver.MySQLError{Number: 1045}) {
		t.Fatal("authentication failure was retryable")
	}
	if SQLite().IsRetryableWriteConflict(&mysqldriver.MySQLError{Number: 1213}) {
		t.Fatal("SQLite classified a MySQL deadlock as retryable")
	}
	wrapped := errors.New("not a driver conflict")
	if MySQL().IsRetryableWriteConflict(wrapped) {
		t.Fatal("ordinary error was retryable")
	}
}

func TestForBackendRejectsUnknownBackend(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("ForBackend accepted an unknown backend")
		}
	}()
	_ = ForBackend(database.BackendKind("postgres"))
}

func TestMySQLVersionContract(t *testing.T) {
	for _, version := range []string{"8.0.12", "8.0.99-commercial", "8.1.0", "8.4.0", "8.4.7-cloud"} {
		if err := validateMySQLVersion(version, "MySQL Community Server"); err != nil {
			t.Errorf("accepted MySQL version %q: %v", version, err)
		}
	}
	for _, test := range []struct{ version, comment string }{
		{"8.0.11", "MySQL Community Server"},
		{"9.0.0", "MySQL Community Server"},
		{"8.0.36-MariaDB", "MariaDB Server"},
		{"10.11.8", "MariaDB Server"},
	} {
		if err := validateMySQLVersion(test.version, test.comment); err == nil {
			t.Errorf("unsupported server %q/%q was accepted", test.version, test.comment)
		}
	}
}

func TestRewriteQueryPreservesSQLiteAndMapsDerivedRepositoryGrammar(t *testing.T) {
	sqliteQuery := "select max(max(coalesce(cached_tokens, 0), coalesce(cache_tokens, 0)) - " +
		"max(coalesce(cache_read_tokens, 0), 0), 0), max(id) from usage_events not indexed"
	if got := SQLite().RewriteQuery(sqliteQuery); got != sqliteQuery {
		t.Fatalf("SQLite rewrite changed query:\n%s", got)
	}
	mysqlQuery := MySQL().RewriteQuery(sqliteQuery)
	for _, want := range []string{
		"greatest(greatest(coalesce(cached_tokens, 0), coalesce(cache_tokens, 0))",
		"greatest(coalesce(cache_read_tokens, 0), 0)",
		"max(id)",
	} {
		if !strings.Contains(mysqlQuery, want) {
			t.Fatalf("MySQL scalar rewrite missing %q:\n%s", want, mysqlQuery)
		}
	}
	if strings.Contains(mysqlQuery, " not indexed") {
		t.Fatalf("MySQL retained SQLite index hint:\n%s", mysqlQuery)
	}
}

func TestRewriteQueryMapsCTEUpsertJSONAndProjectionSearch(t *testing.T) {
	query := "with source as (select value from json_each(?)) " +
		"insert into target (value) select value from source " +
		"on conflict(value) do update set value = excluded.value"
	got := MySQL().RewriteQuery(query)
	for _, want := range []string{
		"insert into target (value)",
		"with source as (select value from json_table(",
		"on duplicate key update value = values(value)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("MySQL CTE upsert rewrite missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "insert into") > strings.Index(got, "with source") {
		t.Fatalf("MySQL CTE was not moved after INSERT target:\n%s", got)
	}
	search := MySQL().RewriteQuery(
		"select event_id from usage_monitoring_event_projection_v1 where event_id in " +
			"(select rowid from usage_monitoring_event_search_v1 where search_text like ?)",
	)
	if strings.Contains(search, "select rowid") || !strings.Contains(search, "select event_id") {
		t.Fatalf("MySQL search projection did not use explicit event_id:\n%s", search)
	}
}

func TestRewriteQueryMapsAnalyticsAndAccountIdentityWithoutSQLiteFunctions(t *testing.T) {
	query := "select " +
		usageidentity.SQLRequestAnalyticsModelExpression("e.model", "e.requested_model") + ", " +
		usageidentity.SQLAccountKeyExpression("e") + " from usage_events e"
	got := MySQL().RewriteQuery(query)
	for _, forbidden := range []string{"cpamp_analytics_model(", " || "} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("MySQL identity rewrite retained %q:\n%s", forbidden, got)
		}
	}
	for _, want := range []string{"regexp_like(", "substring_index(", "concat("} {
		if !strings.Contains(got, want) {
			t.Fatalf("MySQL identity rewrite missing %q:\n%s", want, got)
		}
	}
}

func TestRewriteQueryUsesHashedIdentityLedgerLookupForMySQL(t *testing.T) {
	query := `select 1 from usage_events e where not exists (
		select 1 from usage_event_identity_ledger ledger
		where ledger.event_hash = e.event_hash
	) and not exists (
		select 1 from usage_event_identity_ledger ledger
		where ledger.event_hash = usage_events.event_hash
	)`
	got := MySQL().RewriteQuery(query)
	for _, want := range []string{
		"ledger.__cpamp_pk_hash = UNHEX(SHA2(CAST(JSON_ARRAY(e.event_hash) AS CHAR CHARACTER SET utf8mb4), 256)) and ledger.event_hash = e.event_hash",
		"ledger.__cpamp_pk_hash = UNHEX(SHA2(CAST(JSON_ARRAY(usage_events.event_hash) AS CHAR CHARACTER SET utf8mb4), 256)) and ledger.event_hash = usage_events.event_hash",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("MySQL identity-ledger rewrite missing indexed lookup %q:\n%s", want, got)
		}
	}
}

func TestRewriteQueryQualifiesIdentityLedgerUpdateTargetsForMySQL(t *testing.T) {
	query := `update usage_event_identity_ledger as ledger set
		raw_event_id = e.id,
		timestamp_ms = e.timestamp_ms,
		bucket_ms = e.timestamp_ms - (e.timestamp_ms % 3600000),
		aggregate_schema_version = ?,
		aggregate_structure_revision = ?,
		updated_at_ms = ?
	from usage_events as e
	where ledger.event_hash = e.event_hash
		and e.id > ? and e.id <= ?`
	got := MySQL().RewriteQuery(query)
	for _, want := range []string{
		"join usage_events as e on ledger.__cpamp_pk_hash = UNHEX(SHA2(",
		"ledger.raw_event_id = e.id",
		"ledger.timestamp_ms = e.timestamp_ms",
		"ledger.bucket_ms = e.timestamp_ms",
		"ledger.aggregate_schema_version = ?",
		"ledger.aggregate_structure_revision = ?",
		"ledger.updated_at_ms = ?",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("MySQL identity-ledger update rewrite missing %q:\n%s", want, got)
		}
	}
}
