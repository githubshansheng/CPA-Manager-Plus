package schema

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
)

func TestMySQLPhysicalContractAcceptsCanonicalSchema(t *testing.T) {
	manifest := Current()
	actual := canonicalActualMySQLSchema(manifest)
	result := validateMySQLPhysicalSchema(manifest, actual)
	if !result.Valid || len(result.Differences) != 0 {
		t.Fatalf("canonical schema rejected: %#v", result.Differences)
	}
}

func TestMySQLPhysicalContractContainsEveryManifestColumnAndRequiredAuxiliary(t *testing.T) {
	manifest := Current()
	contract := mysqlPhysicalContract(manifest)
	generated := 0
	logicalPrimaryKeyMappings := 0
	for _, table := range manifest.Tables {
		if table.SQLiteOnly {
			continue
		}
		physical, exists := contract[table.Name]
		if !exists {
			t.Fatalf("missing physical table contract %s", table.Name)
		}
		for _, column := range table.Columns {
			if _, exists := physical.columns[column.Name]; !exists {
				t.Errorf("manifest column omitted from physical contract: %s.%s", table.Name, column.Name)
			}
		}
		if _, exists := physical.indexes["uq_cpamp_logical_pk"]; exists {
			logicalPrimaryKeyMappings++
		}
		for _, column := range physical.columns {
			if column.generated {
				generated++
				if strings.TrimSpace(column.generationExpr) == "" {
					t.Errorf("generated column in %s has no expression contract", table.Name)
				}
			}
		}
	}
	if generated == 0 {
		t.Fatal("physical contract contains no generated auxiliary columns")
	}
	if logicalPrimaryKeyMappings == 0 {
		t.Fatal("physical contract did not exercise logical primary-key mapping")
	}
}

func TestMySQLCompositeIndexesDoNotOversizeBoundedTextPrefixes(t *testing.T) {
	contract := mysqlPhysicalContract(Current())
	outbox, exists := contract["database_outbox"]
	if !exists {
		t.Fatal("database_outbox physical contract is missing")
	}
	index, exists := outbox.indexes["database_outbox_group_idx"]
	if !exists {
		t.Fatal("database_outbox_group_idx is missing")
	}
	if len(index.columns) != 2 || index.columns[0].name != "transaction_id" {
		t.Fatalf("unexpected database_outbox_group_idx columns: %#v", index.columns)
	}
	if index.columns[0].prefix != 0 {
		t.Fatalf("bounded VARCHAR transaction_id must use its full key, got prefix %d", index.columns[0].prefix)
	}

	longText := Column{Name: "payload", Kind: KindText, MySQLType: "LONGTEXT"}
	if got := mysqlIndexPrefixLength(longText, false, 191); got != 191 {
		t.Fatalf("LONGTEXT must retain a bounded index prefix, got %d", got)
	}
	wideText := Column{Name: "value", Kind: KindText, MySQLType: "VARCHAR(512)"}
	if got := mysqlIndexPrefixLength(wideText, true, 191); got != 191 {
		t.Fatalf("wide composite VARCHAR must be prefix-bounded, got %d", got)
	}
}

func TestMySQLHashedUniqueKeyDoesNotDependOnCascadingForeignKeyColumn(t *testing.T) {
	contract := mysqlPhysicalContract(Current())
	results := contract["codex_inspection_results"]
	hashColumn, exists := results.columns["__cpamp_uq_2_hash"]
	if !exists {
		t.Fatal("codex inspection result hash column is missing")
	}
	if strings.Contains(hashColumn.generationExpr, "`run_id`") || !strings.Contains(hashColumn.generationExpr, "`account_key`") {
		t.Fatalf("result hash must exclude cascading run_id and include account_key: %s", hashColumn.generationExpr)
	}
	index, exists := results.indexes["uq_cpamp_hash_2"]
	if !exists || len(index.columns) != 2 || index.columns[0].name != "run_id" || index.columns[1].name != "__cpamp_uq_2_hash" {
		t.Fatalf("unexpected result unique key mapping: %#v", index.columns)
	}
}

