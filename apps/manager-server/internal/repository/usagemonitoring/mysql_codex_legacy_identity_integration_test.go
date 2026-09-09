package usagemonitoring

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/dialect"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
)

func TestMySQLCodexLegacyIdentityStateUpgradeIsIdempotent(t *testing.T) {
	if os.Getenv("CPAMP_MYSQL_INTEGRATION") != "1" {
		t.Skip("set CPAMP_MYSQL_INTEGRATION=1 to run the MySQL repository conformance suite")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	port, err := strconv.Atoi(os.Getenv("CPAMP_MYSQL_TEST_PORT"))
	if err != nil {
		t.Fatal("CPAMP_MYSQL_TEST_PORT must be a valid port")
	}
	config := dbmysql.Config{
		Host: os.Getenv("CPAMP_MYSQL_TEST_HOST"), Port: port,
		Database: os.Getenv("CPAMP_MYSQL_TEST_DATABASE"), Username: os.Getenv("CPAMP_MYSQL_TEST_USERNAME"),
		Password: os.Getenv("CPAMP_MYSQL_TEST_PASSWORD"), TLSMode: dbmysql.TLSDisabled,
	}
	if config.Host == "" || config.Database == "" || config.Username == "" || config.Password == "" {
		t.Fatal(fmt.Errorf("CPAMP_MYSQL_TEST_HOST/PORT/DATABASE/USERNAME/PASSWORD are required"))
	}
	db, err := dbmysql.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := schema.Ensure(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo := newForBackend(db, database.BackendMySQL).(*repository)
	rawTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rawTx.Rollback() }()
	if _, err := rawTx.ExecContext(ctx, `delete from usage_monitoring_rollup_state
		where rollup_name=?`, usageevent.CodexLegacyIdentityRollupName); err != nil {
		t.Fatal(err)
	}
	tx := dialect.WrapTx(rawTx, dialect.MySQL())
	for attempt := 0; attempt < 2; attempt++ {
		if err := repo.ensureCodexLegacyIdentityState(ctx, tx); err != nil {
			t.Fatalf("initialize missing MySQL identity state attempt %d: %v", attempt+1, err)
		}
	}
	var rows int
	var revision, status string
	if err := rawTx.QueryRowContext(ctx, `select count(*),max(structure_revision),max(status)
		from usage_monitoring_rollup_state where rollup_name=?`,
		usageevent.CodexLegacyIdentityRollupName).Scan(&rows, &revision, &status); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || revision != "" || status != "pending" {
		t.Fatalf("initialized MySQL identity state rows=%d revision=%q status=%q", rows, revision, status)
	}
}
