package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
)

const schemaLockName = "cpa-manager-plus:mysql-schema:v1"

type mysqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type mysqlSchemaFeatures struct {
	longTextDefaults bool
	collation        string
}

var modernMySQLSchemaFeatures = mysqlSchemaFeatures{
	longTextDefaults: true,
	collation:        database.MySQLBinaryCollation,
}

func inspectMySQLSchemaFeatures(ctx context.Context, db mysqlExecutor) (mysqlSchemaFeatures, error) {
	var version, comment string
	if err := db.QueryRowContext(ctx, `SELECT @@version, @@version_comment`).Scan(&version, &comment); err != nil {
		return mysqlSchemaFeatures{}, fmt.Errorf("inspect mysql schema version: %w", err)
	}
	parsed, err := database.ParseSupportedMySQLVersion(version, comment)
	if err != nil {
		return mysqlSchemaFeatures{}, err
	}
	return mysqlSchemaFeatures{
		longTextDefaults: parsed.SupportsLongTextDefaults(),
		collation:        parsed.StorageCollation(),
	}, nil
}

func Ensure(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("mysql database is required")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve mysql schema connection: %w", err)
	}
	defer conn.Close()
	return withSchemaLock(ctx, conn, "", func() error {
		features, err := inspectMySQLSchemaFeatures(ctx, conn)
		if err != nil {
			return err
		}
		return ensureLocked(ctx, conn, features)
	})
}

func ensureLocked(ctx context.Context, db mysqlExecutor, features mysqlSchemaFeatures) error {
	manifest := Current()
	for _, table := range manifest.Tables {
		if table.SQLiteOnly {
			continue
		}
		if _, err := db.ExecContext(ctx, createTableDDLForFeatures(table, features)); err != nil {
			return fmt.Errorf("create mysql table %s: %w", table.Name, err)
		}
	}
	for _, table := range manifest.Tables {
		if table.SQLiteOnly {
			continue
		}
		if err := ensureIndexes(ctx, db, table); err != nil {
			return err
		}
	}
	for _, table := range manifest.Tables {
		if table.SQLiteOnly {
			continue
		}
		if err := ensureForeignKeys(ctx, db, table); err != nil {
			return err
		}
	}
	return nil
}

func createTableDDL(table Table) string {
	return createTableDDLForFeatures(table, modernMySQLSchemaFeatures)
}

func createTableDDLForFeatures(table Table, features mysqlSchemaFeatures) string {
	definitions := make([]string, 0, len(table.Columns)+4)
	if table.Name == "usage_monitoring_event_search_v1" {
		// SQLite FTS links its implicit rowid to projection.event_id.
		definitions = append(definitions, "`event_id` BIGINT NOT NULL PRIMARY KEY")
	}
	pk := table.PrimaryKey()
	physicalPK := canUseLogicalPrimaryKey(pk)
	for _, column := range table.Columns {
		definition := quote(column.Name) + " " + column.MySQLType
		if !column.Nullable || physicalPK && column.PrimaryKeyPosition > 0 {
			definition += " NOT NULL"
		} else {
			definition += " NULL"
		}
		if column.AutoIncrement && physicalPK {
			definition += " AUTO_INCREMENT"
		}
		if defaultDDL := mysqlDefaultForFeatures(column, features); defaultDDL != "" {
			definition += " DEFAULT " + defaultDDL
		}
		definitions = append(definitions, definition)
	}
	if len(pk) > 0 && physicalPK {
		definitions = append(definitions, "PRIMARY KEY ("+quotedColumnList(pk)+")")
	} else if len(pk) > 0 {
		definitions = append(definitions, "`__cpamp_row_id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY")
		if canUseLogicalUniqueKey(pk) {
			definitions = append(definitions, "UNIQUE KEY `uq_cpamp_logical_pk` ("+quotedColumnList(pk)+")")
		} else {
			direct, hashed := hashedUniqueKeyParts(pk)
			definitions = append(definitions, "`__cpamp_pk_hash` BINARY(32) GENERATED ALWAYS AS ("+hashExpression(hashed)+") STORED")
			definitions = append(definitions, "UNIQUE KEY `uq_cpamp_logical_pk` ("+hashedUniqueIndexColumns(direct, "__cpamp_pk_hash")+")")
		}
	}
	for ordinal, index := range table.Indexes {
		if !index.Unique || len(index.Columns) == 0 {
			continue
		}
		if sameColumnNames(index.Columns, pk) {
			continue
		}
		columns := columnsByName(table, index.Columns)
		if canUseLogicalUniqueKey(columns) {
			continue
		}
		columnName := fmt.Sprintf("__cpamp_uq_%d_hash", ordinal+1)
		direct, hashed := hashedUniqueKeyParts(columns)
		definitions = append(definitions, quote(columnName)+" BINARY(32) GENERATED ALWAYS AS ("+hashExpression(hashed)+") STORED")
		definitions = append(definitions, "UNIQUE KEY "+quote(fmt.Sprintf("uq_cpamp_hash_%d", ordinal+1))+" ("+hashedUniqueIndexColumns(direct, columnName)+")")
	}
	definitions = append(definitions, foreignKeyAuxiliaryDefinitions(Current(), table.Name)...)
	definitions = append(definitions, mysqlSpecificDefinitions(table.Name)...)
	return "CREATE TABLE IF NOT EXISTS " + quote(table.Name) + " (\n  " +
		strings.Join(definitions, ",\n  ") +
		"\n) ENGINE=InnoDB ROW_FORMAT=DYNAMIC DEFAULT CHARACTER SET " + database.MySQLCharacterSet +
		" COLLATE " + features.collation
}