func TestMySQLPhysicalContractRejectsIncompatibleNonEmptySchema(t *testing.T) {
	manifest := Current()
	tests := []struct {
		name    string
		mutate  func(map[string]actualMySQLTable)
		problem string
	}{
		{name: "missing table", mutate: func(actual map[string]actualMySQLTable) { delete(actual, "usage_events") }, problem: "missing table"},
		{name: "unexpected table", mutate: func(actual map[string]actualMySQLTable) {
			actual["unrelated"] = actualMySQLTable{columns: map[string]actualMySQLColumn{}, indexes: map[string]physicalIndexContract{},
				engine: "innodb", collation: "utf8mb4_0900_bin"}
		}, problem: "unexpected table"},
		{name: "unexpected column", mutate: func(actual map[string]actualMySQLTable) {
			table := actual["usage_events"]
			table.columns["silently_added"] = actualMySQLColumn{dataType: "bigint", columnType: "bigint"}
			actual["usage_events"] = table
		}, problem: "unexpected column"},
		{name: "type", mutate: mutateColumn("usage_events", "input_tokens", func(column *actualMySQLColumn) {
			column.dataType, column.columnType = "int", "int"
		}), problem: "type"},
		{name: "nullability", mutate: mutateColumn("usage_events", "input_tokens", func(column *actualMySQLColumn) {
			column.nullable = !column.nullable
		}), problem: "NULL semantics differ"},
		{name: "default value", mutate: mutateColumn("usage_events", "input_tokens", func(column *actualMySQLColumn) {
			column.defaultValue = sql.NullString{String: "9", Valid: true}
		}), problem: "default"},
		{name: "default unexpectedly present", mutate: mutateColumn("usage_events", "model", func(column *actualMySQLColumn) {
			column.defaultValue = sql.NullString{String: "", Valid: true}
		}), problem: "default"},
		{name: "auto increment", mutate: mutateColumn("usage_events", "id", func(column *actualMySQLColumn) {
			column.extra = ""
		}), problem: "AUTO_INCREMENT"},
		{name: "storage engine", mutate: func(actual map[string]actualMySQLTable) {
			table := actual["usage_events"]
			table.engine = "myisam"
			actual["usage_events"] = table
		}, problem: "storage engine"},
		{name: "table collation", mutate: func(actual map[string]actualMySQLTable) {
			table := actual["usage_events"]
			table.collation = "utf8mb4_0900_ai_ci"
			actual["usage_events"] = table
		}, problem: "table collation"},
		{name: "primary key", mutate: func(actual map[string]actualMySQLTable) {
			table := actual["usage_events"]
			delete(table.indexes, "PRIMARY")
			actual["usage_events"] = table
		}, problem: "missing index PRIMARY"},
		{name: "index uniqueness", mutate: mutateFirstRequiredIndex(func(index *physicalIndexContract) {
			index.unique = !index.unique
		}), problem: "differs from manifest mapping"},
		{name: "index order", mutate: mutateFirstCompositeIndex(func(index *physicalIndexContract) {
			index.columns[0], index.columns[1] = index.columns[1], index.columns[0]
		}), problem: "differs from manifest mapping"},
		{name: "index prefix", mutate: mutateFirstPrefixedIndex(func(index *physicalIndexContract) {
			index.columns[0].prefix++
		}), problem: "differs from manifest mapping"},
		{name: "generated expression", mutate: mutateFirstGeneratedColumn(func(column *actualMySQLColumn) {
			column.generationExpr = "UNHEX(SHA2('wrong', 256))"
		}), problem: "generation expression differs"},
		{name: "generated marker", mutate: mutateFirstGeneratedColumn(func(column *actualMySQLColumn) {
			column.extra = ""
		}), problem: "generated=false"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := canonicalActualMySQLSchema(manifest)
			test.mutate(actual)
			result := validateMySQLPhysicalSchema(manifest, actual)
			if result.Valid {
				t.Fatal("incompatible non-empty schema was accepted")
			}
			if !containsDifference(result.Differences, test.problem) {
				t.Fatalf("differences %#v do not contain %q", result.Differences, test.problem)
			}
		})
	}
}

