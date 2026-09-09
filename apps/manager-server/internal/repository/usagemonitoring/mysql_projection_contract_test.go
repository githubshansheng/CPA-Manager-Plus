package usagemonitoring

import (
	"slices"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
)

func TestMySQLProjectionColumnListsMatchSchemaManifest(t *testing.T) {
	tests := []struct {
		name    string
		table   string
		columns []string
	}{
		{name: "event projection", table: usageprojection.EventTable, columns: mysqlEventProjectionColumns},
		{name: "header projection", table: usageprojection.HeaderTable, columns: mysqlHeaderProjectionColumns},
	}

	manifest := schema.Current()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var manifestColumns []string
			for _, table := range manifest.Tables {
				if table.Name != test.table {
					continue
				}
				manifestColumns = make([]string, len(table.Columns))
				for index, column := range table.Columns {
					manifestColumns[index] = column.Name
				}
				break
			}
			if len(manifestColumns) == 0 {
				t.Fatalf("table %q is missing from schema manifest", test.table)
			}
			if !slices.Equal(test.columns, manifestColumns) {
				t.Fatalf("MySQL projection columns do not match %s manifest\ngot:  %v\nwant: %v", test.table, test.columns, manifestColumns)
			}
		})
	}
}

func TestStoredHeaderRowsQueryAvoidsMySQLStoredKeywordAlias(t *testing.T) {
	lowerQuery := strings.ToLower(storedHeaderRowsSQL)
	if strings.Contains(lowerQuery, "usage_monitoring_header_latest_v1 stored") {
		t.Fatal("header latest query uses MySQL's STORED keyword as a table alias")
	}
	if !strings.Contains(lowerQuery, "usage_monitoring_header_latest_v1 header_latest") {
		t.Fatal("header latest query does not use the portable table alias")
	}
}

func TestRawHeaderRowsQueryQualifiesWindowColumnsForMySQL(t *testing.T) {
	lowerQuery := strings.ToLower(rawHeaderRowsSQL)
	if !strings.Contains(lowerQuery, "partition by candidates.snapshot_key") {
		t.Fatal("raw header query does not qualify the window partition column")
	}
	if !strings.Contains(lowerQuery, "order by candidates.timestamp_ms desc, candidates.id desc") {
		t.Fatal("raw header query does not qualify the window ordering columns")
	}
}
