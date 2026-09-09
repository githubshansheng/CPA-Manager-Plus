package databasemanagement

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/outboxcontext"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func TestReplaceSQLiteCacheFileInstallsValidatedCacheAndReenablesJournal(t *testing.T) {
	fixture := openSQLiteReplacementFixture(t, "old-cache", "new-cache")

	next, err := fixture.runtime.ReplaceSQLiteCacheFile(
		context.Background(), fixture.temporaryPath, fixture.state.Generation, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.Kind() != database.BackendSQLite || next.DB() == nil {
		t.Fatalf("replacement backend = %#v", next)
	}
	assertSQLiteReplacementMarker(t, next.DB(), "new-cache")
	if _, err := os.Stat(fixture.temporaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary cache still exists: %v", err)
	}
	if _, err := os.Stat(fixture.livePath + sqliteRebuildBackupSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement backup still exists: %v", err)
	}

	ctx := context.Background()
	tx, err := outboxcontext.Begin(ctx, next.DB(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key,value,updated_at_ms)
		VALUES ('sqlite-replacement-journal','enabled',1)`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var epoch uint64
	var target, table string
	if err := next.DB().QueryRowContext(ctx, `SELECT source_epoch,target_backend,table_name
		FROM database_outbox ORDER BY outbox_id DESC LIMIT 1`).Scan(&epoch, &target, &table); err != nil {
		t.Fatal(err)
	}
	if epoch != fixture.state.RoutingEpoch || target != string(database.BackendMySQL) || table != "settings" {
		t.Fatalf("journal epoch=%d target=%q table=%q", epoch, target, table)
	}
	if err := fixture.runtime.Close(); err != nil {
		t.Fatal(err)
	}
	closedPath := fixture.livePath + ".closed-handle-check"
	if err := os.Rename(fixture.livePath, closedPath); err != nil {
		t.Fatalf("replacement SQLite handle was not released: %v", err)
	}
	if err := os.Rename(closedPath, fixture.livePath); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceSQLiteCacheFileRejectsStaleGenerationAndWatermark(t *testing.T) {
	tests := []struct {
		name       string
		generation func(control.State) uint64
		watermark  int64
		want       error
	}{
		{
			name:       "generation",
			generation: func(state control.State) uint64 { return state.Generation + 1 },
			watermark:  0,
			want:       ErrGenerationConflict,
		},
		{
			name:       "watermark",
			generation: func(state control.State) uint64 { return state.Generation },
			watermark:  1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := openSQLiteReplacementFixture(t, "old-cache", "new-cache")
			_, err := fixture.runtime.ReplaceSQLiteCacheFile(context.Background(), fixture.temporaryPath,
				test.generation(fixture.state), test.watermark)
			if err == nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("ReplaceSQLiteCacheFile error = %v, want %v", err, test.want)
			}
			backend, release, acquireErr := fixture.runtime.AcquireBackend(context.Background(),
				database.BackendSQLite)
			if acquireErr != nil {
				t.Fatal(acquireErr)
			}
			assertSQLiteReplacementMarker(t, backend.DB(), "old-cache")
			release()
			if _, statErr := os.Stat(fixture.temporaryPath); statErr != nil {
				t.Fatalf("rejected temporary cache was moved: %v", statErr)
			}
		})
	}
}

func TestReplaceSQLiteCacheFileRestoresOldCacheWhenInstalledFileIsInvalid(t *testing.T) {
	fixture := openSQLiteReplacementFixture(t, "old-cache", "new-cache")
	if err := os.Remove(fixture.temporaryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.temporaryPath, []byte(strings.Repeat("not-a-sqlite-database", 256)), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := fixture.runtime.ReplaceSQLiteCacheFile(
		context.Background(), fixture.temporaryPath, fixture.state.Generation, 0,
	)
	if err == nil {
		t.Fatal("ReplaceSQLiteCacheFile accepted a corrupt cache")
	}
	backend, release, acquireErr := fixture.runtime.AcquireBackend(context.Background(),
		database.BackendSQLite)
	if acquireErr != nil {
		t.Fatalf("old cache was not reopened after rollback: %v (replacement error: %v)", acquireErr, err)
	}
	assertSQLiteReplacementMarker(t, backend.DB(), "old-cache")
	release()
	if _, statErr := os.Stat(fixture.livePath + sqliteRebuildBackupSuffix); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rollback backup still exists: %v", statErr)
	}
	if _, statErr := os.Stat(fixture.temporaryPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid installed cache still exists: %v", statErr)
	}
}

func TestRecoverInterruptedSQLiteCacheReplaceRestoresMissingLiveFile(t *testing.T) {
	directory := t.TempDir()
	livePath := filepath.Join(directory, "manager.sqlite")
	db, err := sqliterepo.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	writeSQLiteReplacementMarker(t, db, "recover-me")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	backupPath := livePath + sqliteRebuildBackupSuffix
	if err := moveSQLiteFileSet(livePath, backupPath, true); err != nil {
		t.Fatal(err)
	}
	if err := RecoverInterruptedSQLiteCacheReplace(livePath); err != nil {
		t.Fatal(err)
	}
	recovered, err := sqliterepo.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	assertSQLiteReplacementMarker(t, recovered, "recover-me")
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup was not consumed during recovery: %v", err)
	}

	if err := os.WriteFile(backupPath, []byte("stale-backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CleanupRecoveredSQLiteCacheBackup(livePath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backupPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("validated stale backup was not removed: %v", err)
	}
}

type sqliteReplacementFixture struct {
	runtime       *Runtime
	state         control.State
	livePath      string
	temporaryPath string
}

func openSQLiteReplacementFixture(t *testing.T, liveMarker, temporaryMarker string) sqliteReplacementFixture {
	t.Helper()
	dataDir := t.TempDir()
	livePath := filepath.Join(dataDir, "manager.sqlite")
	liveDB, err := sqliterepo.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = liveDB.Close() })
	if err := databasemigration.NewSQLRepository(liveDB,
		databasemigration.DialectSQLite).EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeSQLiteReplacementMarker(t, liveDB, liveMarker)
	if err := EnableSQLiteOutbox(context.Background(), liveDB, 1); err != nil {
		t.Fatal(err)
	}
	temporaryPath := filepath.Join(dataDir, ".sqlite-cache-rebuild-test.sqlite")
	temporaryDB, err := sqliterepo.Open(temporaryPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = temporaryDB.Close() })
	if err := databasemigration.NewSQLRepository(temporaryDB,
		databasemigration.DialectSQLite).EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := EnableSQLiteOutbox(context.Background(), temporaryDB, 1); err != nil {
		t.Fatal(err)
	}
	writeSQLiteReplacementMarker(t, temporaryDB, temporaryMarker)
	if err := temporaryDB.Close(); err != nil {
		t.Fatal(err)
	}

	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	controlStore, err := control.NewStore(filepath.Join(dataDir, "database-control.json.enc"), protector)
	if err != nil {
		t.Fatal(err)
	}
	state := control.DefaultState()
	state.RoutingEpoch = 1
	state, err = controlStore.Save(0, state)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(RuntimeOptions{
		Control: controlStore, SQLite: database.NewSQLBackend(database.BackendSQLite, liveDB),
		SQLitePath: livePath, DataDir: dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		runtime.backendMu.Lock()
		if runtime.sqlite != nil {
			_ = runtime.sqlite.Close()
			runtime.sqlite = nil
		}
		runtime.backendMu.Unlock()
	})
	return sqliteReplacementFixture{
		runtime: runtime, state: state, livePath: livePath, temporaryPath: temporaryPath,
	}
}

func writeSQLiteReplacementMarker(t *testing.T, db *sql.DB, value string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE replacement_marker (value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO replacement_marker (value) VALUES (?)`, value); err != nil {
		t.Fatal(err)
	}
}

func assertSQLiteReplacementMarker(t *testing.T, db *sql.DB, expected string) {
	t.Helper()
	var actual string
	if err := db.QueryRow(`SELECT value FROM replacement_marker`).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("replacement marker = %q, want %q", actual, expected)
	}
}