func TestMySQLDefaultAndGenerationExpressionNormalization(t *testing.T) {
	for input, want := range map[string]string{
		"BIGINT(20)":          "bigint",
		"BIGINT(20) UNSIGNED": "bigint unsigned",
		"TINYINT(4)":          "tinyint",
		"INT(11)":             "int",
	} {
		if got := normalizeMySQLColumnType(input); got != want {
			t.Errorf("normalizeMySQLColumnType(%q) = %q, want %q", input, got, want)
		}
	}
	empty := "('')"
	if !mysqlDefaultsEquivalent(&empty, sql.NullString{String: "", Valid: true}) {
		t.Fatal("MySQL LONGTEXT expression default should equal its information_schema value")
	}
	if !mysqlDefaultsEquivalent(&empty, sql.NullString{String: `_utf8mb4\'\'`, Valid: true}) {
		t.Fatal("MySQL escaped charset LONGTEXT default should normalize to the empty string")
	}
	zero := "0.0"
	if !mysqlDefaultsEquivalent(&zero, sql.NullString{String: "0", Valid: true}) {
		t.Fatal("numeric defaults with the same value should compare equal")
	}
	if mysqlDefaultsEquivalent(nil, sql.NullString{String: "", Valid: true}) {
		t.Fatal("no default must not equal an explicit empty-string default")
	}
	left := "CASE WHEN `status` = 'active' THEN UNHEX(SHA2(CAST(JSON_ARRAY(`id`) AS CHAR CHARACTER SET utf8mb4), 256)) ELSE NULL END"
	right := "case when `status` = _utf8mb4'active' then unhex(sha2(cast(json_array(`id`) as char charset utf8mb4),256)) else null end"
	if normalizeGenerationExpression(left) != normalizeGenerationExpression(right) {
		t.Fatal("equivalent MySQL generation expressions did not normalize equally")
	}
	escaped := "case when (`status` = _utf8mb4\\'active\\') then unhex(sha2(cast(json_array(`id`) as char charset utf8mb4),256)) else null end"
	if normalizeGenerationExpression(left) != normalizeGenerationExpression(escaped) {
		t.Fatal("escaped INFORMATION_SCHEMA generation expression did not normalize equally")
	}
	isNullOperator := "CASE WHEN `actual_end_ms` IS NULL THEN `activation_id` ELSE NULL END"
	isNullFunction := "(case when isnull(`actual_end_ms`) then `activation_id` else NULL end)"
	if normalizeGenerationExpression(isNullOperator) != normalizeGenerationExpression(isNullFunction) {
		t.Fatal("MySQL 8.0.12 ISNULL rendering did not normalize to the canonical operator")
	}
	if mysqlColumnIsGenerated("DEFAULT_GENERATED") {
		t.Fatal("expression defaults must not be classified as generated columns")
	}
	if !mysqlColumnIsGenerated("STORED GENERATED") {
		t.Fatal("stored generated column marker was not recognized")
	}
}

func TestManagedMySQLJournalTriggerIdentity(t *testing.T) {
	trigger := actualTrigger{table: "usage_events", timing: "AFTER", event: "INSERT"}
	if !isManagedMySQLJournalTrigger("cpamp_mysql_journal_7c2949493b83_insert", trigger) {
		t.Fatal("canonical authority journal trigger was rejected")
	}
	for _, test := range []struct {
		name    string
		trigger actualTrigger
	}{
		{name: "cpamp_mysql_journal_000000000000_insert", trigger: trigger},
		{name: "cpamp_mysql_journal_7c2949493b83_delete", trigger: trigger},
		{name: "cpamp_mysql_journal_7c2949493b83_insert", trigger: actualTrigger{table: "usage_events", timing: "BEFORE", event: "INSERT"}},
	} {
		if isManagedMySQLJournalTrigger(test.name, test.trigger) {
			t.Fatalf("non-canonical journal trigger %q/%#v was accepted", test.name, test.trigger)
		}
	}
}

func TestMySQL8012PhysicalContractOmitsUnsupportedLongTextDefaults(t *testing.T) {
	manifest := Current()
	legacyFeatures := mysqlSchemaFeatures{
		longTextDefaults: false,
		collation:        database.MySQLLegacyNoPadCollation,
	}
	table := findTable(manifest, "account_quota_snapshots")
	legacyDDL := createTableDDLForFeatures(table, legacyFeatures)
	if !strings.Contains(legacyDDL, "`scope_fingerprint` LONGTEXT NOT NULL") {
		t.Fatalf("MySQL 8.0.12 DDL lost the required non-null LONGTEXT column:\n%s", legacyDDL)
	}
	if strings.Contains(legacyDDL, "`scope_fingerprint` LONGTEXT NOT NULL DEFAULT") {
		t.Fatalf("MySQL 8.0.12 DDL retained an unsupported LONGTEXT default:\n%s", legacyDDL)
	}
	if !strings.Contains(legacyDDL, "COLLATE "+database.MySQLLegacyNoPadCollation) {
		t.Fatalf("MySQL 8.0.12 DDL selected an unavailable table collation:\n%s", legacyDDL)
	}
	modernDDL := createTableDDL(table)
	if !strings.Contains(modernDDL, "`scope_fingerprint` LONGTEXT NOT NULL DEFAULT ('')") {
		t.Fatalf("modern MySQL DDL lost the canonical LONGTEXT default:\n%s", modernDDL)
	}

	legacyActual := canonicalActualMySQLSchemaForFeatures(manifest, legacyFeatures)
	legacyResult := validateMySQLPhysicalSchemaForFeatures(manifest, legacyActual, legacyFeatures)
	if !legacyResult.Valid {
		t.Fatalf("MySQL 8.0.12 compatibility schema rejected: %#v", legacyResult.Differences)
	}
	modernResult := validateMySQLPhysicalSchema(manifest, legacyActual)
	if modernResult.Valid || !containsDifference(modernResult.Differences, "default") {
		t.Fatalf("modern MySQL contract accepted missing LONGTEXT defaults: %#v", modernResult.Differences)
	}
}