func canUseLogicalPrimaryKey(pk []Column) bool {
	if len(pk) == 0 || !canUseLogicalUniqueKey(pk) {
		return false
	}
	for _, column := range pk {
		if column.Nullable {
			return false
		}
	}
	return true
}

func canUseLogicalUniqueKey(columns []Column) bool {
	return len(columns) > 0 && estimatedIndexBytes(columns) <= 3072
}

func estimatedIndexBytes(columns []Column) int {
	total := 0
	for _, column := range columns {
		upper := strings.ToUpper(column.MySQLType)
		switch {
		case strings.HasPrefix(upper, "VARCHAR("):
			var length int
			_, _ = fmt.Sscanf(upper, "VARCHAR(%d)", &length)
			total += length * 4
		case strings.HasPrefix(upper, "BIGINT"):
			total += 8
		case strings.HasPrefix(upper, "INT"):
			total += 4
		case strings.HasPrefix(upper, "TINYINT"):
			total++
		case strings.HasPrefix(upper, "DOUBLE"):
			total += 8
		case strings.HasPrefix(upper, "BINARY("):
			var length int
			_, _ = fmt.Sscanf(upper, "BINARY(%d)", &length)
			total += length
		case strings.HasPrefix(upper, "CHAR("):
			var length int
			_, _ = fmt.Sscanf(upper, "CHAR(%d)", &length)
			if strings.Contains(upper, "CHARACTER SET ASCII") {
				total += length
			} else {
				total += length * 4
			}
		default:
			return 3073
		}
	}
	return total
}

func quotedColumnList(columns []Column) string {
	values := make([]string, len(columns))
	for i := range columns {
		values[i] = quote(columns[i].Name)
	}
	return strings.Join(values, ", ")
}

func hashExpression(columns []Column) string {
	args := make([]string, len(columns))
	var nullable []string
	for i := range columns {
		args[i] = quote(columns[i].Name)
		if columns[i].Nullable {
			nullable = append(nullable, quote(columns[i].Name)+" IS NULL")
		}
	}
	digest := "UNHEX(SHA2(CAST(JSON_ARRAY(" + strings.Join(args, ", ") + ") AS CHAR CHARACTER SET utf8mb4), 256))"
	if len(nullable) > 0 {
		return "CASE WHEN " + strings.Join(nullable, " OR ") + " THEN NULL ELSE " + digest + " END"
	}
	return digest
}

// hashedUniqueKeyParts keeps fixed-width columns in the physical unique key
// and hashes only unbounded text/blob values. Besides retaining exact scalar
// semantics, this is required for cascading foreign keys: MySQL rejects a
// cascade when the child FK column is itself referenced by a stored generated
// column.
func hashedUniqueKeyParts(columns []Column) (direct, hashed []Column) {
	for _, column := range columns {
		if column.Kind == KindText || column.Kind == KindBlob {
			hashed = append(hashed, column)
			continue
		}
		direct = append(direct, column)
	}
	if len(hashed) == 0 {
		return nil, append([]Column(nil), columns...)
	}
	return direct, hashed
}

