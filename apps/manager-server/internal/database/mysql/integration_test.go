package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
)

func TestMySQLIntegration(t *testing.T) {
	if os.Getenv("CPAMP_MYSQL_INTEGRATION") != "1" {
		t.Skip("set CPAMP_MYSQL_INTEGRATION=1 to run")
	}
	password := os.Getenv("CPAMP_MYSQL_TEST_PASSWORD")
	host := os.Getenv("CPAMP_MYSQL_TEST_HOST")
	databaseName := os.Getenv("CPAMP_MYSQL_TEST_DATABASE")
	username := os.Getenv("CPAMP_MYSQL_TEST_USERNAME")
	port, portErr := strconv.Atoi(os.Getenv("CPAMP_MYSQL_TEST_PORT"))
	if password == "" || host == "" || databaseName == "" || username == "" || portErr != nil {
		t.Fatal("CPAMP_MYSQL_TEST_HOST/PORT/DATABASE/USERNAME/PASSWORD are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	config := dbmysql.Config{Host: host, Port: port, Database: databaseName, Username: username,
		Password: password, TLSMode: dbmysql.TLSDisabled}
	result, err := dbmysql.Test(ctx, config)
	if err != nil {
		t.Fatalf("test mysql connection: %v", err)
	}
	if err := result.RequireMigrationCapabilities(); err != nil {
		t.Fatalf("mysql migration capability probe: %v", err)
	}
	db, err := dbmysql.Open(ctx, config)
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	defer db.Close()
	if err := schema.Ensure(ctx, db); err != nil {
		t.Fatalf("ensure mysql schema: %v", err)
	}
	validation, err := schema.Validate(ctx, db)
	if err != nil {
		t.Fatalf("validate mysql schema: %v", err)
	}
	if !validation.Valid {
		t.Fatalf("mysql schema differences: %#v", validation.Differences)
	}
	verifyIncompatibleSchemaIsRejected(t, ctx, db)
	verifyLongTextForeignKeys(t, ctx, db)
	t.Logf("MySQL %s connection and schema validation succeeded", result.Version)
}

func verifyIncompatibleSchemaIsRejected(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	const column = "__cpamp_incompatible_schema_probe"
	_, _ = db.ExecContext(ctx, `ALTER TABLE usage_events DROP COLUMN `+column)
	if _, err := db.ExecContext(ctx, `ALTER TABLE usage_events ADD COLUMN `+column+` BIGINT NULL`); err != nil {
		t.Fatalf("install incompatible schema probe: %v", err)
	}
	cleaned := false
	cleanup := func() {
		if cleaned {
			return
		}
		if _, err := db.ExecContext(context.Background(), `ALTER TABLE usage_events DROP COLUMN `+column); err != nil {
			t.Errorf("remove incompatible schema probe: %v", err)
			return
		}
		cleaned = true
	}
	defer cleanup()
	validation, err := schema.Validate(ctx, db)
	if err != nil {
		t.Fatalf("validate incompatible mysql schema: %v", err)
	}
	if validation.Valid {
		t.Fatal("mysql schema with an unexpected column was accepted")
	}
	found := false
	for _, difference := range validation.Differences {
		if difference.Table == "usage_events" && difference.Column == column && difference.Problem == "unexpected column" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("incompatible schema differences did not identify probe column: %#v", validation.Differences)
	}
	cleanup()
	validation, err = schema.Validate(ctx, db)
	if err != nil {
		t.Fatalf("validate restored mysql schema: %v", err)
	}
	if !validation.Valid {
		t.Fatalf("restored mysql schema differences: %#v", validation.Differences)
	}
}

func verifyLongTextForeignKeys(t *testing.T, ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) {
	t.Helper()
	model := fmt.Sprintf("__cpamp_fk_contract_%d_超长模型", time.Now().UnixNano())
	missing := model + "_missing"
	defer func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM model_prices WHERE model=?`, model)
	}()
	if _, err := db.ExecContext(ctx, `INSERT INTO model_price_context_tiers (model, threshold_tokens) VALUES (?, ?)`, missing, 1); err == nil {
		t.Fatal("LONGTEXT child insert without a parent unexpectedly succeeded")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO model_prices
		(model, prompt_per_1m, completion_per_1m, cache_per_1m, updated_at_ms) VALUES (?, 0, 0, 0, ?)`,
		model, time.Now().UnixMilli()); err != nil {
		t.Fatalf("insert LONGTEXT parent: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO model_price_context_tiers (model, threshold_tokens) VALUES (?, ?)`, model, 1); err != nil {
		t.Fatalf("insert context-tier child: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO model_price_service_tiers (model, mode, service_tier) VALUES (?, ?, ?)`, model, "default", "auto"); err != nil {
		t.Fatalf("insert service-tier child: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE model_prices SET model=? WHERE model=?`, model+"_renamed", model); err == nil {
		t.Fatal("LONGTEXT parent key update with children unexpectedly succeeded")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM model_prices WHERE model=?`, model); err != nil {
		t.Fatalf("delete LONGTEXT parent: %v", err)
	}
	for _, table := range []string{"model_price_context_tiers", "model_price_service_tiers"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE model=?`, model).Scan(&count); err != nil {
			t.Fatalf("count cascaded %s rows: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s retained %d rows after parent cascade", table, count)
		}
	}
}
