package databasemanagement

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func TestAcquireSQLiteSourceSwitchRequiresCurrentStableSQLiteOnlyGeneration(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "usage.sqlite")
	db, err := sqliterepo.Open(dbPath)
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
	state, err := controlStore.Save(0, control.DefaultState())
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	runtime, err := NewRuntime(RuntimeOptions{
		Control:    controlStore,
		SQLite:     database.NewSQLBackend(database.BackendSQLite, db),
		SQLitePath: dbPath,
		DataDir:    dataDir,
	})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	release, err := runtime.AcquireSQLiteSourceSwitch(context.Background(), state.Generation)
	if err != nil || release == nil {
		t.Fatalf("safe guard: release=%v err=%v", release != nil, err)
	}
	release()
	release()

	state, err = controlStore.Update(state.Generation, func(current *control.State) error {
		current.Migration = control.MigrationRef{ID: "migration-in-progress", Status: "running"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if release, err := runtime.AcquireSQLiteSourceSwitch(context.Background(), state.Generation); release != nil || !errors.Is(err, ErrSQLiteSourceUnsafe) {
		t.Fatalf("unsafe guard: release=%v err=%v", release != nil, err)
	}
	if release, err := runtime.AcquireSQLiteSourceSwitch(context.Background(), state.Generation-1); release != nil || !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("stale guard: release=%v err=%v", release != nil, err)
	}
}

func TestSQLiteSourceTaskActive(t *testing.T) {
	for _, status := range []string{"running", "pending", "paused", "preparing"} {
		if !sqliteSourceTaskActive(status) {
			t.Fatalf("status %q should block a source switch", status)
		}
	}
	for _, status := range []string{"", "idle", "disabled", "succeeded", "failed", "canceled", "completed"} {
		if sqliteSourceTaskActive(status) {
			t.Fatalf("status %q should not block a source switch", status)
		}
	}
}
