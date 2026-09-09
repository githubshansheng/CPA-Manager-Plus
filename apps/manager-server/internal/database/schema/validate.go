package schema

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
)

type Difference struct {
	Table   string `json:"table"`
	Column  string `json:"column,omitempty"`
	Problem string `json:"problem"`
}

type ValidationResult struct {
	Valid       bool         `json:"valid"`
	Differences []Difference `json:"differences,omitempty"`
}

type physicalColumnContract struct {
	columnType     string
	nullable       bool
	characterSet   string
	collation      string
	defaultValue   *string
	autoIncrement  bool
	generated      bool
	generationExpr string
}

type physicalIndexColumn struct {
	name   string
	prefix int64
}

type physicalIndexContract struct {
	unique    bool
	indexType string
	columns   []physicalIndexColumn
}

type physicalTableContract struct {
	columns   map[string]physicalColumnContract
	indexes   map[string]physicalIndexContract
	engine    string
	collation string
}

type actualMySQLColumn struct {
	dataType       string
	columnType     string
	nullable       bool
	characterSet   string
	collation      string
	defaultValue   sql.NullString
	extra          string
	generationExpr string
}

type actualMySQLTable struct {
	columns   map[string]actualMySQLColumn
	indexes   map[string]physicalIndexContract
	engine    string
	collation string
}

func Validate(ctx context.Context, db mysqlExecutor) (ValidationResult, error) {
	features, err := inspectMySQLSchemaFeatures(ctx, db)
	if err != nil {
		return ValidationResult{}, err
	}
	actual, err := inspectMySQLSchema(ctx, db)
	if err != nil {
		return ValidationResult{}, err
	}
	result := validateMySQLPhysicalSchemaForFeatures(Current(), actual, features)
	if err := validateForeignKeyEnforcement(ctx, db, &result); err != nil {
		return ValidationResult{}, err
	}
	result.Valid = len(result.Differences) == 0
	return result, nil
}