func hashedUniqueIndexColumns(direct []Column, hashColumn string) string {
	columns := make([]string, 0, len(direct)+1)
	for _, column := range direct {
		columns = append(columns, quote(column.Name))
	}
	columns = append(columns, quote(hashColumn))
	return strings.Join(columns, ", ")
}

func mysqlDefault(column Column) string {
	return mysqlDefaultForFeatures(column, modernMySQLSchemaFeatures)
}

func mysqlDefaultForFeatures(column Column, features mysqlSchemaFeatures) string {
	raw := normalizedDefault(column.Default)
	if raw == "" {
		return ""
	}
	if column.Kind == KindText && strings.HasPrefix(strings.ToUpper(column.MySQLType), "LONGTEXT") &&
		!features.longTextDefaults {
		return ""
	}
	if strings.EqualFold(raw, "null") {
		return "NULL"
	}
	if column.Kind == KindText && strings.HasPrefix(strings.ToUpper(column.MySQLType), "LONGTEXT") {
		return "(" + raw + ")"
	}
	return raw
}

func ensureIndexes(ctx context.Context, db mysqlExecutor, table Table) error {
	ordinal := 0
	pk := table.PrimaryKey()
	for _, index := range table.Indexes {
		if len(index.Columns) == 0 {
			continue
		}
		if mysqlConditionalUniqueIndex(table.Name, index.Name) {
			// MySQL has no partial indexes. A nullable generated active-key
			// column plus a unique index is installed below instead.
			continue
		}
		if sameColumnNames(index.Columns, pk) {
			continue
		}
		indexColumns := columnsByName(table, index.Columns)
		if index.Unique && !canUseLogicalUniqueKey(indexColumns) {
			// A collision-resistant generated hash unique key was included in
			// CREATE TABLE; the complete source values remain untruncated.
			continue
		}
		ordinal++
		name := index.Name
		if strings.HasPrefix(name, "sqlite_autoindex_") || !validIdentifier(name) {
			name = shortName("idx_"+table.Name, ordinal)
		}
		exists, err := indexExists(ctx, db, table.Name, name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		columns := make([]string, len(index.Columns))
		textColumns := 0
		for _, column := range indexColumns {
			if column.Kind == KindText {
				textColumns++
			}
		}
		prefixLength := 191
		if textColumns > 0 {
			prefixLength = 768 / textColumns
			if prefixLength > 191 {
				prefixLength = 191
			}
			if prefixLength < 16 {
				prefixLength = 16
			}
		}
		for i, columnName := range index.Columns {
			column := findColumn(table, columnName)
			columns[i] = quote(columnName)
			if prefix := mysqlIndexPrefixLength(column, len(index.Columns) > 1, prefixLength); prefix > 0 {
				columns[i] += fmt.Sprintf("(%d)", prefix)
			}
		}
		unique := ""
		if index.Unique {
			unique = "UNIQUE "
		}
		ddl := "CREATE " + unique + "INDEX " + quote(name) + " ON " + quote(table.Name) + " (" + strings.Join(columns, ", ") + ")"
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create mysql index %s: %w", name, err)
		}
	}
	if table.Name == "usage_monitoring_event_search_v1" {
		name := "idx_usage_monitoring_search_fulltext"
		exists, err := indexExists(ctx, db, table.Name, name)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := db.ExecContext(ctx, `CREATE FULLTEXT INDEX `+quote(name)+` ON `+quote(table.Name)+` (`+quote("search_text")+`)`); err != nil {
				return fmt.Errorf("create mysql monitoring fulltext index: %w", err)
			}
		}
	}
	return ensureMySQLSpecificIndexes(ctx, db, table.Name)
}

