package schema

import (
	"strings"
	"testing"
)

func TestManifestContainsEveryCriticalAuthoritativeField(t *testing.T) {
	manifest := Current()
	usage := findTable(manifest, "usage_events")
	for _, name := range []string{
		"raw_json", "fail_body", "client_ip", "x_forwarded_for", "normalized_uncached_input_tokens",
		"normalized_total_input_tokens", "normalized_cache_read_tokens", "normalized_cache_creation_tokens",
		"request_service_tier", "response_service_tier", "response_metadata_json",
	} {
		column := findColumn(usage, name)
		if column.Name == "" {
			t.Errorf("usage_events.%s missing", name)
			continue
		}
		if (name == "raw_json" || name == "fail_body" || name == "response_metadata_json") && column.MySQLType != "LONGTEXT" {
			t.Errorf("usage_events.%s type=%s, want LONGTEXT", name, column.MySQLType)
		}
	}
	for _, tableName := range []string{"settings", "model_prices", "model_price_context_tiers", "model_price_service_tiers",
		"api_key_aliases", "dead_letter_events", "account_action_candidates", "quota_cooldowns",
		"account_quota_observations", "account_quota_windows", "account_quota_window_activations",
		"account_quota_cycles", "account_quota_snapshots", "codex_inspection_runs", "codex_inspection_results"} {
		if findTable(manifest, tableName).Name == "" {
			t.Errorf("authoritative table %s missing", tableName)
		}
	}
}

func TestManifestRejectsAmbiguousPrimaryKeyAndIndexContracts(t *testing.T) {
	base := Manifest{Version: 1, Tables: []Table{{Name: "items", Class: ClassAuthoritative, Columns: []Column{
		{Name: "id", Kind: KindInteger, MySQLType: "BIGINT", PrimaryKeyPosition: 1},
	}}}}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid baseline manifest: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "primary key gap", mutate: func(manifest *Manifest) {
			manifest.Tables[0].Columns = append(manifest.Tables[0].Columns,
				Column{Name: "scope", Kind: KindText, MySQLType: "LONGTEXT", PrimaryKeyPosition: 3})
		}},
		{name: "invalid auto increment", mutate: func(manifest *Manifest) {
			manifest.Tables[0].Columns[0].Kind = KindText
			manifest.Tables[0].Columns[0].MySQLType = "LONGTEXT"
			manifest.Tables[0].Columns[0].AutoIncrement = true
		}},
		{name: "duplicate index", mutate: func(manifest *Manifest) {
			manifest.Tables[0].Indexes = []Index{{Name: "idx_items_id", Columns: []string{"id"}},
				{Name: "idx_items_id", Columns: []string{"id"}}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := cloneManifest(base)
			test.mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatal("ambiguous manifest unexpectedly validated")
			}
		})
	}
}

func TestManifestContainsReplicationVersionLedger(t *testing.T) {
	table := findTable(Current(), "database_row_versions")
	if !table.MySQLOnly {
		t.Fatal("database_row_versions missing or not MySQL-only")
	}
	for _, name := range []string{"table_name", "primary_key_hash", "primary_key_json", "row_version", "mutation_id", "updated_at_ms"} {
		if findColumn(table, name).Name == "" {
			t.Errorf("database_row_versions.%s missing", name)
		}
	}
	ddl := createTableDDL(table)
	if !strings.Contains(ddl, "PRIMARY KEY (`table_name`, `primary_key_hash`)") && !strings.Contains(ddl, "UNIQUE KEY `uq_cpamp_logical_pk`") {
		t.Fatalf("row version DDL lacks logical uniqueness:\n%s", ddl)
	}
}

func TestCodexLegacyIdentityEvidenceIsDerived(t *testing.T) {
	table := findTable(Current(), "usage_codex_legacy_identity_evidence_v1")
	if table.Name == "" {
		t.Fatal("Codex legacy identity evidence table is missing")
	}
	if table.Class != ClassDerived {
		t.Fatalf("Codex legacy identity evidence class = %q, want %q", table.Class, ClassDerived)
	}
}

func TestCreateTableDDLPreservesWideUsageFields(t *testing.T) {
	ddl := createTableDDL(findTable(Current(), "usage_events"))
	for _, fragment := range []string{"`id` BIGINT NOT NULL AUTO_INCREMENT", "`raw_json` LONGTEXT NULL", "`fail_body` LONGTEXT NULL", "utf8mb4_0900_bin"} {
		if !strings.Contains(ddl, fragment) {
			t.Errorf("usage_events DDL missing %q", fragment)
		}
	}
}

func TestCreateTableDDLRetainsConditionalIdentityConstraints(t *testing.T) {
	for tableName, indexName := range map[string]string{
		"account_action_candidates":        "idx_account_action_candidates_pending_identity_action",
		"quota_cooldowns":                  "idx_quota_cooldowns_active_identity",
		"account_quota_window_activations": "idx_quota_activations_active",
		"account_quota_cycles":             "idx_quota_cycles_active",
	} {
		ddl := createTableDDL(findTable(Current(), tableName))
		if !strings.Contains(ddl, "UNIQUE KEY `"+indexName+"`") || !strings.Contains(ddl, "GENERATED ALWAYS") {
			t.Errorf("%s DDL lost conditional identity constraint:\n%s", tableName, ddl)
		}
	}
}

