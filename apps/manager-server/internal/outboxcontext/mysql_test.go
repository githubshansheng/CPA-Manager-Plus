package outboxcontext

import (
	"strconv"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
)

func TestMySQLJournalTriggersCoverEveryAuthoritativeField(t *testing.T) {
	for _, table := range schema.Current().AuthoritativeTables() {
		statements, err := buildMySQLJournalTriggers(table)
		if err != nil {
			t.Fatalf("build %s journal: %v", table.Name, err)
		}
		if len(statements) != 3 {
			t.Fatalf("%s trigger count=%d, want insert/delete/update", table.Name, len(statements))
		}
		joined := strings.Join(statements, "\n")
		for _, column := range table.Columns {
			if !strings.Contains(joined, mysqlQuoteString(strconv.Quote(column.Name)+":")) ||
				!strings.Contains(joined, "."+mysqlQuoteIdentifier(column.Name)) {
				t.Errorf("%s journal omits complete field %s", table.Name, column.Name)
			}
		}
		for _, fragment := range []string{
			"@cpamp_transaction_id", "@cpamp_source_epoch", "database_mysql_authority_counter",
			"'mysql','sqlite'", "primary_key_json", "payload_json", "SIGNAL SQLSTATE '45000'",
		} {
			if !strings.Contains(joined, fragment) {
				t.Errorf("%s journal missing safety fragment %q", table.Name, fragment)
			}
		}
	}
}

func TestMySQLUpdateJournalEmitsDeleteInsertWhenPrimaryKeyChanges(t *testing.T) {
	table := schema.Current().AuthoritativeTables()[0]
	for _, candidate := range schema.Current().AuthoritativeTables() {
		if candidate.Name == "settings" {
			table = candidate
			break
		}
	}
	statements, err := buildMySQLJournalTriggers(table)
	if err != nil {
		t.Fatal(err)
	}
	update := statements[2]
	for _, fragment := range []string{
		"NOT (OLD.`key` <=> NEW.`key`)", "'delete'", "'insert'", "'update'",
	} {
		if !strings.Contains(update, fragment) {
			t.Errorf("primary-key update trigger missing %q:\n%s", fragment, update)
		}
	}
}

func TestMySQLTypedJournalValuesPreserveNullAndLogicalKinds(t *testing.T) {
	table := schema.Table{Columns: []schema.Column{
		{Name: "integer_value", Kind: schema.KindInteger},
		{Name: "real_value", Kind: schema.KindReal},
		{Name: "text_value", Kind: schema.KindText},
		{Name: "blob_value", Kind: schema.KindBlob},
	}}
	expression := mysqlTypedJSONObject(table.Columns, "NEW")
	for _, fragment := range []string{
		`{"type":"null"}`, `{"type":"integer","value":`,
		`{"type":"real","value":`, `{"type":"text","value":`,
		`{"hex":`, `,"type":"bytes"}`, "HEX(NEW.`blob_value`)",
	} {
		if !strings.Contains(expression, fragment) {
			t.Errorf("typed JSON expression missing %q: %s", fragment, expression)
		}
	}
}

func TestMySQLJournalDigestUsesCanonicalLengthPrefixedEnvelope(t *testing.T) {
	table := schema.Table{Name: "example", Columns: []schema.Column{
		{Name: "id", Kind: schema.KindInteger, PrimaryKeyPosition: 1},
		{Name: "value", Kind: schema.KindText},
	}}
	body, err := mysqlMutationEmission(table, "update", "NEW")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"cpamp_mutation_id=SHA2(CONCAT(@cpamp_transaction_id,':',cpamp_sequence_no),256)",
		"cpamp_mutation_digest=SHA2(CONCAT(",
		"UNHEX(LPAD(HEX(OCTET_LENGTH(",
		"CAST(cpamp_primary_key AS BINARY)",
		"CAST(cpamp_payload AS BINARY)",
		"VALUES (cpamp_mutation_id,cpamp_mutation_digest",
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("MySQL journal digest is missing %q:\n%s", fragment, body)
		}
	}
}

func TestMySQLJournalTriggerNamesAreStableUniqueAndBounded(t *testing.T) {
	seen := map[string]string{}
	for _, table := range schema.Current().AuthoritativeTables() {
		for _, name := range mysqlJournalTriggerNames(table.Name) {
			if len(name) > 64 {
				t.Errorf("trigger name %s is longer than MySQL's identifier limit", name)
			}
			if previous, exists := seen[name]; exists {
				t.Errorf("trigger name %s collides for %s and %s", name, previous, table.Name)
			}
			seen[name] = table.Name
		}
	}
}
