package control

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func TestStoreEncryptsStateAndEnforcesGeneration(t *testing.T) {
	store := newTestStore(t, []byte("0123456789abcdef0123456789abcdef"))
	state := DefaultState()
	state.MySQL = dbmysql.Config{Host: "db.example.test", Database: "cpamp", Username: "manager", Password: "secret", TLSMode: dbmysql.TLSDisabled}
	state.ReplicationEnabled = true
	state.AdminAuthCopy = "admin-secret-copy"
	saved, err := store.Save(0, state)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Generation != 1 {
		t.Fatalf("generation=%d", saved.Generation)
	}
	onDisk, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret", "db.example.test", "admin-secret-copy", "adminAuthCopy"} {
		if strings.Contains(string(onDisk), secret) {
			t.Fatalf("control file exposed %q", secret)
		}
	}
	if info, err := os.Stat(store.Path()); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("control file permissions=%o", info.Mode().Perm())
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.MySQL.Password != "secret" || loaded.AdminAuthCopy != "admin-secret-copy" || loaded.Generation != 1 {
		t.Fatalf("loaded=%#v", loaded)
	}
	if _, err := store.Save(0, loaded); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("CAS error=%v", err)
	}
	encoded, err := json.Marshal(loaded.MySQL)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") {
		t.Fatal("in-memory Config JSON exposed password")
	}
	stateJSON, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stateJSON), "admin-secret-copy") {
		t.Fatal("State JSON exposed administrator authentication copy")
	}
}

func TestStoreFallsBackToLastGoodBackup(t *testing.T) {
	store := newTestStore(t, []byte("0123456789abcdef0123456789abcdef"))
	first, err := store.Save(0, DefaultState())
	if err != nil {
		t.Fatal(err)
	}
	first.CachePolicy.RetentionDays = 30
	second, err := store.Save(first.Generation, first)
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation != 2 {
		t.Fatal("second save did not advance generation")
	}
	if err := os.WriteFile(store.Path(), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Generation != 1 || loaded.CachePolicy.RetentionDays != 15 {
		t.Fatalf("backup state=%#v", loaded)
	}
	loaded.CachePolicy.RetentionDays = 45
	recovered, err := store.Save(loaded.Generation, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Generation != 2 {
		t.Fatalf("recovered generation=%d", recovered.Generation)
	}
}

func TestStoreWrongDataKeyCannotDecrypt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "database-control.json.enc")
	protector, _ := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	store, _ := NewStore(path, protector)
	if _, err := store.Save(0, DefaultState()); err != nil {
		t.Fatal(err)
	}
	other, _ := security.NewProtector([]byte("abcdef0123456789abcdef0123456789"))
	wrongStore, _ := NewStore(path, other)
	if _, err := wrongStore.Load(); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-key Load error=%v", err)
	}
}

func TestStateRequiresConfiguredMySQLForRouting(t *testing.T) {
	state := DefaultState()
	state.WritePrimary = database.BackendMySQL
	if err := state.Validate(); err == nil {
		t.Fatal("mysql routing without config succeeded")
	}
}

func TestDefaultStateDisablesScheduledCacheCleanup(t *testing.T) {
	if state := DefaultState(); state.CachePolicy.Enabled {
		t.Fatal("scheduled SQLite cache cleanup is enabled by default")
	}
}

func newTestStore(t *testing.T, key []byte) *Store {
	t.Helper()
	protector, err := security.NewProtector(key)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(filepath.Join(t.TempDir(), "database-control.json.enc"), protector)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
