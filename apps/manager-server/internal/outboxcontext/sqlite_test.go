package outboxcontext_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/outboxcontext"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/apikeyalias"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/deadletter"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/modelprice"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
)

func TestCanonicalAuthoritativeTablesHaveCompleteJournalContracts(t *testing.T) {
	db, _ := openJournalDatabase(t)
	ctx := context.Background()
	if err := outboxcontext.Audit(ctx, db); err != nil {
		t.Fatalf("audit authoritative journal: %v", err)
	}
	for _, table := range schema.Current().AuthoritativeTables() {
		for _, trigger := range databasemigration.JournalTriggerNames(table.Name) {
			var sqlText string
			if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&sqlText); err != nil {
				t.Fatalf("read %s trigger %s: %v", table.Name, trigger, err)
			}
			for _, column := range table.Columns {
				if !strings.Contains(sqlText, `"`+column.Name+`"`) {
					t.Fatalf("trigger %s omits %s.%s", trigger, table.Name, column.Name)
				}
			}
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at_ms) VALUES('bypass','x',1)`); err == nil {
		t.Fatal("direct authoritative write succeeded without a transaction context")
	}
	if _, err := db.ExecContext(ctx, `UPDATE database_authority_write_context SET active=1,
		transaction_id='expired',source_epoch=1,started_at_ms=1,expires_at_ms=1,row_version_base=1 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at_ms) VALUES('expired','x',1)`); err == nil {
		t.Fatal("authoritative write succeeded with an expired transaction context")
	}
	if _, err := db.ExecContext(ctx, `UPDATE database_authority_write_context SET active=0,
		transaction_id=NULL,source_epoch=0,started_at_ms=0,expires_at_ms=0,row_version_base=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
}

