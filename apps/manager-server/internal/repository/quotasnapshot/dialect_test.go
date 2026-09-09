package quotasnapshot

import (
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/dialect"
)

func TestListCandidatesQueryUsesBackendGrammar(t *testing.T) {
	sqliteQuery := (&repository{dialect: dialect.SQLite()}).listCandidatesQuery()
	if !strings.Contains(sqliteQuery, "cast(logical_window_id as text)") ||
		!strings.Contains(sqliteQuery, "'legacy:' || provider_window_id") {
		t.Fatalf("SQLite candidate query lost its identity expression: %s", sqliteQuery)
	}
	mysqlQuery := (&repository{dialect: dialect.MySQL()}).listCandidatesQuery()
	for _, forbidden := range []string{" as text)", " || ", "account_quota_windows window"} {
		if strings.Contains(mysqlQuery, forbidden) {
			t.Errorf("MySQL candidate query contains SQLite/reserved grammar %q: %s", forbidden, mysqlQuery)
		}
	}
	if !strings.Contains(mysqlQuery, "cast(logical_window_id as char)") ||
		!strings.Contains(mysqlQuery, "concat('legacy:', provider_window_id, char(0)") {
		t.Fatalf("MySQL candidate query lost its identity expression: %s", mysqlQuery)
	}
}