// mysqlIndexPrefixLength returns a prefix only when MySQL actually needs one.
// A prefix longer than a bounded VARCHAR/CHAR column is rejected by MySQL for
// non-unique indexes (Error 1089), so full bounded columns are left unprefixed.
func mysqlIndexPrefixLength(column Column, composite bool, desired int) int {
	if column.Kind != KindText || desired <= 0 {
		return 0
	}
	upper := strings.ToUpper(strings.TrimSpace(column.MySQLType))
	if strings.HasPrefix(upper, "LONGTEXT") {
		return desired
	}
	if !composite {
		return 0
	}
	for _, format := range []string{"VARCHAR(%d)", "CHAR(%d)"} {
		var limit int
		if count, err := fmt.Sscanf(upper, format, &limit); err == nil && count == 1 && limit > 0 {
			if limit <= desired {
				return 0
			}
			return desired
		}
	}
	return desired
}

type mysqlGeneratedColumnDefinition struct {
	name       string
	columnType string
	expression string
	index      string
}

func mysqlGeneratedColumnDefinitions(table string) []mysqlGeneratedColumnDefinition {
	switch table {
	case "account_action_candidates":
		identity := "JSON_ARRAY(`auth_file_name`, `action_type`, COALESCE(TRIM(`reason_code`), ''), " +
			"COALESCE(TRIM(`auth_index`), ''), " +
			"CASE WHEN COALESCE(TRIM(`auth_index`), '') <> '' THEN '' ELSE COALESCE(TRIM(`account_id_snapshot`), '') END, " +
			"CASE WHEN COALESCE(TRIM(`auth_index`), '') <> '' THEN '' ELSE CASE COALESCE(LOWER(REPLACE(TRIM(`provider`), '_', '-')), '') WHEN 'x-ai' THEN 'xai' WHEN 'grok' THEN 'xai' ELSE COALESCE(LOWER(REPLACE(TRIM(`provider`), '_', '-')), '') END END, " +
			"CASE WHEN COALESCE(TRIM(`auth_index`), '') <> '' OR COALESCE(TRIM(`account_id_snapshot`), '') <> '' THEN '' ELSE COALESCE(TRIM(`account_snapshot`), '') END)"
		return []mysqlGeneratedColumnDefinition{{name: "__cpamp_pending_identity_hash", columnType: "BINARY(32)",
			expression: "CASE WHEN `status` = 'pending' THEN UNHEX(SHA2(CAST(" + identity + " AS CHAR CHARACTER SET utf8mb4), 256)) ELSE NULL END",
			index:      "idx_account_action_candidates_pending_identity_action"}}
	case "quota_cooldowns":
		identity := "JSON_ARRAY(`auth_file_name`, `owner`, COALESCE(TRIM(`auth_index`), ''), " +
			"CASE WHEN COALESCE(TRIM(`auth_index`), '') <> '' THEN '' ELSE CASE COALESCE(LOWER(REPLACE(TRIM(`provider`), '_', '-')), '') WHEN 'x-ai' THEN 'xai' WHEN 'grok' THEN 'xai' ELSE COALESCE(LOWER(REPLACE(TRIM(`provider`), '_', '-')), '') END END, " +
			"CASE WHEN COALESCE(TRIM(`auth_index`), '') <> '' THEN '' ELSE COALESCE(TRIM(`account_snapshot`), '') END)"
		return []mysqlGeneratedColumnDefinition{{name: "__cpamp_active_identity_hash", columnType: "BINARY(32)",
			expression: "CASE WHEN `status` = 'active' THEN UNHEX(SHA2(CAST(" + identity + " AS CHAR CHARACTER SET utf8mb4), 256)) ELSE NULL END",
			index:      "idx_quota_cooldowns_active_identity"}}
	case "account_quota_window_activations":
		return []mysqlGeneratedColumnDefinition{{name: "__cpamp_active_window_id", columnType: "BIGINT",
			expression: "CASE WHEN `deactivated_at_ms` IS NULL THEN `window_id` ELSE NULL END",
			index:      "idx_quota_activations_active"}}
	case "account_quota_cycles":
		return []mysqlGeneratedColumnDefinition{{name: "__cpamp_active_activation_id", columnType: "BIGINT",
			expression: "CASE WHEN `actual_end_ms` IS NULL THEN `activation_id` ELSE NULL END",
			index:      "idx_quota_cycles_active"}}
	default:
		return nil
	}
}

