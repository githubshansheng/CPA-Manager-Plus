package schema

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

type actualForeignKey struct {
	columns    []string
	refTable   string
	refColumns []string
	onUpdate   string
	onDelete   string
}

type actualTrigger struct {
	table  string
	timing string
	event  string
}

func validateForeignKeyEnforcement(ctx context.Context, db mysqlExecutor, result *ValidationResult) error {
	actualNative, err := inspectNativeForeignKeys(ctx, db)
	if err != nil {
		return err
	}
	triggers, err := inspectTriggers(ctx, db)
	if err != nil {
		return err
	}
	reported := map[string]bool{}
	expectedTriggers := map[string]bool{}
	report := func(table, problem string) {
		key := table + "\x00" + problem
		if reported[key] {
			return
		}
		reported[key] = true
		result.Differences = append(result.Differences, Difference{Table: table, Problem: problem})
	}
	for _, contract := range foreignKeyContracts(Current()) {
		if contract.Table.SQLiteOnly || contract.RefTable.SQLiteOnly {
			continue
		}
		if !contract.Emulated {
			actual, exists := actualNative[contract.Table.Name+"\x00"+contract.Name]
			if !exists {
				report(contract.Table.Name, "missing foreign key "+contract.Name)
				continue
			}
			if actual.refTable != contract.RefTable.Name || !sameStringColumns(actual.columns, columnNames(contract.Columns)) ||
				!sameStringColumns(actual.refColumns, columnNames(contract.RefColumns)) ||
				!foreignKeyActionsEquivalent(actual.onUpdate, contract.OnUpdate) ||
				!foreignKeyActionsEquivalent(actual.onDelete, contract.OnDelete) {
				report(contract.Table.Name, "foreign key "+contract.Name+" differs from manifest")
			}
			continue
		}

		if ok, err := generatedHashColumnValid(ctx, db, contract.Table.Name, childHashColumn(contract)); err != nil {
			return err
		} else if !ok {
			report(contract.Table.Name, "missing or invalid foreign-key hash column "+childHashColumn(contract))
		}
		if ok, err := generatedHashColumnValid(ctx, db, contract.RefTable.Name, referencedHashColumn(contract)); err != nil {
			return err
		} else if !ok {
			report(contract.RefTable.Name, "missing or invalid referenced-key hash column "+referencedHashColumn(contract))
		}
		if ok, err := hashIndexValid(ctx, db, contract.Table.Name, childHashIndex(contract), childHashColumn(contract), false); err != nil {
			return err
		} else if !ok {
			report(contract.Table.Name, "missing or invalid foreign-key hash index "+childHashIndex(contract))
		}
		if ok, err := hashIndexValid(ctx, db, contract.RefTable.Name, referencedHashIndex(contract), referencedHashColumn(contract), true); err != nil {
			return err
		} else if !ok {
			report(contract.RefTable.Name, "missing or invalid referenced-key hash index "+referencedHashIndex(contract))
		}
		for _, trigger := range emulatedForeignKeyTriggers(contract) {
			expectedTriggers[trigger.Name] = true
			actual, exists := triggers[trigger.Name]
			if !exists || actual.table != trigger.Table || !strings.EqualFold(actual.timing, trigger.Timing) ||
				!strings.EqualFold(actual.event, trigger.Event) {
				report(trigger.Table, "missing foreign-key enforcement trigger "+trigger.Name+" for "+contract.Name)
			}
		}
	}
	for name, trigger := range triggers {
		if !expectedTriggers[name] && !isManagedMySQLJournalTrigger(name, trigger) {
			report(trigger.table, "unexpected trigger "+name)
		}
	}
	return nil
}

func isManagedMySQLJournalTrigger(name string, trigger actualTrigger) bool {
	if !strings.EqualFold(trigger.timing, "AFTER") {
		return false
	}
	event := strings.ToLower(strings.TrimSpace(trigger.event))
	if event != "insert" && event != "update" && event != "delete" {
		return false
	}
	digest := sha256.Sum256([]byte(trigger.table))
	want := "cpamp_mysql_journal_" + hex.EncodeToString(digest[:6]) + "_" + event
	return name == want
}