func canonicalActualMySQLSchema(manifest Manifest) map[string]actualMySQLTable {
	return canonicalActualMySQLSchemaForFeatures(manifest, modernMySQLSchemaFeatures)
}

func canonicalActualMySQLSchemaForFeatures(manifest Manifest, features mysqlSchemaFeatures) map[string]actualMySQLTable {
	physical := mysqlPhysicalContractForFeatures(manifest, features)
	actual := make(map[string]actualMySQLTable, len(physical))
	for tableName, table := range physical {
		item := actualMySQLTable{columns: map[string]actualMySQLColumn{}, indexes: map[string]physicalIndexContract{},
			engine: table.engine, collation: table.collation}
		for name, column := range table.columns {
			extra := ""
			if column.autoIncrement {
				extra = "auto_increment"
			}
			if column.generated {
				extra = "STORED GENERATED"
			}
			defaultValue := sql.NullString{}
			if column.defaultValue != nil {
				defaultValue = sql.NullString{String: *column.defaultValue, Valid: true}
			}
			item.columns[name] = actualMySQLColumn{dataType: mysqlDataType(column.columnType),
				columnType: column.columnType, nullable: column.nullable, characterSet: column.characterSet,
				collation: column.collation, defaultValue: defaultValue, extra: extra, generationExpr: column.generationExpr}
		}
		for name, index := range table.indexes {
			copyIndex := index
			copyIndex.columns = append([]physicalIndexColumn(nil), index.columns...)
			item.indexes[name] = copyIndex
		}
		actual[tableName] = item
	}
	return actual
}

func mutateColumn(table, name string, mutate func(*actualMySQLColumn)) func(map[string]actualMySQLTable) {
	return func(actual map[string]actualMySQLTable) {
		item := actual[table]
		column := item.columns[name]
		mutate(&column)
		item.columns[name] = column
		actual[table] = item
	}
}

func mutateFirstGeneratedColumn(mutate func(*actualMySQLColumn)) func(map[string]actualMySQLTable) {
	return func(actual map[string]actualMySQLTable) {
		for tableName, table := range actual {
			for name, column := range table.columns {
				if !strings.Contains(strings.ToLower(column.extra), "generated") {
					continue
				}
				mutate(&column)
				table.columns[name] = column
				actual[tableName] = table
				return
			}
		}
		panic("canonical schema contains no generated column")
	}
}

func mutateFirstRequiredIndex(mutate func(*physicalIndexContract)) func(map[string]actualMySQLTable) {
	return func(actual map[string]actualMySQLTable) {
		for tableName, table := range actual {
			for name, index := range table.indexes {
				if name == "PRIMARY" {
					continue
				}
				mutate(&index)
				table.indexes[name] = index
				actual[tableName] = table
				return
			}
		}
		panic("canonical schema contains no secondary index")
	}
}

func mutateFirstCompositeIndex(mutate func(*physicalIndexContract)) func(map[string]actualMySQLTable) {
	return func(actual map[string]actualMySQLTable) {
		for tableName, table := range actual {
			for name, index := range table.indexes {
				if len(index.columns) < 2 {
					continue
				}
				mutate(&index)
				table.indexes[name] = index
				actual[tableName] = table
				return
			}
		}
		panic("canonical schema contains no composite index")
	}
}

func mutateFirstPrefixedIndex(mutate func(*physicalIndexContract)) func(map[string]actualMySQLTable) {
	return func(actual map[string]actualMySQLTable) {
		for tableName, table := range actual {
			for name, index := range table.indexes {
				for _, column := range index.columns {
					if column.prefix == 0 {
						continue
					}
					mutate(&index)
					table.indexes[name] = index
					actual[tableName] = table
					return
				}
			}
		}
		panic("canonical schema contains no prefixed index")
	}
}

func containsDifference(differences []Difference, fragment string) bool {
	for _, difference := range differences {
		if strings.Contains(difference.Problem, fragment) {
			return true
		}
	}
	return false
}
