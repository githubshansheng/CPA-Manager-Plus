package usageevent

import (
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func TestValidateMySQLVersionContract(t *testing.T) {
	for _, version := range []string{"8.0.12", "8.0.99-commercial", "8.1.0", "8.4.0", "8.4.7-cloud"} {
		if err := validateMySQLVersion(version, "MySQL Community Server"); err != nil {
			t.Errorf("validateMySQLVersion(%q): %v", version, err)
		}
	}
	for _, test := range []struct{ version, comment string }{
		{"8.0.11", "MySQL Community Server"},
		{"8.0.36-MariaDB", "MariaDB Server"},
		{"10.11.8", "MariaDB Server"},
		{"invalid", "MySQL"},
	} {
		if err := validateMySQLVersion(test.version, test.comment); err == nil {
			t.Errorf("validateMySQLVersion(%q, %q) accepted an unsupported server", test.version, test.comment)
		}
	}
}

func TestConstructorsKeepSQLiteDefault(t *testing.T) {
	legacy := New(nil).(*repository)
	explicit := NewSQLite(nil).(*repository)
	if legacy.backend != database.BackendSQLite || explicit.backend != database.BackendSQLite {
		t.Fatalf("SQLite constructors selected %q and %q", legacy.backend, explicit.backend)
	}
	if _, err := NewForBackend(nil, database.BackendMySQL); err == nil {
		t.Fatal("MySQL constructor accepted a nil database")
	}
	if _, err := NewForBackend(nil, database.BackendKind("postgres")); err == nil {
		t.Fatal("constructor accepted an unsupported backend")
	}
}

func TestMySQLQueryTranslationCoversRepositoryGrammar(t *testing.T) {
	r := &repository{backend: database.BackendMySQL}
	query := r.querySQL(responseMetadataBackfillSelect + "\n" +
		usageidentity.SQLAccountKeyExpression("e") + "\n" +
		`select max(max(e.cached_tokens, e.cache_tokens) - max(e.cache_read_tokens, 0) - max(e.cache_creation_tokens, 0), 0)
		from usage_events e not indexed where e.auth_file_snapshot collate nocase = ?
		and coalesce(e.auth_index, '') in (select value from json_each(?))`)
	for _, forbidden := range []string{
		"cpamp_analytics_model", "json_each", "collate nocase", " not indexed", " || ",
		"json_type(metadata_json", "max(max(e.cached_tokens",
	} {
		if strings.Contains(strings.ToLower(query), forbidden) {
			t.Fatalf("translated MySQL query still contains %q:\n%s", forbidden, query)
		}
	}
	for _, required := range []string{"json_table(", "json_extract(metadata_json", "greatest(", "concat(", "lower(?)"} {
		if !strings.Contains(strings.ToLower(query), required) {
			t.Fatalf("translated MySQL query is missing %q:\n%s", required, query)
		}
	}
}

func TestMySQLSearchUsesLiteralSubstringSemantics(t *testing.T) {
	filter := AnalyticsFilter{FromMS: 1, ToMS: 2, SearchQuery: `A%_短`, SearchAPIKeyHash: "ABC", Models: []string{"m"}}
	where, args := analyticsWhere(filter, true)
	if strings.Contains(strings.ToLower(where), " like ") || !strings.Contains(strings.ToLower(where), "locate(lower(?)") {
		t.Fatalf("MySQL search predicate = %s", where)
	}
	if len(args) < 3 || args[2] != "a%_短" {
		t.Fatalf("MySQL search arguments do not preserve a literal substring: %#v", args)
	}
	translated := (&repository{backend: database.BackendMySQL}).querySQL(where)
	if strings.Contains(translated, "json_each") || !strings.Contains(strings.ToLower(translated), "json_table") {
		t.Fatalf("MySQL filter list predicate = %s", translated)
	}
}

func TestMySQLValuesCTEUsesSelectUnion(t *testing.T) {
	mysql := (&repository{backend: database.BackendMySQL}).valuesCTE(3, 2)
	if mysql != "select ?,?,? union all select ?,?,?" {
		t.Fatalf("MySQL values CTE = %q", mysql)
	}
	sqlite := (&repository{backend: database.BackendSQLite}).valuesCTE(2, 2)
	if sqlite != "values (?,?),(?,?)" {
		t.Fatalf("SQLite values CTE = %q", sqlite)
	}
}

func TestMySQLInsertIfAbsentDoesNotUseIgnore(t *testing.T) {
	r := &repository{backend: database.BackendMySQL}
	query := r.insertIfAbsentSQL("insert or ignore into usage_events (event_hash) values (?)")
	if strings.Contains(strings.ToLower(query), "ignore") || !strings.Contains(strings.ToLower(query), "on duplicate key update") {
		t.Fatalf("MySQL idempotent insert = %q", query)
	}
}
