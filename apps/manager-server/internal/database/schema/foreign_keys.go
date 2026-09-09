package schema

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// foreignKeyContract is the physical MySQL representation of one logical
// foreign-key group in the SQLite schema manifest. MySQL cannot index an
// unbounded LONGTEXT/LONGBLOB value directly, so those relationships are
// enforced with generated SHA-256 lookup columns plus triggers which compare
// the complete source values after the hash lookup.
type foreignKeyContract struct {
	Name       string
	Table      Table
	Columns    []Column
	RefTable   Table
	RefColumns []Column
	OnUpdate   string
	OnDelete   string
	Emulated   bool
}

type mysqlTriggerDefinition struct {
	Name   string
	Table  string
	Timing string
	Event  string
	DDL    string
}

func foreignKeyContracts(manifest Manifest) []foreignKeyContract {
	var result []foreignKeyContract
	for _, table := range manifest.Tables {
		groups := map[string][]ForeignKey{}
		var names []string
		for _, fk := range table.ForeignKeys {
			if _, exists := groups[fk.Name]; !exists {
				names = append(names, fk.Name)
			}
			groups[fk.Name] = append(groups[fk.Name], fk)
		}
		sort.Strings(names)
		for _, name := range names {
			group := groups[name]
			first := group[0]
			refTable := findTable(manifest, first.RefTable)
			contract := foreignKeyContract{
				Name: name, Table: table, RefTable: refTable,
				OnUpdate: normalizedForeignKeyAction(first.OnUpdate),
				OnDelete: normalizedForeignKeyAction(first.OnDelete),
			}
			// InnoDB does not implement SET DEFAULT. Route that action through
			// the same trigger contract even when all participating columns are
			// otherwise natively indexable.
			contract.Emulated = contract.OnUpdate == "SET DEFAULT" || contract.OnDelete == "SET DEFAULT"
			for _, fk := range group {
				column := findColumn(table, fk.Column)
				refColumn := findColumn(refTable, fk.RefColumn)
				contract.Columns = append(contract.Columns, column)
				contract.RefColumns = append(contract.RefColumns, refColumn)
				contract.Emulated = contract.Emulated || isUnindexable(column) || isUnindexable(refColumn)
			}
			result = append(result, contract)
		}
	}
	return result
}

func contractsForTable(manifest Manifest, table string) []foreignKeyContract {
	var result []foreignKeyContract
	for _, contract := range foreignKeyContracts(manifest) {
		if contract.Table.Name == table {
			result = append(result, contract)
		}
	}
	return result
}

func normalizedForeignKeyAction(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return "NO ACTION"
	}
	return value
}

func foreignKeyActionsEquivalent(left, right string) bool {
	left, right = normalizedForeignKeyAction(left), normalizedForeignKeyAction(right)
	if (left == "NO ACTION" || left == "RESTRICT") && (right == "NO ACTION" || right == "RESTRICT") {
		return true
	}
	return left == right
}

func foreignKeyIdentity(contract foreignKeyContract) string {
	parts := []string{contract.Table.Name, contract.Name, contract.RefTable.Name}
	for i := range contract.Columns {
		parts = append(parts, contract.Columns[i].Name, contract.RefColumns[i].Name)
	}
	return strings.Join(parts, "\x00")
}

func foreignKeyContractIdentity(contract foreignKeyContract) string {
	return foreignKeyIdentity(contract) + "\x00" + contract.OnUpdate + "\x00" + contract.OnDelete
}

func identifierDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])[:20]
}

func childHashColumn(contract foreignKeyContract) string {
	return "__cpamp_fk_" + identifierDigest(foreignKeyIdentity(contract)) + "_hash"
}

func childHashIndex(contract foreignKeyContract) string {
	return "idx_cpamp_fk_" + identifierDigest(foreignKeyIdentity(contract))
}

func referencedHashIdentity(contract foreignKeyContract) string {
	parts := []string{contract.RefTable.Name}
	for _, column := range contract.RefColumns {
		parts = append(parts, column.Name)
	}
	return strings.Join(parts, "\x00")
}

func referencedHashColumn(contract foreignKeyContract) string {
	return "__cpamp_fkref_" + identifierDigest(referencedHashIdentity(contract)) + "_hash"
}

func referencedHashIndex(contract foreignKeyContract) string {
	return "uq_cpamp_fkref_" + identifierDigest(referencedHashIdentity(contract))
}

