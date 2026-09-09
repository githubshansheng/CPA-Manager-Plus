package schema_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	. "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestManifestMatchesFreshSQLite(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "schema.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := sqliterepo.RunDerivedStartupMaintenance(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	actual, err := inspectSQLiteManifest(db)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CPAMP_GENERATE_SCHEMA_MANIFEST") == "1" {
		actual.Tables = append(actual.Tables, mysqlInternalTables()...)
		if err := writeGeneratedManifest(actual); err != nil {
			t.Fatal(err)
		}
		return
	}
	want := Current()
	wantTables := map[string]Table{}
	for _, table := range want.Tables {
		if !table.MySQLOnly {
			wantTables[table.Name] = table
		}
	}
	actualTables := map[string]Table{}
	for _, table := range actual.Tables {
		actualTables[table.Name] = table
	}
	for name, actualTable := range actualTables {
		wantTable, exists := wantTables[name]
		if !exists {
			t.Errorf("SQLite table %s is missing from manifest", name)
			continue
		}
		wantColumns := map[string]Column{}
		for _, column := range wantTable.Columns {
			wantColumns[column.Name] = column
		}
		if len(wantColumns) != len(actualTable.Columns) {
			t.Errorf("table %s manifest columns=%d SQLite columns=%d", name, len(wantColumns), len(actualTable.Columns))
		}
		wantIndexes := map[string]Index{}
		for _, index := range wantTable.Indexes {
			wantIndexes[index.Name] = index
		}
		actualIndexes := map[string]Index{}
		for _, index := range actualTable.Indexes {
			actualIndexes[index.Name] = index
		}
		if !reflect.DeepEqual(wantIndexes, actualIndexes) {
			t.Errorf("table %s index manifest differs from fresh SQLite", name)
		}
		wantForeignKeys := map[string]ForeignKey{}
		for _, fk := range wantTable.ForeignKeys {
			wantForeignKeys[fk.Name+"/"+fk.Column] = fk
		}
		actualForeignKeys := map[string]ForeignKey{}
		for _, fk := range actualTable.ForeignKeys {
			actualForeignKeys[fk.Name+"/"+fk.Column] = fk
		}
		if !reflect.DeepEqual(wantForeignKeys, actualForeignKeys) {
			t.Errorf("table %s foreign-key manifest differs from fresh SQLite", name)
		}
		if strings.Join(strings.Fields(wantTable.SourceDDL), " ") != strings.Join(strings.Fields(actualTable.SourceDDL), " ") {
			t.Errorf("table %s source DDL differs from manifest", name)
		}
		for _, actualColumn := range actualTable.Columns {
			wantColumn, exists := wantColumns[actualColumn.Name]
			if !exists {
				t.Errorf("SQLite column %s.%s is missing from manifest", name, actualColumn.Name)
				continue
			}
			if wantColumn.Kind != actualColumn.Kind || wantColumn.Nullable != actualColumn.Nullable ||
				normalizedDefault(wantColumn.Default) != normalizedDefault(actualColumn.Default) ||
				wantColumn.PrimaryKeyPosition != actualColumn.PrimaryKeyPosition || wantColumn.AutoIncrement != actualColumn.AutoIncrement {
				t.Errorf("column %s.%s mismatch: manifest=%#v SQLite=%#v", name, actualColumn.Name, wantColumn, actualColumn)
			}
		}
	}
	for name := range wantTables {
		if _, exists := actualTables[name]; !exists {
			t.Errorf("manifest table %s is absent from fresh SQLite", name)
		}
	}
}