func TestUsageEventAndIdentityLedgerCommitWithOneStableGroup(t *testing.T) {
	db, repository := openJournalDatabase(t)
	ctx := context.Background()
	events := usageevent.New(db)
	result, err := events.InsertBatch(ctx, []model.UsageEvent{
		{EventHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", TimestampMS: 1_700_000_000_000,
			Timestamp: "2023-11-14T22:13:20Z", Model: "gpt-journal", RawJSON: `{"all":true}`,
			FailBody: "", CreatedAtMS: 1_700_000_000_001},
		{EventHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", TimestampMS: 1_700_000_000_002,
			Timestamp: "2023-11-14T22:13:20.002Z", Model: "gpt-journal", RawJSON: `{"all":false}`,
			FailBody: "failure body", Failed: true, CreatedAtMS: 1_700_000_000_003},
	})
	if err != nil || result.Inserted != 2 {
		t.Fatalf("insert usage event = %#v, err=%v", result, err)
	}
	var ledgerID int64
	if err := db.QueryRowContext(ctx, `SELECT raw_event_id FROM usage_event_identity_ledger WHERE event_hash=?`, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").Scan(&ledgerID); err != nil || ledgerID <= 0 {
		t.Fatalf("identity ledger id=%d, err=%v", ledgerID, err)
	}
	groups, err := repository.PendingOutbox(ctx, 10)
	if err != nil || len(groups) != 1 || len(groups[0].Mutations) != 2 {
		t.Fatalf("usage outbox groups=%#v, err=%v", groups, err)
	}
	mutation := groups[0].Mutations[0]
	if mutation.Table != "usage_events" || mutation.Sequence != 0 || mutation.TransactionID == "" ||
		groups[0].Mutations[1].TransactionID != mutation.TransactionID || groups[0].Mutations[1].Sequence != 1 {
		t.Fatalf("usage mutations=%#v", groups[0].Mutations)
	}
	assertStoredDigest(t, db, mutation)
	assertStoredDigest(t, db, groups[0].Mutations[1])
	var payload map[string]typedValue
	if err := json.Unmarshal(mutation.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["id"].Value != "1" || payload["raw_json"].Value != `{"all":true}` || payload["fail_body"].Type != "null" {
		t.Fatalf("usage payload did not preserve ID/raw_json/NULL: %s", mutation.Payload)
	}
}

func TestCrossTableInspectionAndQuotaWritesShareStrictSequence(t *testing.T) {
	db, repository := openJournalDatabase(t)
	ctx := context.Background()
	tx, err := outboxcontext.Begin(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO codex_inspection_runs(id,trigger_type,trigger_key,status,started_at_ms,settings_json,created_at_ms,updated_at_ms)
			VALUES(101,'manual',NULL,'completed',10,'{}',10,10)`, nil},
		{`INSERT INTO codex_inspection_results(id,run_id,account_key,file_name,display_account,action,created_at_ms)
			VALUES(102,101,'acct','auth.json','acct','keep',11)`, nil},
		{`INSERT INTO codex_inspection_logs(id,run_id,level,message,detail_json,created_at_ms)
			VALUES(103,101,'info','done',NULL,12)`, nil},
		{`INSERT INTO account_quota_observations(id,observation_hash,account_key,provider,source,source_observation_id,
			inventory_scope_key,inventory_mode,observed_at_ms,created_at_ms)
			VALUES(201,'obs-hash','acct','codex','probe',NULL,'scope','full',20,20)`, nil},
		{`INSERT INTO account_quota_windows(id,account_key,provider,provider_window_id,window_kind,window_mode,
			model_scope_kind,model_scope_key,model_ids_json,scope_fingerprint,inventory_scope_key,relationship_kind,
			container_provider_window_id,availability,first_seen_at_ms,last_seen_at_ms,last_observation_id,created_at_ms,updated_at_ms)
			VALUES(202,'acct','codex','window','primary','rolling','all',NULL,NULL,'fingerprint','scope',NULL,NULL,
			'active',20,20,201,20,20)`, nil},
		{`INSERT INTO account_quota_window_activations(id,window_id,generation,status,activated_at_ms,
			activation_accuracy,activate_observation_id,created_at_ms,updated_at_ms)
			VALUES(203,202,1,'active',20,'observed',201,20,20)`, nil},
		{`INSERT INTO account_quota_cycles(id,activation_id,provider_cycle_key,state,actual_start_ms,boundary_accuracy,
			first_observation_id,created_at_ms,updated_at_ms)
			VALUES(204,203,'cycle','active',20,'observed',201,20,20)`, nil},
		{`INSERT INTO account_quota_snapshots(id,observation_id,logical_window_id,activation_id,cycle_id,account_key,
			provider,provider_window_id,window_kind,window_mode,model_scope_kind,model_scope_key,model_ids_json,
			scope_fingerprint,content_hash,source,source_observation_id,observed_at_ms,boundary_accuracy,created_at_ms)
			VALUES(205,201,202,203,204,'acct','codex','window','primary','rolling','all',NULL,NULL,'fingerprint',
			'content','probe',NULL,20,'observed',20)`, nil},
	}
	for index, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("statement %d: %v", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	groups, err := repository.PendingOutbox(ctx, 10)
	if err != nil || len(groups) != 1 || len(groups[0].Mutations) != len(statements) {
		t.Fatalf("cross-table outbox groups=%#v, err=%v", groups, err)
	}
	group := groups[0]
	for index, mutation := range group.Mutations {
		if mutation.TransactionID != group.TransactionID || mutation.Sequence != index {
			t.Fatalf("mutation %d has transaction=%q sequence=%d; group=%q", index, mutation.TransactionID, mutation.Sequence, group.TransactionID)
		}
		assertStoredDigest(t, db, mutation)
	}
	if !strings.Contains(string(group.Mutations[0].Payload), `"trigger_key":{"type":"null"}`) ||
		!strings.Contains(string(group.Mutations[7].Payload), `"id":{"type":"integer","value":"205"}`) {
		t.Fatalf("NULL/explicit IDs not preserved: first=%s last=%s", group.Mutations[0].Payload, group.Mutations[7].Payload)
	}
}

func TestConfigurationRepositoriesJournalSettingsPricesAndAliases(t *testing.T) {
	db, repository := openJournalDatabase(t)
	ctx := context.Background()
	if err := setting.New(db).SaveSetup(ctx, model.Setup{CPAUpstreamURL: "https://manager.invalid", ManagementKey: "encrypted-secret"}); err != nil {
		t.Fatal(err)
	}
	prices := map[string]model.ModelPrice{"gpt-complete": {
		Prompt: 1.25, Completion: 2.5, Cache: 0.25, RawJSON: `{"unbounded":"完整字段"}`,
		ContextTiers: []model.ModelPriceContextTier{{ThresholdTokens: 128_000, Prompt: 3,
			PromptConfigured: true}},
		ServiceTiers: []model.ModelPriceServiceTier{{Mode: "fast", ServiceTier: "priority",
			Completion: 4, CompletionConfigured: true}},
	}}
	if err := modelprice.New(db).ReplaceAll(ctx, prices); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	if err := apikeyalias.New(db).UpsertMany(ctx, []model.APIKeyAlias{{APIKeyHash: hash, Alias: "primary"}}, nil, false); err != nil {
		t.Fatal(err)
	}
	groups, err := repository.PendingOutbox(ctx, 10)
	if err != nil || len(groups) != 3 {
		t.Fatalf("configuration groups=%#v, err=%v", groups, err)
	}
	if len(groups[0].Mutations) != 1 || groups[0].Mutations[0].Table != "settings" {
		t.Fatalf("settings group=%#v", groups[0])
	}
	wantPriceTables := []string{"model_prices", "model_price_context_tiers", "model_price_service_tiers"}
	if len(groups[1].Mutations) != len(wantPriceTables) {
		t.Fatalf("price group=%#v", groups[1])
	}
	for index, want := range wantPriceTables {
		mutation := groups[1].Mutations[index]
		if mutation.Table != want || mutation.Sequence != index {
			t.Fatalf("price mutation %d=%#v, want table %s", index, mutation, want)
		}
		assertStoredDigest(t, db, mutation)
	}
	if !strings.Contains(string(groups[1].Mutations[0].Payload), `完整字段`) {
		t.Fatalf("model raw_json missing: %s", groups[1].Mutations[0].Payload)
	}
	if len(groups[2].Mutations) != 1 || groups[2].Mutations[0].Table != "api_key_aliases" {
		t.Fatalf("alias group=%#v", groups[2])
	}
}

func TestDisabledReplicationKeepsLegacySQLiteWritesAndNoTriggers(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "disabled.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO settings(key,value,updated_at_ms) VALUES('legacy','ok',1)`); err != nil {
		t.Fatalf("legacy write while replication disabled: %v", err)
	}
	var triggers int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'cpamp_journal_%'`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if triggers != 0 {
		t.Fatalf("disabled replication installed %d journal triggers", triggers)
	}
}

func TestLocalCacheMaintenanceDeleteDoesNotProduceOutbox(t *testing.T) {
	db, repository := openJournalDatabase(t)
	ctx := context.Background()
	if err := deadletter.New(db).Insert(ctx, `{"event":"failed"}`, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO usage_event_identity_ledger
		(event_hash,raw_event_id,timestamp_ms,bucket_ms,aggregate_schema_version,aggregate_structure_revision,
		first_seen_at_ms,updated_at_ms) VALUES('derived-maintenance',NULL,10,0,0,'',10,10)`); err != nil {
		t.Fatal(err)
	}
	_, _, _, before, err := repository.OutboxBacklog(ctx)
	if err != nil || before != 1 {
		t.Fatalf("before maintenance watermark=%d, err=%v", before, err)
	}
	maintenance, err := outboxcontext.BeginLocalCacheMaintenance(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer maintenance.Rollback()
	if _, err := maintenance.DeleteWhere(ctx, "settings", `key=?`, "setup"); err == nil {
		t.Fatal("cache maintenance accepted permanent configuration data")
	}
	result, err := maintenance.DeleteWhere(ctx, "dead_letter_events", `id=?`, 1)
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		t.Fatalf("maintenance deleted %d rows", affected)
	}
	derivedResult, err := maintenance.DeleteDerivedWhere(ctx, "usage_event_identity_ledger", `event_hash=?`, "derived-maintenance")
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := derivedResult.RowsAffected(); affected != 1 {
		t.Fatalf("maintenance deleted %d derived rows", affected)
	}
	coverage := databasemigration.CacheCoverage{EarliestAtMS: 100, LatestAtMS: 200,
		EarliestID: 2, LatestID: 9, Watermark: before, UpdatedAtMS: 300}
	if err := maintenance.SaveCacheCoverage(ctx, coverage); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.Commit(); err != nil {
		t.Fatal(err)
	}
	_, _, _, after, err := repository.OutboxBacklog(ctx)
	if err != nil || after != before {
		t.Fatalf("maintenance changed outbox watermark from %d to %d, err=%v", before, after, err)
	}
	if err := outboxcontext.Audit(ctx, db); err != nil {
		t.Fatalf("journal was not restored: %v", err)
	}
	var earliestAtMS, latestAtMS, earliestID, latestID, watermark int64
	if err := db.QueryRowContext(ctx, `SELECT earliest_at_ms,latest_at_ms,earliest_id,latest_id,watermark
		FROM database_cache_coverage WHERE id=1`).Scan(&earliestAtMS, &latestAtMS, &earliestID, &latestID, &watermark); err != nil {
		t.Fatal(err)
	}
	if earliestAtMS != coverage.EarliestAtMS || latestAtMS != coverage.LatestAtMS ||
		earliestID != coverage.EarliestID || latestID != coverage.LatestID || watermark != coverage.Watermark {
		t.Fatalf("cache coverage=(%d,%d,%d,%d,%d), want %#v", earliestAtMS, latestAtMS, earliestID, latestID, watermark, coverage)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at_ms) VALUES('bypass-after-maintenance','x',1)`); err == nil {
		t.Fatal("ordinary bypass succeeded after local cache maintenance")
	}
}

func TestMySQLReplicaApplyIsAtomicIdempotentAndDoesNotLoop(t *testing.T) {
	db, repository := openJournalDatabase(t)
	ctx := context.Background()
	routing, err := repository.Routing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	routing, err = repository.CompareAndSwapRouting(ctx, routing.Generation, routing.Epoch,
		databasemigration.RoutingChange{WritePrimary: databasemigration.BackendMySQL,
			BusinessRead: routing.BusinessRead, SystemRead: routing.SystemRead})
	if err != nil || routing.Epoch != 2 {
		t.Fatalf("switch replica target fence: %#v, err=%v", routing, err)
	}

	group := mysqlSettingsMutationGroup(routing.Epoch, "reverse-tx-1", "reverse-key", "值<&\u2028", 10)
	apply, err := outboxcontext.BeginReplicaApply(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer apply.Rollback()
	result, err := apply.ApplyInboxGroup(ctx, repository, group,
		func(ctx context.Context, tx *sql.Tx, mutation databasemigration.Mutation) error {
			if mutation.Table != "settings" || mutation.Operation != databasemigration.OperationInsert {
				return errors.New("unexpected reverse mutation")
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at_ms)
				VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET
				value=excluded.value,updated_at_ms=excluded.updated_at_ms`, "reverse-key", "值<&\u2028", 10)
			return err
		})
	if err != nil || !result.Applied {
		t.Fatalf("apply reverse Inbox group: %#v, err=%v", result, err)
	}
	if err := apply.Commit(); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='reverse-key'`).Scan(&value); err != nil || value != "值<&\u2028" {
		t.Fatalf("reverse setting=%q, err=%v", value, err)
	}
	var outboxRows, inboxRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM database_outbox`).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM database_inbox WHERE transaction_id=?`, group.TransactionID).Scan(&inboxRows); err != nil {
		t.Fatal(err)
	}
	if outboxRows != 0 || inboxRows != 1 {
		t.Fatalf("reverse apply looped or omitted Inbox: outbox=%d inbox=%d", outboxRows, inboxRows)
	}
	if err := outboxcontext.Audit(ctx, db); err != nil {
		t.Fatalf("journal was not restored: %v", err)
	}

	duplicate, err := outboxcontext.BeginReplicaApply(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	duplicateResult, err := duplicate.ApplyInboxGroup(ctx, repository, group,
		func(context.Context, *sql.Tx, databasemigration.Mutation) error {
			return errors.New("duplicate unexpectedly reached applier")
		})
	if err != nil || duplicateResult.Applied {
		_ = duplicate.Rollback()
		t.Fatalf("duplicate reverse apply: %#v, err=%v", duplicateResult, err)
	}
	if err := duplicate.Commit(); err != nil {
		t.Fatal(err)
	}

	rolledBackGroup := mysqlSettingsMutationGroup(routing.Epoch, "reverse-tx-2", "rolled-back", "x", 11)
	rolledBack, err := outboxcontext.BeginReplicaApply(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = rolledBack.ApplyInboxGroup(ctx, repository, rolledBackGroup,
		func(ctx context.Context, tx *sql.Tx, _ databasemigration.Mutation) error {
			if _, execErr := tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at_ms)
				VALUES('rolled-back','x',11)`); execErr != nil {
				return execErr
			}
			return errors.New("injected reverse apply failure")
		})
	if err == nil {
		t.Fatal("reverse apply failure was not propagated")
	}
	if err := rolledBack.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key='rolled-back'`).Scan(&inboxRows); err != nil || inboxRows != 0 {
		t.Fatalf("rolled-back authoritative row count=%d, err=%v", inboxRows, err)
	}
	if err := outboxcontext.Audit(ctx, db); err != nil {
		t.Fatalf("journal was not restored after rollback: %v", err)
	}
}

func mysqlSettingsMutationGroup(epoch int64, transactionID, key, value string, rowVersion int64) databasemigration.MutationGroup {
	primaryKey, _ := json.Marshal(map[string]any{"key": map[string]any{"type": "text", "value": key}})
	payload, _ := json.Marshal(map[string]any{
		"key":           map[string]any{"type": "text", "value": key},
		"value":         map[string]any{"type": "text", "value": value},
		"updated_at_ms": map[string]any{"type": "integer", "value": "10"},
	})
	mutation := databasemigration.Mutation{
		ID: transactionID + "-mutation", TransactionID: transactionID, Sequence: 0,
		Source: databasemigration.BackendMySQL, Target: databasemigration.BackendSQLite,
		SourceEpoch: epoch, Table: "settings", Operation: databasemigration.OperationInsert,
		PrimaryKey: primaryKey, Payload: payload, SchemaVersion: schema.Current().Version,
		RowVersion: rowVersion, CreatedAtMS: 10, OutboxID: rowVersion,
	}
	return databasemigration.MutationGroup{TransactionID: transactionID,
		Source: databasemigration.BackendMySQL, Target: databasemigration.BackendSQLite,
		SourceEpoch: epoch, Watermark: rowVersion, Mutations: []databasemigration.Mutation{mutation}}
}

func TestLocalCacheMaintenanceWatermarkCoverageAndRollbackAreAtomic(t *testing.T) {
	db, repository := openJournalDatabase(t)
	ctx := context.Background()
	if result, err := usageevent.New(db).InsertBatch(ctx, []model.UsageEvent{
		{EventHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", TimestampMS: 100, Timestamp: "old", Model: "gpt-test", CreatedAtMS: 100},
		{EventHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", TimestampMS: 300, Timestamp: "new", Model: "gpt-test", CreatedAtMS: 300},
	}); err != nil || result.Inserted != 2 {
		t.Fatalf("insert cleanup fixtures=%#v, err=%v", result, err)
	}
	_, _, _, sourceWatermark, err := repository.OutboxBacklog(ctx)
	if err != nil || sourceWatermark == 0 {
		t.Fatalf("source watermark=%d, err=%v", sourceWatermark, err)
	}

	maintenance, err := outboxcontext.BeginLocalCacheMaintenance(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.AssertReplicationWatermark(ctx, sourceWatermark); !errors.Is(err, outboxcontext.ErrReplicationWatermarkChanged) {
		_ = maintenance.Rollback()
		t.Fatalf("pending outbox watermark check=%v", err)
	}
	if err := maintenance.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, "usage_events", 2)

	groups, err := repository.PendingOutbox(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		if err := repository.MarkOutboxApplied(ctx, group, time.UnixMilli(500)); err != nil {
			t.Fatal(err)
		}
	}
	maintenance, err = outboxcontext.BeginLocalCacheMaintenance(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.AssertReplicationWatermark(ctx, sourceWatermark-1); !errors.Is(err, outboxcontext.ErrReplicationWatermarkChanged) {
		_ = maintenance.Rollback()
		t.Fatalf("advanced source watermark check=%v", err)
	}
	if err := maintenance.Rollback(); err != nil {
		t.Fatal(err)
	}

	maintenance, err = outboxcontext.BeginLocalCacheMaintenance(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.AssertReplicationWatermark(ctx, sourceWatermark); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.DeleteWhere(ctx, "usage_events", `timestamp_ms < ?`, 200); err != nil {
		t.Fatal(err)
	}
	coverage, err := maintenance.RefreshCacheCoverage(ctx, sourceWatermark, false)
	if err != nil {
		t.Fatal(err)
	}
	if coverage.EarliestAtMS != 300 || coverage.LatestAtMS != 300 || coverage.EarliestID != 2 || coverage.LatestID != 2 {
		t.Fatalf("transactional coverage=%#v", coverage)
	}
	if err := maintenance.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, "usage_events", 2)
	var persistedEarliest, persistedLatest int64
	if err := db.QueryRowContext(ctx, `SELECT earliest_at_ms,latest_at_ms FROM database_cache_coverage WHERE id=1`).Scan(
		&persistedEarliest, &persistedLatest,
	); err != nil {
		t.Fatal(err)
	}
	if persistedEarliest != 0 || persistedLatest != 0 {
		t.Fatalf("rolled back coverage persisted as %d..%d", persistedEarliest, persistedLatest)
	}

	maintenance, err = outboxcontext.BeginLocalCacheMaintenance(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := maintenance.AssertReplicationWatermark(ctx, sourceWatermark); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.DeleteWhere(ctx, "usage_events", `timestamp_ms < ?`, 200); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.RefreshCacheCoverage(ctx, sourceWatermark, false); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.Commit(); err != nil {
		t.Fatal(err)
	}
	assertCount(t, db, "usage_events", 1)
	if err := outboxcontext.Audit(ctx, db); err != nil {
		t.Fatalf("journal restoration after commit: %v", err)
	}
	_, _, _, afterWatermark, err := repository.OutboxBacklog(ctx)
	if err != nil || afterWatermark != sourceWatermark {
		t.Fatalf("maintenance changed source watermark from %d to %d, err=%v", sourceWatermark, afterWatermark, err)
	}
}

func TestWriteFenceWaitsForTransactionsAndBlocksNewWrites(t *testing.T) {
	db, _ := openJournalDatabase(t)
	ctx := context.Background()
	tx, err := outboxcontext.Begin(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at_ms) VALUES('before-fence','x',1)`); err != nil {
		t.Fatal(err)
	}
	fenceReady := make(chan func(), 1)
	fenceErr := make(chan error, 1)
	go func() {
		release, err := outboxcontext.AcquireWriteFence(ctx, db)
		if err != nil {
			fenceErr <- err
			return
		}
		fenceReady <- release
	}()
	select {
	case <-fenceReady:
		t.Fatal("write fence acquired before active transaction completed")
	case err := <-fenceErr:
		t.Fatal(err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var release func()
	select {
	case release = <-fenceReady:
	case err := <-fenceErr:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("write fence did not acquire after transaction commit")
	}
	writeDone := make(chan error, 1)
	go func() {
		_, err := outboxcontext.Exec(ctx, db, `INSERT INTO settings(key,value,updated_at_ms) VALUES('after-fence','x',2)`)
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		t.Fatalf("write completed while fence held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	release() // release is intentionally idempotent.
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("write did not resume after fence release")
	}
}

func TestWrongEpochEnableFailsClosed(t *testing.T) {
	db, _ := openJournalDatabase(t)
	ctx := context.Background()
	if err := outboxcontext.Enable(ctx, db, 2); err == nil {
		t.Fatal("mismatched epoch was accepted")
	}
	if err := setting.New(db).SaveSetup(ctx, model.Setup{CPAUpstreamURL: "https://manager.invalid", ManagementKey: "secret"}); err == nil {
		t.Fatal("write succeeded after failed epoch activation")
	}
	if err := outboxcontext.Enable(ctx, db, 1); err != nil {
		t.Fatalf("restore correct epoch: %v", err)
	}
	if err := setting.New(db).SaveSetup(ctx, model.Setup{CPAUpstreamURL: "https://manager.invalid", ManagementKey: "secret"}); err != nil {
		t.Fatalf("write after correct epoch activation: %v", err)
	}
}

func TestSQLiteRestartSuspendsMigrationsAndRestoresJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.sqlite")
	db, err := sqliterepo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	installJournal(t, db)
	if err := setting.New(db).SaveSetup(context.Background(), model.Setup{CPAUpstreamURL: "https://manager.invalid", ManagementKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	suspended, err := outboxcontext.SuspendForSchemaMigration(context.Background(), db)
	if err != nil || !suspended {
		t.Fatalf("persist startup suspension=%v, err=%v", suspended, err)
	}
	// Closing here simulates a crash between migration suspension and trigger
	// restoration. Open must observe the durable marker and finish restoration.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqliterepo.Open(path)
	if err != nil {
		t.Fatalf("reopen installed journal through schema migration: %v", err)
	}
	defer reopened.Close()
	if err := outboxcontext.Audit(context.Background(), reopened); err != nil {
		t.Fatalf("restored journal audit: %v", err)
	}
	if err := setting.New(reopened).SaveSetup(context.Background(), model.Setup{CPAUpstreamURL: "https://manager.invalid/2", ManagementKey: "secret"}); err != nil {
		t.Fatalf("journaled write after restart: %v", err)
	}
}

func TestSQLiteRestartRestoresDisabledJournalAfterMySQLCutover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mysql-primary-restart.sqlite")
	db, err := sqliterepo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	installJournal(t, db)

	release, err := outboxcontext.AcquireWriteFence(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err := outboxcontext.DisableSQLite(context.Background(), db); err != nil {
		release()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE database_routing_state SET
		write_primary='mysql', business_read='mysql', system_read='sqlite', epoch=2
		WHERE id=1`); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for restart := 1; restart <= 2; restart++ {
		func() {
			reopened, err := sqliterepo.Open(path)
			if err != nil {
				t.Fatalf("reopen %d MySQL-primary SQLite replica through schema migration: %v", restart, err)
			}
			defer reopened.Close()
			if err := outboxcontext.Audit(context.Background(), reopened); err != nil {
				t.Fatalf("restart %d restored replica journal audit: %v", restart, err)
			}
			var enabled, active, suspended int
			if err := reopened.QueryRow(`SELECT enabled, active, journal_suspended
				FROM database_authority_write_context WHERE id=1`).Scan(&enabled, &active, &suspended); err != nil {
				t.Fatal(err)
			}
			if enabled != 0 || active != 0 || suspended != 0 {
				t.Fatalf("restart %d replica journal context enabled=%d active=%d suspended=%d",
					restart, enabled, active, suspended)
			}
			var primary string
			var epoch int64
			if err := reopened.QueryRow(`SELECT write_primary, epoch FROM database_routing_state WHERE id=1`).Scan(
				&primary, &epoch,
			); err != nil {
				t.Fatal(err)
			}
			if primary != "mysql" || epoch != 2 {
				t.Fatalf("restart %d changed routing primary=%q epoch=%d", restart, primary, epoch)
			}
			if _, err := reopened.Exec(`INSERT INTO settings(key,value,updated_at_ms)
				VALUES('direct-replica-write','x',1)`); err == nil {
				t.Fatalf("restart %d direct SQLite write succeeded while MySQL is authoritative", restart)
			}
		}()
	}
}

type typedValue struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

func openJournalDatabase(t *testing.T) (*sql.DB, *databasemigration.SQLRepository) {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := installJournal(t, db)
	return db, repository
}

func installJournal(t *testing.T, db *sql.DB) *databasemigration.SQLRepository {
	t.Helper()
	ctx := context.Background()
	repository := databasemigration.NewSQLRepository(db, databasemigration.DialectSQLite)
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	manifest := canonicalMigrationManifest()
	provider := &outboxcontext.SQLiteProvider{SchemaVersion: schema.Current().Version}
	installer := databasemigration.SQLiteJournalInstaller{DB: db, Manifest: manifest,
		Provider: provider, SchemaVersion: schema.Current().Version}
	if err := installer.Install(ctx); err != nil {
		t.Fatalf("install journal: %v", err)
	}
	if err := outboxcontext.Enable(ctx, db, 1); err != nil {
		t.Fatalf("enable journal: %v", err)
	}
	return repository
}

func canonicalMigrationManifest() databasemigration.StaticManifest {
	manifest := schema.Current()
	authoritative := manifest.AuthoritativeTables()
	known := make(map[string]bool, len(authoritative))
	for _, table := range authoritative {
		known[table.Name] = true
	}
	result := make(databasemigration.StaticManifest, 0, len(authoritative))
	for _, table := range authoritative {
		spec := databasemigration.TableSpec{Name: table.Name}
		for _, column := range table.Columns {
			nullable := column.Nullable
			if column.PrimaryKeyPosition > 0 {
				nullable = false
			}
			spec.Columns = append(spec.Columns, databasemigration.ColumnSpec{Name: column.Name,
				Nullable: nullable, LogicalType: string(column.Kind), DefaultSQL: column.Default})
			if column.PrimaryKeyPosition > 0 {
				spec.PrimaryKey = append(spec.PrimaryKey, column.Name)
			}
		}
		for _, foreignKey := range table.ForeignKeys {
			if foreignKey.RefTable != table.Name && known[foreignKey.RefTable] && !contains(spec.Dependencies, foreignKey.RefTable) {
				spec.Dependencies = append(spec.Dependencies, foreignKey.RefTable)
			}
		}
		result = append(result, spec)
	}
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func assertCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s row count=%d, want %d", table, got, want)
	}
}

func assertStoredDigest(t *testing.T, db *sql.DB, mutation databasemigration.Mutation) {
	t.Helper()
	var stored string
	if err := db.QueryRow(`SELECT mutation_digest FROM database_outbox WHERE mutation_id=?`, mutation.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if want := databasemigration.MutationDigest(mutation); stored != want {
		t.Fatalf("mutation digest=%s, want %s for %#v", stored, want, mutation)
	}
}