func mysqlSpecificDefinitions(table string) []string {
	var result []string
	for _, definition := range mysqlGeneratedColumnDefinitions(table) {
		result = append(result,
			quote(definition.name)+" "+definition.columnType+" GENERATED ALWAYS AS ("+definition.expression+") STORED",
			"UNIQUE KEY "+quote(definition.index)+" ("+quote(definition.name)+")")
	}
	return result
}

func ensureMySQLSpecificIndexes(ctx context.Context, db mysqlExecutor, table string) error {
	if err := ensureMySQLConditionalUniqueIndexes(ctx, db, table); err != nil {
		return err
	}
	for _, index := range mysqlAdditionalIndexDefinitions(table) {
		exists, err := indexExists(ctx, db, table, index.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		columns := make([]string, len(index.columns))
		for i, column := range index.columns {
			columns[i] = quote(column.name)
			if column.prefix > 0 {
				columns[i] += fmt.Sprintf("(%d)", column.prefix)
			}
		}
		if _, err := db.ExecContext(ctx, "CREATE INDEX "+quote(index.name)+" ON "+quote(table)+" ("+strings.Join(columns, ", ")+")"); err != nil {
			return fmt.Errorf("create mysql-specific index %s: %w", index.name, err)
		}
	}
	return nil
}

type mysqlAdditionalIndexDefinition struct {
	name    string
	columns []physicalIndexColumn
}

func mysqlAdditionalIndexDefinitions(table string) []mysqlAdditionalIndexDefinition {
	column := func(name string, prefix int64) physicalIndexColumn {
		return physicalIndexColumn{name: name, prefix: prefix}
	}
	switch table {
	case "account_quota_snapshots":
		return []mysqlAdditionalIndexDefinition{{name: "idx_quota_snapshots_legacy_migration", columns: []physicalIndexColumn{
			column("account_key", 128), column("provider", 64), column("observation_id", 0),
			column("observed_at_ms", 0), column("id", 0),
		}}}
	case "usage_monitoring_account_daily_rollups_v1":
		return []mysqlAdditionalIndexDefinition{
			{name: "idx_usage_monitoring_account_daily_legacy_window", columns: []physicalIndexColumn{
				column("structure_revision", 96), column("source", 96), column("auth_index", 96), column("bucket_ms", 0),
			}},
			{name: "idx_usage_monitoring_account_daily_credential_window", columns: []physicalIndexColumn{
				column("structure_revision", 96), column("auth_file_snapshot", 96), column("auth_index", 96), column("bucket_ms", 0),
			}},
		}
	default:
		return nil
	}
}

type mysqlConditionalUniqueDefinition struct {
	index      string
	column     string
	columnType string
	expression string
}

func mysqlConditionalUniqueIndex(table, index string) bool {
	for _, definition := range mysqlConditionalUniqueDefinitions(table) {
		if definition.index == index {
			return true
		}
	}
	return false
}

func mysqlConditionalUniqueDefinitions(table string) []mysqlConditionalUniqueDefinition {
	if table != "account_quota_window_activations" && table != "account_quota_cycles" {
		return nil
	}
	generated := mysqlGeneratedColumnDefinitions(table)
	result := make([]mysqlConditionalUniqueDefinition, len(generated))
	for i, definition := range generated {
		result[i] = mysqlConditionalUniqueDefinition{index: definition.index, column: definition.name,
			columnType: definition.columnType, expression: definition.expression}
	}
	return result
}

func ensureMySQLConditionalUniqueIndexes(ctx context.Context, db mysqlExecutor, table string) error {
	for _, definition := range mysqlConditionalUniqueDefinitions(table) {
		var columns int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema=DATABASE() AND table_name=? AND column_name=?`,
			table, definition.column).Scan(&columns); err != nil {
			return err
		}
		if columns == 0 {
			ddl := "ALTER TABLE " + quote(table) + " ADD COLUMN " + quote(definition.column) + " " +
				definition.columnType + " GENERATED ALWAYS AS (" + definition.expression + ") STORED"
			if _, err := db.ExecContext(ctx, ddl); err != nil {
				return fmt.Errorf("create mysql conditional unique column %s.%s: %w", table, definition.column, err)
			}
		}
		var indexedColumns sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index)
			FROM information_schema.statistics WHERE table_schema=DATABASE()
			AND table_name=? AND index_name=?`, table, definition.index).Scan(&indexedColumns); err != nil {
			return err
		}
		if indexedColumns.Valid && indexedColumns.String != definition.column {
			if _, err := db.ExecContext(ctx, "DROP INDEX "+quote(definition.index)+" ON "+quote(table)); err != nil {
				return fmt.Errorf("drop lossy mysql partial-index translation %s: %w", definition.index, err)
			}
			indexedColumns = sql.NullString{}
		}
		if !indexedColumns.Valid || indexedColumns.String == "" {
			if _, err := db.ExecContext(ctx, "CREATE UNIQUE INDEX "+quote(definition.index)+" ON "+
				quote(table)+" ("+quote(definition.column)+")"); err != nil {
				return fmt.Errorf("create mysql conditional unique index %s: %w", definition.index, err)
			}
		}
	}
	return nil
}