func inspectNativeForeignKeys(ctx context.Context, db mysqlExecutor) (map[string]actualForeignKey, error) {
	rows, err := db.QueryContext(ctx, `SELECT k.table_name, k.constraint_name, k.column_name,
		k.referenced_table_name, k.referenced_column_name, r.update_rule, r.delete_rule
		FROM information_schema.key_column_usage k
		JOIN information_schema.referential_constraints r
		  ON r.constraint_schema=k.constraint_schema AND r.table_name=k.table_name
		 AND r.constraint_name=k.constraint_name
		WHERE k.constraint_schema=DATABASE() AND k.referenced_table_name IS NOT NULL
		ORDER BY k.table_name, k.constraint_name, k.ordinal_position`)
	if err != nil {
		return nil, fmt.Errorf("inspect mysql foreign keys: %w", err)
	}
	defer rows.Close()
	result := map[string]actualForeignKey{}
	for rows.Next() {
		var table, name, column, refTable, refColumn, onUpdate, onDelete string
		if err := rows.Scan(&table, &name, &column, &refTable, &refColumn, &onUpdate, &onDelete); err != nil {
			return nil, err
		}
		key := table + "\x00" + name
		item := result[key]
		item.columns = append(item.columns, column)
		item.refColumns = append(item.refColumns, refColumn)
		item.refTable, item.onUpdate, item.onDelete = refTable, onUpdate, onDelete
		result[key] = item
	}
	return result, rows.Err()
}

func inspectTriggers(ctx context.Context, db mysqlExecutor) (map[string]actualTrigger, error) {
	rows, err := db.QueryContext(ctx, `SELECT trigger_name, event_object_table, action_timing, event_manipulation
		FROM information_schema.triggers WHERE trigger_schema=DATABASE()`)
	if err != nil {
		return nil, fmt.Errorf("inspect mysql schema triggers: %w", err)
	}
	defer rows.Close()
	result := map[string]actualTrigger{}
	for rows.Next() {
		var name string
		var trigger actualTrigger
		if err := rows.Scan(&name, &trigger.table, &trigger.timing, &trigger.event); err != nil {
			return nil, err
		}
		result[name] = trigger
	}
	return result, rows.Err()
}

func generatedHashColumnValid(ctx context.Context, db mysqlExecutor, table, column string) (bool, error) {
	var dataType, extra, expression string
	var length int64
	err := db.QueryRowContext(ctx, `SELECT data_type, extra, generation_expression, COALESCE(character_maximum_length, 0)
		FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name=?`,
		table, column).Scan(&dataType, &extra, &expression, &length)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect mysql generated hash column %s.%s: %w", table, column, err)
	}
	return strings.EqualFold(dataType, "binary") && length == 32 && strings.Contains(strings.ToUpper(extra), "STORED GENERATED") &&
		strings.TrimSpace(expression) != "", nil
}

func hashIndexValid(ctx context.Context, db mysqlExecutor, table, index, column string, unique bool) (bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT non_unique, column_name FROM information_schema.statistics
		WHERE table_schema=DATABASE() AND table_name=? AND index_name=? ORDER BY seq_in_index`, table, index)
	if err != nil {
		return false, fmt.Errorf("inspect mysql hash index %s.%s: %w", table, index, err)
	}
	defer rows.Close()
	var columns []string
	nonUnique := -1
	for rows.Next() {
		var value int
		var actualColumn string
		if err := rows.Scan(&value, &actualColumn); err != nil {
			return false, err
		}
		if nonUnique < 0 {
			nonUnique = value
		}
		columns = append(columns, actualColumn)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	wantNonUnique := 1
	if unique {
		wantNonUnique = 0
	}
	return nonUnique == wantNonUnique && len(columns) == 1 && columns[0] == column, nil
}

func columnNames(columns []Column) []string {
	result := make([]string, len(columns))
	for i, column := range columns {
		result[i] = column.Name
	}
	return result
}
