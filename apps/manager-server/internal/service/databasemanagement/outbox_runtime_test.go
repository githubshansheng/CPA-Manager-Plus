package databasemanagement

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/outboxcontext"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestEnableSQLiteOutboxInstallsCompleteAuditedJournal(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "manager.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := EnableSQLiteOutbox(ctx, db, 1); err != nil {
		t.Fatal(err)
	}
	if err := outboxcontext.Audit(ctx, db); err != nil {
		t.Fatal(err)
	}
	// Installation and activation are restart-safe/idempotent.
	if err := EnableSQLiteOutbox(ctx, db, 1); err != nil {
		t.Fatal(err)
	}
}

func TestEnableSQLiteOutboxRejectsMismatchedRoutingFence(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "manager.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repository := databasemigration.NewSQLRepository(db, databasemigration.DialectSQLite)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := EnableSQLiteOutbox(ctx, db, 2); err == nil || !strings.Contains(err.Error(), "routing fence") {
		t.Fatalf("mismatched epoch error = %v", err)
	}
}
