package databasemanagement

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
)

type lifecycleTestBackend struct {
	closed chan struct{}
	once   sync.Once
}

func (b *lifecycleTestBackend) Kind() database.BackendKind { return database.BackendMySQL }
func (b *lifecycleTestBackend) DB() *sql.DB                { return nil }
func (b *lifecycleTestBackend) Ping(context.Context) error { return nil }
func (b *lifecycleTestBackend) Stats() sql.DBStats         { return sql.DBStats{} }
func (b *lifecycleTestBackend) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestReplaceMySQLWaitsForBackgroundBackendLease(t *testing.T) {
	oldBackend := &lifecycleTestBackend{closed: make(chan struct{})}
	nextBackend := &lifecycleTestBackend{closed: make(chan struct{})}
	runtime := &Runtime{mysql: oldBackend}

	runtime.backgroundMu.RLock()
	done := make(chan struct{})
	go func() {
		runtime.replaceMySQL(nextBackend)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("mysql pool was replaced while a background lease was active")
	case <-time.After(40 * time.Millisecond):
	}
	select {
	case <-oldBackend.closed:
		t.Fatal("old mysql pool was closed while a background lease was active")
	default:
	}

	runtime.backgroundMu.RUnlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("mysql pool replacement did not complete after lease release")
	}
	select {
	case <-oldBackend.closed:
	default:
		t.Fatal("old mysql pool was not closed after replacement")
	}
}