func TestMySQLInternalManifestMatchesMigrationRepositoryContract(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "migration-contract.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := databasemigration.NewSQLRepository(db, databasemigration.DialectSQLite).EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, table := range Current().Tables {
		if !table.MySQLOnly {
			continue
		}
		// The reverse-Outbox row-version counter exists only on the MySQL
		// authority side and therefore has no SQLite migration-repository table.
		if table.Name == "database_mysql_authority_counter" {
			if !reflect.DeepEqual(table.Columns, []Column{
				{Name: "id", Kind: KindInteger, Nullable: false, PrimaryKeyPosition: 1, MySQLType: "INT"},
				{Name: "last_row_version", Kind: KindInteger, Nullable: false, MySQLType: "BIGINT"},
			}) {
				t.Errorf("mysql authority counter manifest differs from reverse-Outbox contract: %#v", table.Columns)
			}
			continue
		}
		rows, err := db.Query(`pragma table_info(` + quoteSQLite(table.Name) + `)`)
		if err != nil {
			t.Fatal(err)
		}
		var actual []Column
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, typeName string
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &name, &typeName, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			kind := sqliteKind(typeName)
			// The repository contract targets both engines; MySQL PRIMARY KEY
			// columns are non-null even when SQLite's PRAGMA reports otherwise.
			nullable := notNull == 0 && primaryKey == 0
			actual = append(actual, Column{Name: name, Kind: kind, Nullable: nullable, PrimaryKeyPosition: primaryKey,
				AutoIncrement: primaryKey == 1 && ((table.Name == "database_outbox" && name == "outbox_id") ||
					(table.Name == "database_migration_events" && name == "event_id"))})
		}
		rows.Close()
		if len(actual) != len(table.Columns) {
			t.Errorf("migration table %s columns=%d, manifest=%d", table.Name, len(actual), len(table.Columns))
			continue
		}
		for i, column := range actual {
			want := table.Columns[i]
			if column.Name != want.Name || column.Kind != want.Kind || column.Nullable != want.Nullable ||
				column.PrimaryKeyPosition != want.PrimaryKeyPosition || column.AutoIncrement != want.AutoIncrement {
				t.Errorf("migration contract %s.%s mismatch: repository=%#v manifest=%#v", table.Name, column.Name, column, want)
			}
		}
	}
}

func inspectSQLiteManifest(db *sql.DB) (Manifest, error) {
	rows, err := db.Query(`select name, sql from sqlite_master where type='table' and name not like 'sqlite_%' order by name`)
	if err != nil {
		return Manifest{}, err
	}
	defer rows.Close()
	type tableDDL struct{ name, ddl string }
	var rawTables []tableDDL
	for rows.Next() {
		var item tableDDL
		if err := rows.Scan(&item.name, &item.ddl); err != nil {
			return Manifest{}, err
		}
		if isFTSShadowTable(item.name) {
			continue
		}
		rawTables = append(rawTables, item)
	}
	manifest := Manifest{Version: 1}
	for _, raw := range rawTables {
		table := Table{Name: raw.name, Class: classifyTable(raw.name), SQLiteOnly: raw.name == "database_authority_write_context", SourceDDL: raw.ddl}
		indexes, _, err := inspectSQLiteIndexes(db, raw.name)
		if err != nil {
			return Manifest{}, err
		}
		table.Indexes = indexes
		columnRows, err := db.Query(`pragma table_info(` + quoteSQLite(raw.name) + `)`)
		if err != nil {
			return Manifest{}, err
		}
		normalizedDDL := strings.ToLower(strings.Join(strings.Fields(raw.ddl), " "))
		for columnRows.Next() {
			var cid, notNull, pk int
			var name, typ string
			var defaultValue sql.NullString
			if err := columnRows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
				columnRows.Close()
				return Manifest{}, err
			}
			kind := sqliteKind(typ)
			var defaultPointer *string
			if defaultValue.Valid {
				value := defaultValue.String
				defaultPointer = &value
			}
			auto := pk == 1 && strings.Contains(normalizedDDL,
				strings.ToLower(name)+" integer primary key autoincrement")
			// INTEGER PRIMARY KEY aliases SQLite's non-null rowid even though
			// PRAGMA table_info historically reports notnull=0.
			nullable := notNull == 0 && !(pk > 0 && kind == KindInteger)
			column := Column{Name: name, Kind: kind, Nullable: nullable,
				Default: defaultPointer, PrimaryKeyPosition: pk, AutoIncrement: auto}
			column.MySQLType = inferMySQLType(column)
			table.Columns = append(table.Columns, column)
		}
		columnRows.Close()
		foreignKeys, err := inspectSQLiteForeignKeys(db, raw.name)
		if err != nil {
			return Manifest{}, err
		}
		table.ForeignKeys = foreignKeys
		manifest.Tables = append(manifest.Tables, table)
	}
	return manifest, nil
}