func ensureForeignKeys(ctx context.Context, db mysqlExecutor, table Table) error {
	for _, contract := range contractsForTable(Current(), table.Name) {
		if contract.Emulated {
			if err := ensureEmulatedForeignKey(ctx, db, contract); err != nil {
				return fmt.Errorf("create emulated mysql foreign key %s: %w", contract.Name, err)
			}
			continue
		}
		exists, err := constraintExists(ctx, db, table.Name, contract.Name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		columns := make([]string, len(contract.Columns))
		refs := make([]string, len(contract.RefColumns))
		for i := range contract.Columns {
			columns[i] = quote(contract.Columns[i].Name)
			refs[i] = quote(contract.RefColumns[i].Name)
		}
		ddl := "ALTER TABLE " + quote(table.Name) + " ADD CONSTRAINT " + quote(contract.Name) + " FOREIGN KEY (" +
			strings.Join(columns, ", ") + ") REFERENCES " + quote(contract.RefTable.Name) + " (" + strings.Join(refs, ", ") + ")"
		if action := actionDDL(contract.OnDelete); action != "" {
			ddl += " ON DELETE " + action
		}
		if action := actionDDL(contract.OnUpdate); action != "" {
			ddl += " ON UPDATE " + action
		}
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("create mysql foreign key %s: %w", contract.Name, err)
		}
	}
	return nil
}

func isUnindexable(column Column) bool {
	upper := strings.ToUpper(column.MySQLType)
	return strings.HasPrefix(upper, "LONGTEXT") || strings.HasPrefix(upper, "LONGBLOB")
}

func indexExists(ctx context.Context, db mysqlExecutor, table, index string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `select count(*) from information_schema.statistics
		where table_schema=database() and table_name=? and index_name=?`, table, index).Scan(&count)
	return count > 0, err
}

func constraintExists(ctx context.Context, db mysqlExecutor, table, constraint string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `select count(*) from information_schema.table_constraints
		where constraint_schema=database() and table_name=? and constraint_name=?`, table, constraint).Scan(&count)
	return count > 0, err
}

var invalidConstraintChars = regexp.MustCompile(`[^A-Za-z0-9_$]+`)

func shortName(prefix string, ordinal int) string {
	value := invalidConstraintChars.ReplaceAllString(prefix, "_")
	suffix := fmt.Sprintf("_%d", ordinal)
	if len(value)+len(suffix) > 64 {
		value = value[:64-len(suffix)]
	}
	return value + suffix
}

func actionDDL(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	switch value {
	case "CASCADE", "RESTRICT", "SET NULL", "NO ACTION":
		return value
	default:
		return ""
	}
}

func quote(value string) string { return "`" + strings.ReplaceAll(value, "`", "``") + "`" }

func findColumn(table Table, name string) Column {
	for _, column := range table.Columns {
		if column.Name == name {
			return column
		}
	}
	return Column{}
}

func findTable(manifest Manifest, name string) Table {
	for _, table := range manifest.Tables {
		if table.Name == name {
			return table
		}
	}
	return Table{}
}

func columnsByName(table Table, names []string) []Column {
	columns := make([]Column, len(names))
	for i, name := range names {
		columns[i] = findColumn(table, name)
	}
	return columns
}

func sameColumnNames(names []string, columns []Column) bool {
	if len(names) != len(columns) {
		return false
	}
	for i := range names {
		if names[i] != columns[i].Name {
			return false
		}
	}
	return true
}