func foreignKeyAuxiliaryDefinitions(manifest Manifest, tableName string) []string {
	seen := map[string]bool{}
	var definitions []string
	appendDefinition := func(key, definition string) {
		if seen[key] {
			return
		}
		seen[key] = true
		definitions = append(definitions, definition)
	}
	for _, contract := range foreignKeyContracts(manifest) {
		if !contract.Emulated {
			continue
		}
		if contract.Table.Name == tableName {
			column := childHashColumn(contract)
			appendDefinition("column/"+column, quote(column)+" BINARY(32) GENERATED ALWAYS AS ("+
				hashExpression(contract.Columns)+") STORED")
			index := childHashIndex(contract)
			appendDefinition("index/"+index, "KEY "+quote(index)+" ("+quote(column)+")")
		}
		if contract.RefTable.Name == tableName {
			column := referencedHashColumn(contract)
			appendDefinition("column/"+column, quote(column)+" BINARY(32) GENERATED ALWAYS AS ("+
				hashExpression(contract.RefColumns)+") STORED")
			index := referencedHashIndex(contract)
			appendDefinition("index/"+index, "UNIQUE KEY "+quote(index)+" ("+quote(column)+")")
		}
	}
	return definitions
}

func ensureEmulatedForeignKey(ctx context.Context, db mysqlExecutor, contract foreignKeyContract) error {
	if err := ensureGeneratedHashColumn(ctx, db, contract.Table.Name, childHashColumn(contract), hashExpression(contract.Columns)); err != nil {
		return err
	}
	if err := ensureGeneratedHashColumn(ctx, db, contract.RefTable.Name, referencedHashColumn(contract), hashExpression(contract.RefColumns)); err != nil {
		return err
	}
	if err := ensureHashIndex(ctx, db, contract.Table.Name, childHashIndex(contract), childHashColumn(contract), false); err != nil {
		return err
	}
	if err := ensureHashIndex(ctx, db, contract.RefTable.Name, referencedHashIndex(contract), referencedHashColumn(contract), true); err != nil {
		return err
	}
	for _, trigger := range emulatedForeignKeyTriggers(contract) {
		exists, err := triggerExists(ctx, db, trigger.Name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := db.ExecContext(ctx, trigger.DDL); err != nil {
			return fmt.Errorf("create mysql foreign-key trigger %s: %w", trigger.Name, err)
		}
	}
	return nil
}

func ensureGeneratedHashColumn(ctx context.Context, db mysqlExecutor, table, column, expression string) error {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema=DATABASE() AND table_name=? AND column_name=?`, table, column).Scan(&count); err != nil {
		return fmt.Errorf("inspect mysql foreign-key hash column %s.%s: %w", table, column, err)
	}
	if count > 0 {
		return nil
	}
	ddl := "ALTER TABLE " + quote(table) + " ADD COLUMN " + quote(column) +
		" BINARY(32) GENERATED ALWAYS AS (" + expression + ") STORED"
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create mysql foreign-key hash column %s.%s: %w", table, column, err)
	}
	return nil
}

func ensureHashIndex(ctx context.Context, db mysqlExecutor, table, index, column string, unique bool) error {
	exists, err := indexExists(ctx, db, table, index)
	if err != nil {
		return fmt.Errorf("inspect mysql foreign-key hash index %s.%s: %w", table, index, err)
	}
	if exists {
		return nil
	}
	prefix := "CREATE INDEX "
	if unique {
		prefix = "CREATE UNIQUE INDEX "
	}
	if _, err := db.ExecContext(ctx, prefix+quote(index)+" ON "+quote(table)+" ("+quote(column)+")"); err != nil {
		return fmt.Errorf("create mysql foreign-key hash index %s.%s: %w", table, index, err)
	}
	return nil
}

func triggerExists(ctx context.Context, db mysqlExecutor, trigger string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.triggers
		WHERE trigger_schema=DATABASE() AND trigger_name=?`, trigger).Scan(&count)
	return count > 0, err
}

func emulatedForeignKeyTriggers(contract foreignKeyContract) []mysqlTriggerDefinition {
	digest := identifierDigest(foreignKeyContractIdentity(contract))
	trigger := func(suffix, timing, event, table, body string) mysqlTriggerDefinition {
		name := "cpamp_fk_" + digest + "_" + suffix
		return mysqlTriggerDefinition{Name: name, Table: table, Timing: timing, Event: event, DDL: "CREATE TRIGGER " + quote(name) + " " +
			timing + " " + event + " ON " + quote(table) + " FOR EACH ROW BEGIN " + body + " END"}
	}
	message := sqlStringLiteral("foreign key " + contract.Name + " fails")
	nullCondition := qualifiedNullCondition("NEW", contract.Columns)
	parentLookup := parentExistsExpression(contract, "NEW")
	insertBody := "IF NOT (" + nullCondition + ") AND NOT " + parentLookup +
		" THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = " + message + "; END IF;"
	triggers := []mysqlTriggerDefinition{
		trigger("bi", "BEFORE", "INSERT", contract.Table.Name, insertBody),
		trigger("bu", "BEFORE", "UPDATE", contract.Table.Name, insertBody),
	}

	changed := qualifiedColumnsChanged("OLD", "NEW", contract.RefColumns)
	children := childrenExistExpression(contract, "OLD")
	switch contract.OnUpdate {
	case "CASCADE", "SET NULL", "SET DEFAULT":
		statement := childUpdateStatement(contract, contract.OnUpdate)
		body := "IF " + changed + " THEN " + statement + "; END IF;"
		triggers = append(triggers, trigger("pau", "AFTER", "UPDATE", contract.RefTable.Name, body))
	default:
		body := "IF " + changed + " AND " + children +
			" THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = " + message + "; END IF;"
		triggers = append(triggers, trigger("pbu", "BEFORE", "UPDATE", contract.RefTable.Name, body))
	}

	switch contract.OnDelete {
	case "CASCADE":
		body := "DELETE FROM " + quote(contract.Table.Name) + " WHERE " + childMatchPredicate(contract, "OLD") + ";"
		triggers = append(triggers, trigger("pad", "AFTER", "DELETE", contract.RefTable.Name, body))
	case "SET NULL", "SET DEFAULT":
		body := childUpdateStatementForDeletedParent(contract, contract.OnDelete) + ";"
		triggers = append(triggers, trigger("pad", "AFTER", "DELETE", contract.RefTable.Name, body))
	default:
		body := "IF " + children + " THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = " + message + "; END IF;"
		triggers = append(triggers, trigger("pbd", "BEFORE", "DELETE", contract.RefTable.Name, body))
	}
	return triggers
}