func inspectSQLiteIndexes(db *sql.DB, table string) ([]Index, map[string]bool, error) {
	rows, err := db.Query(`pragma index_list(` + quoteSQLite(table) + `)`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	indexed := map[string]bool{}
	var result []Index
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return nil, nil, err
		}
		index := Index{Name: name, Unique: unique != 0}
		_ = db.QueryRow(`select coalesce(sql, '') from sqlite_master where type='index' and name=?`, name).Scan(&index.SourceDDL)
		columnRows, err := db.Query(`pragma index_info(` + quoteSQLite(name) + `)`)
		if err != nil {
			return nil, nil, err
		}
		hasExpression := false
		for columnRows.Next() {
			var seqno, cid int
			var column sql.NullString
			if err := columnRows.Scan(&seqno, &cid, &column); err != nil {
				columnRows.Close()
				return nil, nil, err
			}
			if !column.Valid || cid < 0 {
				hasExpression = true
				continue
			}
			index.Columns = append(index.Columns, column.String)
			indexed[column.String] = true
		}
		columnRows.Close()
		if hasExpression {
			index.Columns = nil
		}
		result = append(result, index)
	}
	return result, indexed, rows.Err()
}

func inspectSQLiteForeignKeys(db *sql.DB, table string) ([]ForeignKey, error) {
	rows, err := db.Query(`pragma foreign_key_list(` + quoteSQLite(table) + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ForeignKey
	for rows.Next() {
		var id, seq int
		var refTable, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return nil, err
		}
		result = append(result, ForeignKey{Name: fmt.Sprintf("fk_%s_%d", table, id), Column: from,
			RefTable: refTable, RefColumn: to, OnUpdate: onUpdate, OnDelete: onDelete})
	}
	return result, rows.Err()
}

