package databasemanagement

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func TestStartMigrationRejectsFailedTaskUntilItIsResumedOrCanceled(t *testing.T) {
	dataDir := t.TempDir()
	db, err := sqliterepo.Open(filepath.Join(dataDir, "source.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	controlStore, err := control.NewStore(filepath.Join(dataDir, "database-control.json.enc"), protector)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	repository := databasemigration.NewSQLRepository(db, databasemigration.DialectSQLite)
	if err := repository.EnsureSchema(context.Background()); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	migration, _, err := repository.CreateMigration(context.Background(), databasemigration.CreateMigrationRequest{
		IdempotencyKey: "existing-migration", ExpectedGeneration: 1,
		Source: databasemigration.BackendSQLite, Target: databasemigration.BackendMySQL,
		FrozenPriceHash: "prices-v1", FrozenPriceBook: []byte(`{}`),
	})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	migration, err = repository.AdvanceMigration(context.Background(), migration.ID, migration.Generation,
		databasemigration.PhaseCopyHistory)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	migration, err = repository.FailMigration(context.Background(), migration.ID, migration.Generation,
		errors.New("temporary target failure"))
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}

	state := control.DefaultState()
	state.MySQL = dbmysql.Config{Host: "mysql.test", Database: "cpamp", Username: "manager",
		TLSMode: dbmysql.TLSDisabled}
	state.ReplicationEnabled = true
	// The control file is a cached reference and may lag a repository transition
	// after a crash. StartMigration must honor the persisted failed task.
	state.Migration = control.MigrationRef{ID: migration.ID, Phase: string(databasemigration.PhaseCopyHistory),
		Status: string(databasemigration.StatusSucceeded)}
	state, err = controlStore.Save(0, state)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	runtime, err := NewRuntime(RuntimeOptions{Control: controlStore,
		SQLite: database.NewSQLBackend(database.BackendSQLite, db), DataDir: dataDir})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	_, err = runtime.StartMigration(context.Background(), MutationControl{
		ExpectedGeneration: state.Generation,
		IdempotencyKey:     "must-resume-existing-migration",
	})
	if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), "resume or cancel") {
		t.Fatalf("start over failed migration error = %v", err)
	}
}
