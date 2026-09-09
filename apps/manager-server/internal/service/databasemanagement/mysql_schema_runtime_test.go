package databasemanagement

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func TestMySQLSchemaReinitializeMutationRequiresExplicitDropConfirmation(t *testing.T) {
	valid := MySQLSchemaReinitializeMutation{
		MutationControl: MutationControl{ExpectedGeneration: 3, IdempotencyKey: "reinitialize-3"},
		Target:          database.BackendMySQL,
		ConfirmDatabase: "cpamp",
		ConfirmDrop:     true,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid confirmation rejected: %v", err)
	}
	for name, mutate := range map[string]func(*MySQLSchemaReinitializeMutation){
		"target":   func(input *MySQLSchemaReinitializeMutation) { input.Target = database.BackendSQLite },
		"database": func(input *MySQLSchemaReinitializeMutation) { input.ConfirmDatabase = "" },
		"drop":     func(input *MySQLSchemaReinitializeMutation) { input.ConfirmDrop = false },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := candidate.Validate(); !errors.Is(err, ErrUnsafeOperation) {
				t.Fatalf("error=%v, want ErrUnsafeOperation", err)
			}
		})
	}
}

func TestMySQLSchemaReinitializeSafetyGates(t *testing.T) {
	base := control.DefaultState()
	base.MySQL = dbmysql.Config{Host: "db.example.test", Port: 3306, Database: "cpamp",
		Username: "manager", TLSMode: dbmysql.TLSDisabled}
	input := MySQLSchemaReinitializeMutation{MutationControl: MutationControl{
		ExpectedGeneration: 1, IdempotencyKey: "schema-1"}, Target: database.BackendMySQL,
		ConfirmDatabase: "cpamp", ConfirmDrop: true}
	if err := validateMySQLSchemaReinitializeSafety(base, input); err != nil {
		t.Fatalf("safe state rejected: %v", err)
	}

	cases := map[string]func(*control.State, *MySQLSchemaReinitializeMutation){
		"database mismatch": func(_ *control.State, request *MySQLSchemaReinitializeMutation) {
			request.ConfirmDatabase = "another_database"
		},
		"replication": func(state *control.State, _ *MySQLSchemaReinitializeMutation) {
			state.ReplicationEnabled = true
		},
		"migration": func(state *control.State, _ *MySQLSchemaReinitializeMutation) {
			state.Migration = control.MigrationRef{ID: "migration-1", Status: string(databasemigration.StatusRunning)}
		},
		"failover": func(state *control.State, _ *MySQLSchemaReinitializeMutation) {
			state.Failover.Status = "switching"
		},
		"write primary": func(state *control.State, _ *MySQLSchemaReinitializeMutation) {
			state.WritePrimary = database.BackendMySQL
		},
		"business read primary": func(state *control.State, _ *MySQLSchemaReinitializeMutation) {
			state.BusinessReadPrimary = database.BackendMySQL
		},
		"system read primary": func(state *control.State, _ *MySQLSchemaReinitializeMutation) {
			state.SystemReadPrimary = database.BackendMySQL
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			state, request := base, input
			mutate(&state, &request)
			if err := validateMySQLSchemaReinitializeSafety(state, request); !errors.Is(err, ErrUnsafeOperation) {
				t.Fatalf("error=%v, want ErrUnsafeOperation", err)
			}
		})
	}

	for _, status := range []databasemigration.RunStatus{
		databasemigration.StatusCanceled, databasemigration.StatusFailed, databasemigration.StatusSucceeded,
	} {
		state := base
		state.Migration = control.MigrationRef{ID: "migration-terminal", Status: string(status)}
		if err := validateMySQLSchemaReinitializeSafety(state, input); err != nil {
			t.Errorf("terminal migration status %s rejected: %v", status, err)
		}
	}
}

func TestMySQLSchemaReinitializeChecksGenerationAndSafetyBeforeConnecting(t *testing.T) {
	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	controlStore, err := control.NewStore(filepath.Join(t.TempDir(), "database-control.json.enc"), protector)
	if err != nil {
		t.Fatal(err)
	}
	state := control.DefaultState()
	state.MySQL = dbmysql.Config{Host: "unreachable.invalid", Port: 3306, Database: "cpamp",
		Username: "manager", TLSMode: dbmysql.TLSDisabled}
	state.ReplicationEnabled = true
	saved, err := controlStore.Save(0, state)
	if err != nil {
		t.Fatal(err)
	}
	sqliteDB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "runtime.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(RuntimeOptions{Control: controlStore,
		SQLite: database.NewSQLBackend(database.BackendSQLite, sqliteDB)})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	request := MySQLSchemaReinitializeMutation{MutationControl: MutationControl{
		ExpectedGeneration: saved.Generation + 1, IdempotencyKey: "stale-generation"},
		Target: database.BackendMySQL, ConfirmDatabase: "cpamp", ConfirmDrop: true}
	if _, err := runtime.ReinitializeMySQLSchema(context.Background(), request); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("stale generation error=%v", err)
	}
	request.ExpectedGeneration = saved.Generation
	request.IdempotencyKey = "replication-gate"
	if _, err := runtime.ReinitializeMySQLSchema(context.Background(), request); !errors.Is(err, ErrUnsafeOperation) {
		t.Fatalf("replication gate error=%v", err)
	}
}