func TestMySQLQuotaLifecyclePartialIndexesUseNullableActiveKeys(t *testing.T) {
	tests := []struct {
		table, index, generated, terminal string
	}{
		{"account_quota_window_activations", "idx_quota_activations_active", "__cpamp_active_window_id", "deactivated_at_ms"},
		{"account_quota_cycles", "idx_quota_cycles_active", "__cpamp_active_activation_id", "actual_end_ms"},
	}
	for _, test := range tests {
		t.Run(test.table, func(t *testing.T) {
			ddl := createTableDDL(findTable(Current(), test.table))
			for _, fragment := range []string{
				"GENERATED ALWAYS AS (CASE WHEN `" + test.terminal + "` IS NULL",
				"UNIQUE KEY `" + test.index + "` (`" + test.generated + "`)",
			} {
				if !strings.Contains(ddl, fragment) {
					t.Errorf("%s DDL missing %q:\n%s", test.table, fragment, ddl)
				}
			}
			if !mysqlConditionalUniqueIndex(test.table, test.index) {
				t.Fatalf("%s is not excluded from the generic unconditional index path", test.index)
			}
		})
	}
}

func TestEveryManifestForeignKeyHasMySQLEnforcementContract(t *testing.T) {
	manifest := Current()
	logicalGroups := 0
	for _, table := range manifest.Tables {
		seen := map[string]bool{}
		for _, fk := range table.ForeignKeys {
			if !seen[fk.Name] {
				seen[fk.Name] = true
				logicalGroups++
			}
		}
	}
	contracts := foreignKeyContracts(manifest)
	if len(contracts) != logicalGroups {
		t.Fatalf("MySQL foreign-key contracts=%d, manifest groups=%d", len(contracts), logicalGroups)
	}
	emulated := map[string]foreignKeyContract{}
	for _, contract := range contracts {
		if contract.Emulated {
			emulated[contract.Table.Name+"/"+contract.Name] = contract
		}
	}
	for _, key := range []string{
		"model_price_context_tiers/fk_model_price_context_tiers_0",
		"model_price_service_tiers/fk_model_price_service_tiers_0",
	} {
		contract, exists := emulated[key]
		if !exists {
			t.Errorf("LONGTEXT relationship %s lacks emulated contract", key)
			continue
		}
		childDDL := createTableDDL(contract.Table)
		parentDDL := createTableDDL(contract.RefTable)
		for _, fragment := range []string{childHashColumn(contract), childHashIndex(contract)} {
			if !strings.Contains(childDDL, fragment) {
				t.Errorf("child DDL for %s lacks %s", key, fragment)
			}
		}
		for _, fragment := range []string{referencedHashColumn(contract), referencedHashIndex(contract)} {
			if !strings.Contains(parentDDL, fragment) {
				t.Errorf("parent DDL for %s lacks %s", key, fragment)
			}
		}
		joined := ""
		for _, trigger := range emulatedForeignKeyTriggers(contract) {
			joined += trigger.DDL
		}
		for _, fragment := range []string{
			"BEFORE INSERT", "BEFORE UPDATE", "AFTER DELETE", "SIGNAL SQLSTATE '45000'",
			"DELETE FROM `" + contract.Table.Name + "`", "<=>", "SHA2",
		} {
			if !strings.Contains(joined, fragment) {
				t.Errorf("foreign-key triggers for %s lack %q:\n%s", key, fragment, joined)
			}
		}
	}
	if len(emulated) != 2 {
		t.Errorf("emulated foreign-key contracts=%d, want the two current LONGTEXT relationships", len(emulated))
	}
}

func TestEmulatedForeignKeySupportsCompositeNullAndActions(t *testing.T) {
	parent := Table{Name: "parent", Columns: []Column{
		{Name: "tenant", Kind: KindText, MySQLType: "LONGTEXT"},
		{Name: "external_id", Kind: KindText, MySQLType: "LONGTEXT", Nullable: true},
	}}
	child := Table{Name: "child", Columns: []Column{
		{Name: "tenant", Kind: KindText, MySQLType: "LONGTEXT"},
		{Name: "external_id", Kind: KindText, MySQLType: "LONGTEXT", Nullable: true},
	}}
	contract := foreignKeyContract{
		Name: "fk_child_parent", Table: child, Columns: child.Columns, RefTable: parent,
		RefColumns: parent.Columns, OnUpdate: "CASCADE", OnDelete: "SET NULL", Emulated: true,
	}
	if expression := hashExpression(contract.Columns); !strings.Contains(expression, "CASE WHEN `external_id` IS NULL") {
		t.Fatalf("nullable composite hash does not preserve SQL NULL semantics: %s", expression)
	}
	var joined string
	for _, trigger := range emulatedForeignKeyTriggers(contract) {
		joined += trigger.DDL
	}
	for _, fragment := range []string{
		"NEW.`tenant` IS NULL OR NEW.`external_id` IS NULL",
		"AFTER UPDATE", "`tenant` = NEW.`tenant`", "`external_id` = NEW.`external_id`",
		"AFTER DELETE", "`tenant` = NULL", "`external_id` = NULL",
		"`cpamp_parent`.`tenant` <=> NEW.`tenant`",
		"`cpamp_parent`.`external_id` <=> NEW.`external_id`",
	} {
		if !strings.Contains(joined, fragment) {
			t.Errorf("composite foreign-key trigger lacks %q:\n%s", fragment, joined)
		}
	}
}
