package schema

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const dropObjectBatchSize = 100

// Reinitialize destructively replaces every table, view, and trigger in the
// selected MySQL database with the canonical schema. expectedDatabase is
// checked on the reserved connection before any DDL and is used to qualify
// every DROP, preventing a connection/session change from broadening scope.
// MySQL DDL is not transactional: callers must report any error and may safely
// retry the complete operation under the same advisory lock.
func Reinitialize(ctx context.Context, db *sql.DB, expectedDatabase string) error {
	if db == nil {
		return errors.New("mysql database is required")
	}
	expectedDatabase = strings.TrimSpace(expectedDatabase)
	if expectedDatabase == "" {
		return errors.New("expected mysql database is required")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve mysql schema connection: %w", err)
	}
	defer conn.Close()
	return withSchemaLock(ctx, conn, expectedDatabase, func() error {
		features, err := inspectMySQLSchemaFeatures(ctx, conn)
		if err != nil {
			return err
		}
		inventory, err := inspectDropInventory(ctx, conn, expectedDatabase)
		if err != nil {
			return err
		}
		statements := buildDropStatements(expectedDatabase, inventory)
		if err := withForeignKeyChecksDisabled(ctx, conn, func() error {
			for _, statement := range statements {
				if _, err := conn.ExecContext(ctx, statement); err != nil {
					return fmt.Errorf("execute destructive mysql schema DDL: %w", err)
				}
			}
			return ensureLocked(ctx, conn, features)
		}); err != nil {
			return fmt.Errorf("mysql schema reinitialization is incomplete and must be retried: %w", err)
		}
		validation, err := Validate(ctx, conn)
		if err != nil {
			return fmt.Errorf("validate reinitialized mysql schema: %w", err)
		}
		if !validation.Valid {
			return fmt.Errorf("reinitialized mysql schema differs from canonical manifest: %v", validation.Differences)
		}
		return nil
	})
}

type dropInventory struct {
	triggers []string
	views    []string
	tables   []string
}

func inspectDropInventory(ctx context.Context, db mysqlExecutor, databaseName string) (dropInventory, error) {
	var inventory dropInventory
	triggerRows, err := db.QueryContext(ctx, `SELECT trigger_name FROM information_schema.triggers
		WHERE trigger_schema=? ORDER BY trigger_name`, databaseName)
	if err != nil {
		return inventory, fmt.Errorf("inspect mysql schema triggers: %w", err)
	}
	for triggerRows.Next() {
		var name string
		if err := triggerRows.Scan(&name); err != nil {
			triggerRows.Close()
			return inventory, err
		}
		inventory.triggers = append(inventory.triggers, name)
	}
	if err := triggerRows.Err(); err != nil {
		triggerRows.Close()
		return inventory, err
	}
	triggerRows.Close()

	tableRows, err := db.QueryContext(ctx, `SELECT table_name, table_type FROM information_schema.tables
		WHERE table_schema=? ORDER BY table_name`, databaseName)
	if err != nil {
		return inventory, fmt.Errorf("inspect mysql schema objects: %w", err)
	}
	for tableRows.Next() {
		var name, objectType string
		if err := tableRows.Scan(&name, &objectType); err != nil {
			tableRows.Close()
			return inventory, err
		}
		switch strings.ToUpper(strings.TrimSpace(objectType)) {
		case "BASE TABLE":
			inventory.tables = append(inventory.tables, name)
		case "VIEW":
			inventory.views = append(inventory.views, name)
		default:
			tableRows.Close()
			return inventory, fmt.Errorf("unsupported mysql schema object %s of type %s", name, objectType)
		}
	}
	if err := tableRows.Err(); err != nil {
		tableRows.Close()
		return inventory, err
	}
	tableRows.Close()
	return inventory, nil
}

func buildDropStatements(databaseName string, inventory dropInventory) []string {
	triggers := append([]string(nil), inventory.triggers...)
	views := append([]string(nil), inventory.views...)
	tables := append([]string(nil), inventory.tables...)
	sort.Strings(triggers)
	sort.Strings(views)
	sort.Strings(tables)
	statements := make([]string, 0, len(triggers)+(len(views)+len(tables))/dropObjectBatchSize+2)
	for _, name := range triggers {
		statements = append(statements, "DROP TRIGGER IF EXISTS "+qualifiedIdentifier(databaseName, name))
	}
	statements = appendDropBatches(statements, "DROP VIEW IF EXISTS ", databaseName, views)
	statements = appendDropBatches(statements, "DROP TABLE IF EXISTS ", databaseName, tables)
	return statements
}

func appendDropBatches(statements []string, prefix, databaseName string, names []string) []string {
	for start := 0; start < len(names); start += dropObjectBatchSize {
		end := min(start+dropObjectBatchSize, len(names))
		qualified := make([]string, end-start)
		for index, name := range names[start:end] {
			qualified[index] = qualifiedIdentifier(databaseName, name)
		}
		statements = append(statements, prefix+strings.Join(qualified, ", "))
	}
	return statements
}

func qualifiedIdentifier(databaseName, objectName string) string {
	return quote(databaseName) + "." + quote(objectName)
}

func withSchemaLock(ctx context.Context, db mysqlExecutor, expectedDatabase string, action func() error) (resultErr error) {
	var selected sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&selected); err != nil {
		return fmt.Errorf("inspect selected mysql database: %w", err)
	}
	if !selected.Valid || strings.TrimSpace(selected.String) == "" {
		return errors.New("mysql connection has no selected database")
	}
	if expectedDatabase != "" && selected.String != expectedDatabase {
		return fmt.Errorf("selected mysql database %q does not match confirmed database %q",
			selected.String, expectedDatabase)
	}
	digest := sha256.Sum256([]byte(selected.String))
	lockName := schemaLockName + ":" + hex.EncodeToString(digest[:8])
	var locked sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT GET_LOCK(?, 30)`, lockName).Scan(&locked); err != nil {
		return fmt.Errorf("acquire mysql schema lock: %w", err)
	}
	if !locked.Valid || locked.Int64 != 1 {
		return errors.New("timed out acquiring mysql schema lock")
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released sql.NullInt64
		if err := db.QueryRowContext(releaseCtx, `SELECT RELEASE_LOCK(?)`, lockName).Scan(&released); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("release mysql schema lock: %w", err))
		} else if !released.Valid || released.Int64 != 1 {
			resultErr = errors.Join(resultErr, errors.New("mysql schema lock was not owned when released"))
		}
	}()
	return action()
}

func withForeignKeyChecksDisabled(ctx context.Context, db mysqlExecutor, action func() error) (resultErr error) {
	var previous int
	if err := db.QueryRowContext(ctx, `SELECT @@SESSION.FOREIGN_KEY_CHECKS`).Scan(&previous); err != nil {
		return fmt.Errorf("inspect mysql foreign key checks: %w", err)
	}
	if previous != 0 && previous != 1 {
		return fmt.Errorf("unexpected mysql FOREIGN_KEY_CHECKS value %d", previous)
	}
	if _, err := db.ExecContext(ctx, `SET SESSION FOREIGN_KEY_CHECKS=0`); err != nil {
		return fmt.Errorf("disable mysql foreign key checks: %w", err)
	}
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		statement := fmt.Sprintf("SET SESSION FOREIGN_KEY_CHECKS=%d", previous)
		if _, err := db.ExecContext(restoreCtx, statement); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("restore mysql foreign key checks: %w", err))
		}
	}()
	return action()
}