func inspectMySQLSchema(ctx context.Context, db mysqlExecutor) (map[string]actualMySQLTable, error) {
	tables := map[string]actualMySQLTable{}
	rows, err := db.QueryContext(ctx, `SELECT table_name, COALESCE(engine, ''), COALESCE(table_collation, '')
		FROM information_schema.tables WHERE table_schema=DATABASE()`)
	if err != nil {
		return nil, fmt.Errorf("inspect mysql schema tables: %w", err)
	}
	for rows.Next() {
		var name, engine, collation string
		if err := rows.Scan(&name, &engine, &collation); err != nil {
			rows.Close()
			return nil, err
		}
		tables[name] = actualMySQLTable{columns: map[string]actualMySQLColumn{}, indexes: map[string]physicalIndexContract{},
			engine: strings.ToLower(engine), collation: strings.ToLower(collation)}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = db.QueryContext(ctx, `SELECT table_name, column_name, data_type, column_type, is_nullable,
		COALESCE(character_set_name, ''), COALESCE(collation_name, ''), column_default, extra,
		COALESCE(generation_expression, '')
		FROM information_schema.columns WHERE table_schema=DATABASE()
		ORDER BY table_name, ordinal_position`)
	if err != nil {
		return nil, fmt.Errorf("inspect mysql schema columns: %w", err)
	}
	for rows.Next() {
		var table, name, nullable string
		var column actualMySQLColumn
		if err := rows.Scan(&table, &name, &column.dataType, &column.columnType, &nullable,
			&column.characterSet, &column.collation, &column.defaultValue, &column.extra, &column.generationExpr); err != nil {
			rows.Close()
			return nil, err
		}
		column.dataType = strings.ToLower(column.dataType)
		column.columnType = normalizeMySQLColumnType(column.columnType)
		column.nullable = nullable == "YES"
		column.characterSet = strings.ToLower(column.characterSet)
		column.collation = strings.ToLower(column.collation)
		item, exists := tables[table]
		if !exists {
			item = actualMySQLTable{columns: map[string]actualMySQLColumn{}, indexes: map[string]physicalIndexContract{}}
		}
		item.columns[name] = column
		tables[table] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = db.QueryContext(ctx, `SELECT table_name, index_name, non_unique, index_type, seq_in_index,
		column_name, sub_part FROM information_schema.statistics WHERE table_schema=DATABASE()
		ORDER BY table_name, index_name, seq_in_index`)
	if err != nil {
		return nil, fmt.Errorf("inspect mysql schema indexes: %w", err)
	}
	for rows.Next() {
		var table, name, indexType string
		var nonUnique, sequence int
		var column sql.NullString
		var prefix sql.NullInt64
		if err := rows.Scan(&table, &name, &nonUnique, &indexType, &sequence, &column, &prefix); err != nil {
			rows.Close()
			return nil, err
		}
		item := tables[table]
		index := item.indexes[name]
		index.unique = nonUnique == 0
		index.indexType = strings.ToUpper(indexType)
		if column.Valid {
			index.columns = append(index.columns, physicalIndexColumn{name: column.String, prefix: prefix.Int64})
		}
		item.indexes[name] = index
		tables[table] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return tables, nil
}

func validateMySQLPhysicalSchema(manifest Manifest, actual map[string]actualMySQLTable) ValidationResult {
	return validateMySQLPhysicalSchemaForFeatures(manifest, actual, modernMySQLSchemaFeatures)
}

func validateMySQLPhysicalSchemaForFeatures(manifest Manifest, actual map[string]actualMySQLTable,
	features mysqlSchemaFeatures) ValidationResult {
	wanted := mysqlPhysicalContractForFeatures(manifest, features)
	result := ValidationResult{Valid: true}
	report := func(table, column, problem string) {
		result.Differences = append(result.Differences, Difference{Table: table, Column: column, Problem: problem})
	}
	for _, tableName := range sortedTableNames(wanted) {
		want := wanted[tableName]
		found, exists := actual[tableName]
		if !exists {
			report(tableName, "", "missing table")
			continue
		}
		if !strings.EqualFold(found.engine, want.engine) {
			report(tableName, "", fmt.Sprintf("storage engine %s, want %s", found.engine, want.engine))
		}
		if !strings.EqualFold(found.collation, want.collation) {
			report(tableName, "", fmt.Sprintf("table collation %s, want %s", found.collation, want.collation))
		}
		for _, columnName := range sortedColumnNames(want.columns) {
			wantColumn := want.columns[columnName]
			foundColumn, exists := found.columns[columnName]
			if !exists {
				report(tableName, columnName, "missing column")
				continue
			}
			validateMySQLColumn(tableName, columnName, wantColumn, foundColumn, report)
		}
		for _, columnName := range sortedActualColumnNames(found.columns) {
			if _, exists := want.columns[columnName]; !exists {
				report(tableName, columnName, "unexpected column")
			}
		}
		for _, indexName := range sortedIndexNames(want.indexes) {
			wantIndex := want.indexes[indexName]
			foundIndex, exists := found.indexes[indexName]
			if !exists {
				report(tableName, "", "missing index "+indexName)
				continue
			}
			if foundIndex.unique != wantIndex.unique || !strings.EqualFold(foundIndex.indexType, wantIndex.indexType) ||
				!samePhysicalIndexColumns(foundIndex.columns, wantIndex.columns) {
				report(tableName, "", "index "+indexName+" differs from manifest mapping")
			}
		}
	}
	for _, tableName := range sortedActualTableNames(actual) {
		if _, exists := wanted[tableName]; !exists {
			report(tableName, "", "unexpected table")
		}
	}
	result.Valid = len(result.Differences) == 0
	return result
}

func validateMySQLColumn(table, name string, want physicalColumnContract, found actualMySQLColumn,
	report func(string, string, string)) {
	wantDataType := mysqlDataType(want.columnType)
	if found.dataType != wantDataType || found.columnType != want.columnType {
		report(table, name, fmt.Sprintf("type %s, want %s", found.columnType, want.columnType))
	}
	if found.nullable != want.nullable {
		report(table, name, "NULL semantics differ")
	}
	if found.characterSet != want.characterSet || found.collation != want.collation {
		report(table, name, fmt.Sprintf("character set/collation %s/%s, want %s/%s",
			found.characterSet, found.collation, want.characterSet, want.collation))
	}
	if !mysqlDefaultsEquivalent(want.defaultValue, found.defaultValue) {
		report(table, name, fmt.Sprintf("default %s, want %s", printableDefault(found.defaultValue), printableWantedDefault(want.defaultValue)))
	}
	foundAutoIncrement := strings.Contains(strings.ToLower(found.extra), "auto_increment")
	if foundAutoIncrement != want.autoIncrement {
		report(table, name, fmt.Sprintf("AUTO_INCREMENT=%t, want %t", foundAutoIncrement, want.autoIncrement))
	}
	foundGenerated := mysqlColumnIsGenerated(found.extra)
	if foundGenerated != want.generated {
		report(table, name, fmt.Sprintf("generated=%t, want %t", foundGenerated, want.generated))
	} else if want.generated && normalizeGenerationExpression(found.generationExpr) != normalizeGenerationExpression(want.generationExpr) {
		report(table, name, "generation expression differs from manifest mapping")
	}
}

func mysqlPhysicalContract(manifest Manifest) map[string]physicalTableContract {
	return mysqlPhysicalContractForFeatures(manifest, modernMySQLSchemaFeatures)
}

func mysqlPhysicalContractForFeatures(manifest Manifest, features mysqlSchemaFeatures) map[string]physicalTableContract {
	result := map[string]physicalTableContract{}
	for _, table := range manifest.Tables {
		if table.SQLiteOnly {
			continue
		}
		contract := physicalTableContract{columns: map[string]physicalColumnContract{}, indexes: map[string]physicalIndexContract{},
			engine: "innodb", collation: strings.ToLower(features.collation)}
		pk := table.PrimaryKey()
		physicalPK := canUseLogicalPrimaryKey(pk)
		if table.Name == "usage_monitoring_event_search_v1" {
			contract.columns["event_id"] = ordinaryPhysicalColumn("BIGINT", false, nil, false)
			contract.indexes["PRIMARY"] = physicalIndexContract{unique: true, indexType: "BTREE",
				columns: []physicalIndexColumn{{name: "event_id"}}}
		}
		for _, column := range table.Columns {
			contract.columns[column.Name] = ordinaryPhysicalColumnForCollation(column.MySQLType,
				column.Nullable && !(physicalPK && column.PrimaryKeyPosition > 0),
				mysqlManifestDefaultForFeatures(column, features),
				column.AutoIncrement && physicalPK, features.collation)
		}
		if len(pk) > 0 && physicalPK {
			contract.indexes["PRIMARY"] = physicalIndexContract{unique: true, indexType: "BTREE", columns: indexColumns(pk, 0)}
		} else if len(pk) > 0 {
			contract.columns["__cpamp_row_id"] = ordinaryPhysicalColumn("BIGINT UNSIGNED", false, nil, true)
			contract.indexes["PRIMARY"] = physicalIndexContract{unique: true, indexType: "BTREE",
				columns: []physicalIndexColumn{{name: "__cpamp_row_id"}}}
			if canUseLogicalUniqueKey(pk) {
				contract.indexes["uq_cpamp_logical_pk"] = physicalIndexContract{unique: true, indexType: "BTREE", columns: indexColumns(pk, 0)}
			} else {
				direct, hashed := hashedUniqueKeyParts(pk)
				contract.columns["__cpamp_pk_hash"] = generatedPhysicalColumn("BINARY(32)", true, hashExpression(hashed))
				contract.indexes["uq_cpamp_logical_pk"] = physicalIndexContract{unique: true, indexType: "BTREE",
					columns: hashedPhysicalIndexColumns(direct, "__cpamp_pk_hash")}
			}
		}
		addManifestIndexContracts(&contract, table, pk)
		if table.Name == "usage_monitoring_event_search_v1" {
			contract.indexes["idx_usage_monitoring_search_fulltext"] = physicalIndexContract{indexType: "FULLTEXT",
				columns: []physicalIndexColumn{{name: "search_text"}}}
		}
		for _, definition := range mysqlGeneratedColumnDefinitions(table.Name) {
			contract.columns[definition.name] = generatedPhysicalColumn(definition.columnType, true, definition.expression)
			contract.indexes[definition.index] = physicalIndexContract{unique: true, indexType: "BTREE",
				columns: []physicalIndexColumn{{name: definition.name}}}
		}
		for _, definition := range mysqlAdditionalIndexDefinitions(table.Name) {
			contract.indexes[definition.name] = physicalIndexContract{indexType: "BTREE", columns: definition.columns}
		}
		result[table.Name] = contract
	}
	for _, fk := range foreignKeyContracts(manifest) {
		if !fk.Emulated || fk.Table.SQLiteOnly || fk.RefTable.SQLiteOnly {
			continue
		}
		child := result[fk.Table.Name]
		childColumn := childHashColumn(fk)
		child.columns[childColumn] = generatedPhysicalColumn("BINARY(32)", true, hashExpression(fk.Columns))
		child.indexes[childHashIndex(fk)] = physicalIndexContract{indexType: "BTREE", columns: []physicalIndexColumn{{name: childColumn}}}
		result[fk.Table.Name] = child
		parent := result[fk.RefTable.Name]
		parentColumn := referencedHashColumn(fk)
		parent.columns[parentColumn] = generatedPhysicalColumn("BINARY(32)", true, hashExpression(fk.RefColumns))
		parent.indexes[referencedHashIndex(fk)] = physicalIndexContract{unique: true, indexType: "BTREE",
			columns: []physicalIndexColumn{{name: parentColumn}}}
		result[fk.RefTable.Name] = parent
	}
	return result
}

func addManifestIndexContracts(contract *physicalTableContract, table Table, pk []Column) {
	eligibleOrdinal := 0
	for manifestOrdinal, index := range table.Indexes {
		if len(index.Columns) == 0 || mysqlConditionalUniqueIndex(table.Name, index.Name) || sameColumnNames(index.Columns, pk) {
			continue
		}
		columns := columnsByName(table, index.Columns)
		if index.Unique && !canUseLogicalUniqueKey(columns) {
			name := fmt.Sprintf("__cpamp_uq_%d_hash", manifestOrdinal+1)
			direct, hashed := hashedUniqueKeyParts(columns)
			contract.columns[name] = generatedPhysicalColumn("BINARY(32)", true, hashExpression(hashed))
			contract.indexes[fmt.Sprintf("uq_cpamp_hash_%d", manifestOrdinal+1)] = physicalIndexContract{unique: true, indexType: "BTREE",
				columns: hashedPhysicalIndexColumns(direct, name)}
			continue
		}
		eligibleOrdinal++
		name := index.Name
		if strings.HasPrefix(name, "sqlite_autoindex_") || !validIdentifier(name) {
			name = shortName("idx_"+table.Name, eligibleOrdinal)
		}
		contract.indexes[name] = physicalIndexContract{unique: index.Unique, indexType: "BTREE", columns: mysqlManifestIndexColumns(columns)}
	}
}

func hashedPhysicalIndexColumns(direct []Column, hashColumn string) []physicalIndexColumn {
	columns := make([]physicalIndexColumn, 0, len(direct)+1)
	for _, column := range direct {
		columns = append(columns, physicalIndexColumn{name: column.Name})
	}
	return append(columns, physicalIndexColumn{name: hashColumn})
}

func mysqlManifestIndexColumns(columns []Column) []physicalIndexColumn {
	textColumns := 0
	for _, column := range columns {
		if column.Kind == KindText {
			textColumns++
		}
	}
	prefixLength := int64(191)
	if textColumns > 0 {
		prefixLength = int64(768 / textColumns)
		if prefixLength > 191 {
			prefixLength = 191
		}
		if prefixLength < 16 {
			prefixLength = 16
		}
	}
	result := make([]physicalIndexColumn, len(columns))
	for i, column := range columns {
		result[i].name = column.Name
		result[i].prefix = int64(mysqlIndexPrefixLength(column, len(columns) > 1, int(prefixLength)))
	}
	return result
}

func ordinaryPhysicalColumn(mysqlType string, nullable bool, defaultValue *string, autoIncrement bool) physicalColumnContract {
	return ordinaryPhysicalColumnForCollation(mysqlType, nullable, defaultValue, autoIncrement,
		database.MySQLBinaryCollation)
}

func ordinaryPhysicalColumnForCollation(mysqlType string, nullable bool, defaultValue *string,
	autoIncrement bool, textCollation string) physicalColumnContract {
	characterSet, collation := "", ""
	upper := strings.ToUpper(mysqlType)
	base := normalizeMySQLColumnType(mysqlType)
	if strings.HasPrefix(base, "char(") || strings.HasPrefix(base, "varchar(") || base == "longtext" {
		characterSet, collation = strings.ToLower(database.MySQLCharacterSet), strings.ToLower(textCollation)
		if strings.Contains(upper, "CHARACTER SET ASCII") {
			characterSet, collation = "ascii", "ascii_bin"
		}
	}
	return physicalColumnContract{columnType: base, nullable: nullable, characterSet: characterSet, collation: collation,
		defaultValue: defaultValue, autoIncrement: autoIncrement}
}

func generatedPhysicalColumn(mysqlType string, nullable bool, expression string) physicalColumnContract {
	column := ordinaryPhysicalColumn(mysqlType, nullable, nil, false)
	column.generated = true
	column.generationExpr = expression
	return column
}

func mysqlManifestDefault(column Column) *string {
	return mysqlManifestDefaultForFeatures(column, modernMySQLSchemaFeatures)
}

func mysqlManifestDefaultForFeatures(column Column, features mysqlSchemaFeatures) *string {
	value := mysqlDefaultForFeatures(column, features)
	if value == "" || strings.EqualFold(strings.TrimSpace(value), "NULL") {
		return nil
	}
	return &value
}

var mysqlTypeAttributes = regexp.MustCompile(`(?i)\s+(?:character\s+set|collate)\s+[a-z0-9_]+`)
var mysqlIntegerDisplayWidth = regexp.MustCompile(`(?i)\b(tinyint|smallint|mediumint|int|integer|bigint)\(\d+\)`)

func normalizeMySQLColumnType(value string) string {
	value = mysqlTypeAttributes.ReplaceAllString(strings.TrimSpace(value), "")
	// MySQL 8.0.12 still reports deprecated integer display widths in
	// INFORMATION_SCHEMA even when the DDL omits them. They do not affect the
	// numeric range or storage contract and disappeared from later 8.0 releases.
	value = mysqlIntegerDisplayWidth.ReplaceAllString(value, "$1")
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func mysqlDataType(columnType string) string {
	if index := strings.IndexAny(columnType, "( "); index >= 0 {
		return columnType[:index]
	}
	return columnType
}

var expressionCharsetLiteral = regexp.MustCompile(`(?i)_(?:utf8mb4|utf8|ascii)'`)
var expressionIsNullFunction = regexp.MustCompile("(?i)isnull\\(\\s*(`?[a-z_][a-z0-9_]*`?)\\s*\\)")

func normalizeGenerationExpression(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	// INFORMATION_SCHEMA escapes quotes in generated/default expressions even
	// though SHOW CREATE TABLE renders the same literals without backslashes.
	value = strings.ReplaceAll(value, `\'`, `'`)
	value = expressionCharsetLiteral.ReplaceAllString(value, "'")
	// MySQL 8.0.12 renders `column IS NULL` as ISNULL(column) in generated
	// expressions. Later releases retain the original operator form.
	value = expressionIsNullFunction.ReplaceAllString(value, "$1 is null")
	value = strings.ReplaceAll(value, "`", "")
	value = strings.ReplaceAll(value, "character set", "charset")
	value = strings.Join(strings.Fields(value), "")
	// MySQL's data dictionary inserts grouping parentheses while canonicalizing
	// generated expressions (notably around CASE predicates). The generated
	// contracts in this package contain no parenthesis-bearing string literals,
	// so comparing the full ordered token stream without grouping punctuation
	// remains strict while avoiding version-specific formatting differences.
	value = strings.NewReplacer("(", "", ")", "").Replace(value)
	return value
}

func hasBalancedOuterParentheses(value string) bool {
	if len(value) < 2 || value[0] != '(' || value[len(value)-1] != ')' {
		return false
	}
	depth := 0
	quoted := false
	for i, r := range value {
		if r == '\'' {
			quoted = !quoted
		}
		if quoted {
			continue
		}
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i != len(value)-1 {
				return false
			}
		}
	}
	return depth == 0
}

func mysqlDefaultsEquivalent(want *string, actual sql.NullString) bool {
	if want == nil {
		return !actual.Valid
	}
	return actual.Valid && normalizeMySQLDefault(*want) == normalizeMySQLDefault(actual.String)
}

func normalizeMySQLDefault(value string) string {
	value = strings.TrimSpace(value)
	for hasBalancedOuterParentheses(value) {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	value = strings.ReplaceAll(value, `\'`, `'`)
	value = expressionCharsetLiteral.ReplaceAllString(value, "'")
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		value = strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	if number, err := strconv.ParseFloat(value, 64); err == nil {
		return strconv.FormatFloat(number, 'g', -1, 64)
	}
	return value
}

func mysqlColumnIsGenerated(extra string) bool {
	extra = strings.ToLower(extra)
	return strings.Contains(extra, "stored generated") || strings.Contains(extra, "virtual generated")
}

func printableDefault(value sql.NullString) string {
	if !value.Valid {
		return "NULL/no default"
	}
	return strconv.Quote(value.String)
}

func printableWantedDefault(value *string) string {
	if value == nil {
		return "NULL/no default"
	}
	return strconv.Quote(*value)
}

func indexColumns(columns []Column, prefix int64) []physicalIndexColumn {
	result := make([]physicalIndexColumn, len(columns))
	for i, column := range columns {
		result[i] = physicalIndexColumn{name: column.Name, prefix: prefix}
	}
	return result
}

func samePhysicalIndexColumns(left, right []physicalIndexColumn) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func sortedTableNames(values map[string]physicalTableContract) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func sortedActualTableNames(values map[string]actualMySQLTable) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func sortedColumnNames(values map[string]physicalColumnContract) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func sortedActualColumnNames(values map[string]actualMySQLColumn) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func sortedIndexNames(values map[string]physicalIndexContract) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