func mysqlInternalTables() []Table {
	col := func(name string, kind ColumnKind, mysqlType string, nullable bool) Column {
		return Column{Name: name, Kind: kind, Nullable: nullable, MySQLType: mysqlType}
	}
	pk := func(column Column, position int) Column { column.PrimaryKeyPosition = position; return column }
	autoPK := func(name string) Column {
		return Column{Name: name, Kind: KindInteger, MySQLType: "BIGINT", PrimaryKeyPosition: 1, AutoIncrement: true}
	}
	big := func(name string) Column { return col(name, KindInteger, "BIGINT", false) }
	integer := func(name string) Column { return col(name, KindInteger, "INT", false) }
	text := func(name string, size int) Column {
		return col(name, KindText, fmt.Sprintf("VARCHAR(%d)", size), false)
	}
	longText := func(name string, nullable bool) Column { return col(name, KindText, "LONGTEXT", nullable) }
	unique := func(name string, columns ...string) Index { return Index{Name: name, Columns: columns, Unique: true} }
	index := func(name string, columns ...string) Index { return Index{Name: name, Columns: columns} }
	return []Table{
		{Name: "database_routing_state", Class: ClassInternal, MySQLOnly: true, Columns: []Column{
			pk(integer("id"), 1), big("generation"), text("write_primary", 16), text("business_read", 16),
			text("system_read", 16), big("epoch"), big("updated_at_ms"),
		}},
		{Name: "database_migrations", Class: ClassInternal, MySQLOnly: true,
			Indexes: []Index{unique("uq_database_migrations_idempotency", "idempotency_key")}, Columns: []Column{
				pk(text("id", 64), 1), text("idempotency_key", 128), big("generation"), text("source_backend", 16),
				text("target_backend", 16), text("phase", 32), text("status", 16), integer("batch_size"),
				text("frozen_price_hash", 128), big("final_outbox_watermark"), longText("validation_json", true),
				text("validation_token", 128), big("created_at_ms"), big("updated_at_ms"), big("finished_at_ms"), longText("last_error", false),
			}},
		{Name: "database_migration_events", Class: ClassInternal, MySQLOnly: true,
			Indexes: []Index{index("database_migration_events_migration_idx", "migration_id", "event_id")}, Columns: []Column{
				autoPK("event_id"), text("migration_id", 64), text("event_type", 32), text("previous_phase", 32),
				text("previous_status", 16), text("phase", 32), text("status", 16), big("generation"),
				text("table_name", 128), longText("error_text", false), big("created_at_ms"),
			}},
		{Name: "database_migration_tables", Class: ClassInternal, MySQLOnly: true, Columns: []Column{
			pk(text("migration_id", 64), 1), pk(text("table_name", 128), 2), longText("checkpoint_json", true),
			longText("source_watermark_json", true), big("rows_copied"), big("bytes_copied"), big("requests"),
			integer("batch_size"), integer("completed"), big("updated_at_ms"),
		}},
		{Name: "database_outbox", Class: ClassInternal, MySQLOnly: true, Indexes: []Index{
			unique("uq_database_outbox_mutation", "mutation_id"), unique("uq_database_outbox_transaction_sequence", "transaction_id", "sequence_no"),
			index("database_outbox_pending_idx", "applied_at_ms", "outbox_id"), index("database_outbox_group_idx", "transaction_id", "sequence_no"),
		}, Columns: []Column{
			autoPK("outbox_id"), text("mutation_id", 96), text("mutation_digest", 128), text("transaction_id", 96),
			integer("sequence_no"), text("source_backend", 16), text("target_backend", 16), big("source_epoch"),
			text("table_name", 128), text("mutation_operation", 16), longText("primary_key_json", false),
			longText("payload_json", true), integer("schema_version"), big("row_version"), big("payload_bytes"),
			big("created_at_ms"), big("applied_at_ms"),
		}},
		{Name: "database_inbox", Class: ClassInternal, MySQLOnly: true,
			Indexes: []Index{unique("uq_database_inbox_transaction_sequence", "transaction_id", "sequence_no")}, Columns: []Column{
				pk(text("mutation_id", 96), 1), text("mutation_digest", 128), text("transaction_id", 96), integer("sequence_no"),
				text("source_backend", 16), big("source_epoch"), big("outbox_id"), big("applied_at_ms"),
			}},
		{Name: "database_inbox_sources", Class: ClassInternal, MySQLOnly: true, Columns: []Column{
			pk(text("source_backend", 16), 1), big("accepted_epoch"), big("watermark"), big("updated_at_ms"),
		}},
		{Name: "database_row_versions", Class: ClassInternal, MySQLOnly: true, Columns: []Column{
			pk(text("table_name", 128), 1), pk(col("primary_key_hash", KindText, "CHAR(64) CHARACTER SET ascii COLLATE ascii_bin", false), 2),
			longText("primary_key_json", false), big("source_epoch"), big("row_version"), text("mutation_id", 96),
			text("mutation_digest", 128), big("updated_at_ms"),
		}},
		{Name: "database_replication_state", Class: ClassInternal, MySQLOnly: true, Columns: []Column{
			pk(text("direction", 64), 1), text("source_backend", 16), text("target_backend", 16), big("epoch"),
			big("source_watermark"), big("target_watermark"), big("backlog_rows"), big("backlog_bytes"),
			big("oldest_backlog_at_ms"), col("throughput_rows_per_sec", KindReal, "DOUBLE", false), big("retries"),
			big("heartbeat_at_ms"), big("last_progress_at_ms"), big("last_success_at_ms"), longText("last_error", false),
		}},
		{Name: "database_cache_policy", Class: ClassInternal, MySQLOnly: true, Columns: []Column{
			pk(integer("id"), 1), big("generation"), integer("enabled"), integer("retention_days"), integer("batch_size"), big("updated_at_ms"),
		}},
		{Name: "database_cache_coverage", Class: ClassInternal, MySQLOnly: true, Columns: []Column{
			pk(integer("id"), 1), big("earliest_at_ms"), big("latest_at_ms"), big("earliest_id"), big("latest_id"),
			big("watermark"), integer("complete"), big("updated_at_ms"),
		}},
		{Name: "database_maintenance_tasks", Class: ClassInternal, MySQLOnly: true,
			Indexes: []Index{unique("uq_database_maintenance_idempotency", "idempotency_key")}, Columns: []Column{
				pk(text("id", 64), 1), text("idempotency_key", 128), big("generation"), text("kind", 16), text("status", 16),
				integer("retention_days"), text("validation_token", 128), longText("preview_json", true), text("current_table", 128),
				big("processed_rows"), big("created_at_ms"), big("updated_at_ms"), big("finished_at_ms"), longText("last_error", false),
			}},
		{Name: "database_operation_idempotency", Class: ClassInternal, MySQLOnly: true, Columns: []Column{
			pk(text("idempotency_key", 128), 1), text("operation_name", 128), text("request_hash", 128), longText("result_json", false),
			big("result_generation"), big("created_at_ms"), big("updated_at_ms"),
		}},
		{Name: "database_journal_contracts", Class: ClassInternal, MySQLOnly: true, Columns: []Column{
			pk(text("table_name", 128), 1), text("manifest_hash", 128), text("provider_contract", 128), big("installed_at_ms"),
		}},
		{Name: "database_mysql_authority_counter", Class: ClassInternal, MySQLOnly: true, Columns: []Column{
			pk(integer("id"), 1), big("last_row_version"),
		}},
	}
}