func qualifiedNullCondition(qualifier string, columns []Column) string {
	parts := make([]string, len(columns))
	for i, column := range columns {
		parts[i] = qualifier + "." + quote(column.Name) + " IS NULL"
	}
	return strings.Join(parts, " OR ")
}

func qualifiedHashExpression(qualifier string, columns []Column) string {
	parts := make([]string, len(columns))
	for i, column := range columns {
		parts[i] = qualifier + "." + quote(column.Name)
	}
	return "UNHEX(SHA2(CAST(JSON_ARRAY(" + strings.Join(parts, ", ") + ") AS CHAR CHARACTER SET utf8mb4), 256))"
}

func parentExistsExpression(contract foreignKeyContract, childQualifier string) string {
	parts := []string{quote("cpamp_parent") + "." + quote(referencedHashColumn(contract)) + " = " +
		qualifiedHashExpression(childQualifier, contract.Columns)}
	for i := range contract.Columns {
		parts = append(parts, quote("cpamp_parent")+"."+quote(contract.RefColumns[i].Name)+" <=> "+
			childQualifier+"."+quote(contract.Columns[i].Name))
	}
	return "EXISTS (SELECT 1 FROM " + quote(contract.RefTable.Name) + " AS " + quote("cpamp_parent") +
		" WHERE " + strings.Join(parts, " AND ") + " LIMIT 1)"
}

func childrenExistExpression(contract foreignKeyContract, parentQualifier string) string {
	return "EXISTS (SELECT 1 FROM " + quote(contract.Table.Name) + " AS " + quote("cpamp_child") +
		" WHERE " + childMatchPredicateWithAlias(contract, parentQualifier, "cpamp_child") + " LIMIT 1)"
}

func childMatchPredicate(contract foreignKeyContract, parentQualifier string) string {
	return childMatchPredicateWithAlias(contract, parentQualifier, "")
}

func childMatchPredicateWithAlias(contract foreignKeyContract, parentQualifier, childAlias string) string {
	childColumn := func(name string) string { return quote(name) }
	if childAlias != "" {
		childColumn = func(name string) string { return quote(childAlias) + "." + quote(name) }
	}
	parts := []string{childColumn(childHashColumn(contract)) + " = " + qualifiedHashExpression(parentQualifier, contract.RefColumns)}
	for i := range contract.Columns {
		parts = append(parts, childColumn(contract.Columns[i].Name)+" <=> "+
			parentQualifier+"."+quote(contract.RefColumns[i].Name))
	}
	return strings.Join(parts, " AND ")
}

func qualifiedColumnsChanged(oldQualifier, newQualifier string, columns []Column) string {
	parts := make([]string, len(columns))
	for i, column := range columns {
		parts[i] = "NOT (" + oldQualifier + "." + quote(column.Name) + " <=> " +
			newQualifier + "." + quote(column.Name) + ")"
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

func childUpdateStatement(contract foreignKeyContract, action string) string {
	assignments := make([]string, len(contract.Columns))
	for i, column := range contract.Columns {
		switch action {
		case "CASCADE":
			assignments[i] = quote(column.Name) + " = NEW." + quote(contract.RefColumns[i].Name)
		case "SET NULL":
			assignments[i] = quote(column.Name) + " = NULL"
		case "SET DEFAULT":
			assignments[i] = quote(column.Name) + " = DEFAULT"
		}
	}
	return "UPDATE " + quote(contract.Table.Name) + " SET " + strings.Join(assignments, ", ") +
		" WHERE " + childMatchPredicate(contract, "OLD")
}

func childUpdateStatementForDeletedParent(contract foreignKeyContract, action string) string {
	return childUpdateStatement(contract, action)
}

func sqlStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
