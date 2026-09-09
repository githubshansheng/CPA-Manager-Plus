package databasemanagement

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestSynchronizeWriteRoutingResumesOneSidedSwitch(t *testing.T) {
	first, _ := openFailoverMetadata(t, "first.sqlite")
	second, _ := openFailoverMetadata(t, "second.sqlite")
	ctx := context.Background()
	if err := synchronizeWriteRouting(ctx, first, databasemigration.BackendMySQL, 2); err != nil {
		t.Fatal(err)
	}
	if err := synchronizeWriteRouting(ctx, first, databasemigration.BackendMySQL, 2); err != nil {
		t.Fatalf("resume already-switched routing: %v", err)
	}
	if err := synchronizeWriteRouting(ctx, second, databasemigration.BackendMySQL, 2); err != nil {
		t.Fatalf("finish second routing row: %v", err)
	}
	for name, repository := range map[string]*databasemigration.SQLRepository{"first": first, "second": second} {
		routing, err := repository.Routing(ctx)
		if err != nil || routing.WritePrimary != databasemigration.BackendMySQL || routing.Epoch != 2 {
			t.Fatalf("%s routing=%#v, err=%v", name, routing, err)
		}
	}
	if err := synchronizeWriteRouting(ctx, first, databasemigration.BackendMySQL, 3); err == nil {
		t.Fatal("already-switched routing accepted a different desired epoch")
	}
}

func TestFailoverDesiredEpochPersistsAcrossCrash(t *testing.T) {
	state := control.DefaultState()
	state.RoutingEpoch = 9
	epoch, err := failoverDesiredEpoch(state)
	if err != nil || epoch != 10 {
		t.Fatalf("new desired epoch=%d, err=%v", epoch, err)
	}
	state.Failover = control.FailoverState{Status: "switching", From: database.BackendSQLite,
		To: database.BackendMySQL, Epoch: 10}
	state.RoutingEpoch = 9
	epoch, err = failoverDesiredEpoch(state)
	if err != nil || epoch != 10 {
		t.Fatalf("recovered desired epoch=%d, err=%v", epoch, err)
	}
}

func TestReturnToSQLiteRequiresReverseInboxWatermark(t *testing.T) {
	source, sourceDB := openFailoverMetadata(t, "mysql-source.sqlite")
	target, _ := openFailoverMetadata(t, "sqlite-target.sqlite")
	ctx := context.Background()
	if err := synchronizeWriteRouting(ctx, source, databasemigration.BackendMySQL, 2); err != nil {
		t.Fatal(err)
	}
	group := failoverMutationGroup()
	tx, err := sourceDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.AppendOutbox(ctx, tx, group); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := requireFailoverReplicationBoundary(ctx, database.BackendMySQL, target, source); err == nil {
		t.Fatal("pending reverse Outbox did not block returning writes to SQLite")
	}
	pending, err := source.PendingOutbox(ctx, 1)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending reverse group=%#v, err=%v", pending, err)
	}
	if _, err := target.ApplyInboxGroup(ctx, pending[0],
		func(context.Context, *sql.Tx, databasemigration.Mutation) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := source.MarkOutboxApplied(ctx, pending[0], time.UnixMilli(100)); err != nil {
		t.Fatal(err)
	}
	if err := requireFailoverReplicationBoundary(ctx, database.BackendMySQL, target, source); err != nil {
		t.Fatalf("caught-up reverse replication blocked failback: %v", err)
	}
}

func openFailoverMetadata(t *testing.T, name string) (*databasemigration.SQLRepository, *sql.DB) {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := databasemigration.NewSQLRepository(db, databasemigration.DialectSQLite)
	if err := repository.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return repository, db
}

func failoverMutationGroup() databasemigration.MutationGroup {
	key, _ := json.Marshal(map[string]any{"key": map[string]any{"type": "text", "value": "x"}})
	payload, _ := json.Marshal(map[string]any{
		"key":           map[string]any{"type": "text", "value": "x"},
		"value":         map[string]any{"type": "text", "value": "y"},
		"updated_at_ms": map[string]any{"type": "integer", "value": "1"},
	})
	mutation := databasemigration.Mutation{ID: "failover-mutation", TransactionID: "failover-transaction",
		Source: databasemigration.BackendMySQL, Target: databasemigration.BackendSQLite,
		SourceEpoch: 2, Table: "settings", Operation: databasemigration.OperationInsert,
		PrimaryKey: key, Payload: payload, SchemaVersion: 1, RowVersion: 1, OutboxID: 1}
	return databasemigration.MutationGroup{TransactionID: mutation.TransactionID,
		Source: mutation.Source, Target: mutation.Target, SourceEpoch: mutation.SourceEpoch,
		Mutations: []databasemigration.Mutation{mutation}}
}