func inferMySQLType(column Column) string {
	switch column.Kind {
	case KindInteger:
		if booleanColumn(column.Name) {
			return "TINYINT"
		}
		return "BIGINT"
	case KindReal:
		return "DOUBLE"
	case KindBlob:
		return "LONGBLOB"
	case KindText:
		// SQLite TEXT has no declared character bound. Keep the complete value
		// in LONGTEXT even when it participates in a key or query index; MySQL
		// uses generated SHA-256 identity columns for exact uniqueness and
		// prefix indexes only as a lookup accelerator.
		return "LONGTEXT"
	default:
		return "LONGTEXT"
	}
}

func booleanColumn(name string) bool {
	switch name {
	case "failed", "ready", "disabled", "is_quota", "auto_recover_eligible", "auto_disable_eligible",
		"pre_disabled_state", "lifecycle_applied", "prompt_configured", "completion_configured",
		"cache_configured", "cache_read_configured", "cache_creation_configured", "reset_credits_available":
		return true
	default:
		return false
	}
}

func sqliteKind(value string) ColumnKind {
	value = strings.ToUpper(strings.TrimSpace(value))
	switch {
	case strings.Contains(value, "INT"):
		return KindInteger
	case strings.Contains(value, "REAL"), strings.Contains(value, "FLOA"), strings.Contains(value, "DOUB"):
		return KindReal
	case strings.Contains(value, "BLOB"):
		return KindBlob
	default:
		return KindText
	}
}

var authoritativeTables = map[string]bool{
	"usage_events": true, "dead_letter_events": true, "settings": true, "model_prices": true,
	"model_price_context_tiers": true, "model_price_service_tiers": true, "api_key_aliases": true,
	"account_action_candidates": true, "codex_inspection_runs": true, "codex_inspection_leases": true,
	"codex_inspection_results": true, "codex_inspection_logs": true, "codex_inspection_disable_ownership": true,
	"quota_cooldowns": true, "account_quota_observations": true, "account_quota_windows": true,
	"account_quota_window_activations": true, "account_quota_cycles": true, "account_quota_snapshots": true,
}

func classifyTable(name string) TableClass {
	if authoritativeTables[name] {
		return ClassAuthoritative
	}
	if strings.Contains(name, "rollup") || strings.Contains(name, "aggregate") ||
		strings.HasPrefix(name, "usage_monitoring_") || name == "usage_event_identity_ledger" ||
		name == "usage_codex_legacy_identity_evidence_v1" {
		return ClassDerived
	}
	return ClassInternal
}

func isFTSShadowTable(name string) bool {
	for _, suffix := range []string{"_config", "_data", "_idx", "_docsize", "_content"} {
		if strings.HasSuffix(name, suffix) && strings.HasPrefix(name, "usage_monitoring_event_search_v1") {
			return true
		}
	}
	return false
}

func writeGeneratedManifest(manifest Manifest) error {
	sort.Slice(manifest.Tables, func(i, j int) bool { return manifest.Tables[i].Name < manifest.Tables[j].Name })
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	content := "// Code generated from the canonical fresh SQLite schema; DO NOT EDIT.\n\npackage schema\n\nconst manifestJSON = `" + string(encoded) + "`\n"
	return os.WriteFile("manifest_generated.go", []byte(content), 0o644)
}

func quoteSQLite(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func normalizedDefault(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
