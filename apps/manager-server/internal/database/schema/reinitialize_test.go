package schema

import (
	"fmt"
	"strings"
	"testing"
)

func TestBuildDropStatementsIsSchemaQualifiedAndDependencyOrdered(t *testing.T) {
	statements := buildDropStatements("cpamp_prod", dropInventory{
		triggers: []string{"z_trigger", "a`trigger"},
		views:    []string{"z_view", "a_view"},
		tables:   []string{"usage_events", "extra_old_table"},
	})
	want := []string{
		"DROP TRIGGER IF EXISTS `cpamp_prod`.`a``trigger`",
		"DROP TRIGGER IF EXISTS `cpamp_prod`.`z_trigger`",
		"DROP VIEW IF EXISTS `cpamp_prod`.`a_view`, `cpamp_prod`.`z_view`",
		"DROP TABLE IF EXISTS `cpamp_prod`.`extra_old_table`, `cpamp_prod`.`usage_events`",
	}
	if len(statements) != len(want) {
		t.Fatalf("statements=%#v, want %#v", statements, want)
	}
	for index := range want {
		if statements[index] != want[index] {
			t.Errorf("statement[%d]=%q, want %q", index, statements[index], want[index])
		}
		if strings.Contains(statements[index], "DROP DATABASE") {
			t.Fatalf("drop plan broadened to database scope: %s", statements[index])
		}
	}
}

func TestBuildDropStatementsBatchesLargeInventoriesWithinConfirmedSchema(t *testing.T) {
	tables := make([]string, dropObjectBatchSize+1)
	for index := range tables {
		tables[index] = fmt.Sprintf("legacy_%03d", index)
	}
	statements := buildDropStatements("only_this_schema", dropInventory{tables: tables})
	if len(statements) != 2 {
		t.Fatalf("DROP batches=%d, want 2", len(statements))
	}
	for _, statement := range statements {
		if !strings.HasPrefix(statement, "DROP TABLE IF EXISTS ") {
			t.Fatalf("unexpected statement: %s", statement)
		}
		for _, part := range strings.Split(strings.TrimPrefix(statement, "DROP TABLE IF EXISTS "), ", ") {
			if !strings.HasPrefix(part, "`only_this_schema`.") {
				t.Fatalf("object is not bound to confirmed schema: %s", part)
			}
		}
	}
}

func TestBuildDropStatementsDoesNotInventObjectsForEmptySchema(t *testing.T) {
	if statements := buildDropStatements("cpamp", dropInventory{}); len(statements) != 0 {
		t.Fatalf("empty inventory statements=%#v", statements)
	}
}
