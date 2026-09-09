package databasemanagement

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/outboxcontext"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

const cacheTestDayMS int64 = 24 * 60 * 60 * 1000

func TestSQLiteCacheCleanupPlanClassifiesEveryAuthoritativeTable(t *testing.T) {
	classified := map[string]string{
		"settings":                  "permanent configuration",
		"model_prices":              "permanent configuration",
		"model_price_context_tiers": "permanent configuration",
		"model_price_service_tiers": "permanent configuration",
		"api_key_aliases":           "permanent configuration",
	}
	for _, table := range sqliteCacheCleanupOrder {
		if prior := classified[table]; prior != "" {
			t.Fatalf("authoritative table %s classified twice (%s)", table, prior)
		}
		if protectedCleanupTable(table) {
			classified[table] = "protected business data"
		} else {
			classified[table] = "prunable business data"
		}
	}
	for _, table := range schema.Current().AuthoritativeTables() {
		if classified[table.Name] == "" {
			t.Errorf("authoritative table %s is absent from the fixed cache plan", table.Name)
		}
		delete(classified, table.Name)
	}
	for table, class := range classified {
		t.Errorf("cache plan classifies non-authoritative table %s as %s", table, class)
	}
}

func TestSQLiteCacheCleanupUsageIsBoundedAtomicAndKeepsIdentityTombstone(t *testing.T) {
	db, replication := openCacheRuntimeDB(t)
	ctx := context.Background()
	cutoffMS := 2 * cacheTestDayMS
	oldAtMS := cacheTestDayMS / 2
	newAtMS := 3 * cacheTestDayMS
	tx, err := outboxcontext.Begin(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_events
		(id,event_hash,timestamp_ms,timestamp,model,created_at_ms) VALUES
		(1,'cache-old',?,'old','gpt-test',?),
		(2,'cache-new',?,'new','gpt-test',?)`, oldAtMS, oldAtMS, newAtMS, newAtMS); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_event_identity_ledger
		(event_hash,raw_event_id,timestamp_ms,bucket_ms,first_seen_at_ms,updated_at_ms) VALUES
		('cache-old',1,?,0,?,?),('cache-new',2,?,?,?,?)`, oldAtMS, oldAtMS, oldAtMS,
		newAtMS, newAtMS-newAtMS%cacheTestDayMS, newAtMS, newAtMS); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE usage_data_migrations SET status='completed'
		WHERE name='usage_cache_accounting_v2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO usage_monitoring_selector_daily_rollups_v1
		(model_format_revision,bucket_ms,model,api_key_hash,provider,auth_file_snapshot,
		account_snapshot,auth_label_snapshot,auth_index,source,source_hash,updated_at_ms)
		VALUES
		('',0,'gpt-test','key-1','openai','a.json','account','label','1','collector','source-1',1),
		('',?,'gpt-test','key-2','openai','b.json','account','label','2','collector','source-2',1)`, cacheTestDayMS); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO usage_monitoring_event_projection_v1
		(event_id,timestamp_ms,search_text,account_key,provider,executor_type,model,
		analytics_model,resolved_model,auth_index,source,source_hash,api_key_hash,
		account_snapshot,auth_label_snapshot,auth_file_snapshot,auth_provider_snapshot,
		auth_project_id_snapshot,reasoning_effort,service_tier,failed,input_tokens,
		output_tokens,reasoning_tokens,cached_tokens,cache_tokens,cache_read_tokens,
		cache_creation_tokens,normalized_total_input_tokens,total_tokens,
		header_quota_plan_type,header_error_kind,header_error_code,header_trace_id,updated_at_ms)
		VALUES(1,?,'old searchable event','account','openai','collector','gpt-test',
		'gpt-test','gpt-test','1','collector','source-1','key-1','account','label',
		'a.json','openai','project','','default',0,1,2,0,0,0,0,0,1,3,'','','','',?)`, oldAtMS, oldAtMS); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO usage_monitoring_header_latest_v1
		(snapshot_key,event_id,event_hash,timestamp_ms,auth_file_snapshot,auth_index,
		account_snapshot,auth_label_snapshot,auth_provider_snapshot,auth_project_id_snapshot,
		source,source_hash,response_metadata_json,header_quota_plan_type,header_error_kind,
		header_error_code,header_trace_id,updated_at_ms)
		VALUES('old-header',1,'cache-old',?,'a.json','1','account','label','openai',
		'project','collector','source-1','{}','','','','',?)`, oldAtMS, oldAtMS); err != nil {
		t.Fatal(err)
	}
	watermark := markCacheRuntimeOutboxApplied(t, replication)
	data := NewSQLiteCacheCleanupData(db)

	preview, err := data.Preview(ctx, cutoffMS)
	if err != nil {
		t.Fatal(err)
	}
	usagePreview := cachePreviewFor(t, preview, "usage_events")
	if usagePreview.Rows != 1 || usagePreview.ProtectedRows != 0 ||
		usagePreview.FromMS != oldAtMS || usagePreview.ToMS != oldAtMS {
		t.Fatalf("usage preview=%#v", usagePreview)
	}
	constraints := databasemigration.CleanupConstraints{CutoffMS: cutoffMS,
		SynchronizedWatermark: watermark, ProtectActiveAndIncomplete: true, Limit: 1}
	first, err := data.DeleteBatch(ctx, "usage_events", constraints)
	if err != nil {
		t.Fatal(err)
	}
	if first.Deleted != 0 || first.Done {
		t.Fatalf("selector prepass result=%#v", first)
	}
	assertCacheCount(t, db, "usage_events", 2)
	assertCacheCount(t, db, "usage_monitoring_selector_daily_rollups_v1", 1)
	var metadataVersion int
	if err := db.QueryRow(`SELECT schema_version FROM usage_monitoring_rollup_state
		WHERE rollup_name='metadata_v1'`).Scan(&metadataVersion); err != nil {
		t.Fatal(err)
	}
	if metadataVersion != 0 {
		t.Fatalf("metadata schema version=%d during selector prepass", metadataVersion)
	}

	second, err := data.DeleteBatch(ctx, "usage_events", constraints)
	if err != nil {
		t.Fatal(err)
	}
	if second.Deleted != 1 || !second.Done {
		t.Fatalf("usage cleanup result=%#v", second)
	}
	if second.Coverage.EarliestAtMS != newAtMS || second.Coverage.LatestAtMS != newAtMS ||
		second.Coverage.EarliestID != 2 || second.Coverage.LatestID != 2 || second.Coverage.Watermark != watermark {
		t.Fatalf("usage coverage=%#v", second.Coverage)
	}
	for _, table := range []string{"usage_monitoring_selector_daily_rollups_v1",
		"usage_monitoring_event_projection_v1", "usage_monitoring_header_latest_v1"} {
		assertCacheCount(t, db, table, 0)
	}
	assertCacheCount(t, db, "usage_events", 1)
	assertCacheCount(t, db, "usage_event_identity_ledger", 2)
	var rawEventID int64
	if err := db.QueryRow(`SELECT raw_event_id FROM usage_event_identity_ledger WHERE event_hash='cache-old'`).Scan(&rawEventID); err != nil {
		t.Fatal(err)
	}
	if rawEventID != 1 {
		t.Fatalf("identity tombstone raw event id=%d", rawEventID)
	}
	for query, want := range map[string]string{
		`SELECT status FROM usage_data_migrations WHERE name='usage_cache_accounting_v2'`:    "clearing",
		`SELECT status FROM usage_hourly_aggregate_state WHERE aggregate_name='hourly_core'`: "clearing",
		`SELECT status FROM usage_pricing_rollup_state WHERE rollup_name='pricing_v1'`:       "clearing",
		`SELECT status FROM usage_monitoring_rollup_state WHERE rollup_name='stats_v1'`:      "clearing",
		`SELECT status FROM usage_monitoring_rollup_state WHERE rollup_name='metadata_v1'`:   "rebuilding",
	} {
		var got string
		if err := db.QueryRow(query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s returned %q, want %q", query, got, want)
		}
	}
	if err := outboxcontext.Audit(ctx, db); err != nil {
		t.Fatalf("journal restoration: %v", err)
	}
}

func TestSQLiteCacheCleanupWithNoUsageCandidatesLeavesDerivedStateReady(t *testing.T) {
	db, _ := openCacheRuntimeDB(t)
	ctx := context.Background()
	data := NewSQLiteCacheCleanupData(db)
	result, err := data.DeleteBatch(ctx, "usage_events", databasemigration.CleanupConstraints{
		CutoffMS: 1_000, SynchronizedWatermark: 0,
		ProtectActiveAndIncomplete: true, Limit: 10,
	})
	if err != nil || result.Deleted != 0 || !result.Done {
		t.Fatalf("empty usage cleanup=%#v err=%v", result, err)
	}
	var accountingStatus, metadataStatus string
	var metadataVersion int
	if err := db.QueryRow(`SELECT status FROM usage_data_migrations
		WHERE name='usage_cache_accounting_v2'`).Scan(&accountingStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status,schema_version FROM usage_monitoring_rollup_state
		WHERE rollup_name='metadata_v1'`).Scan(&metadataStatus, &metadataVersion); err != nil {
		t.Fatal(err)
	}
	if accountingStatus != "completed" || metadataStatus != "ready" || metadataVersion == 0 {
		t.Fatalf("empty cleanup dirtied derived state: accounting=%q metadata=%q version=%d",
			accountingStatus, metadataStatus, metadataVersion)
	}
}

func TestSQLiteCacheCleanupDeletesOnlyOldTerminalRowsAndProtectsUnsafeTables(t *testing.T) {
	db, replication := openCacheRuntimeDB(t)
	ctx := context.Background()
	cutoffMS := int64(1_000)
	tx, err := outboxcontext.Begin(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statements := []string{
		`INSERT INTO account_action_candidates
			(action_type,status,auth_file_name,first_seen_at_ms,last_seen_at_ms,created_at_ms,updated_at_ms)
			VALUES('disable','ignored','old.json',1,1,1,100),
			('disable','pending','active.json',1,1,1,100),
			('disable','resolved','new.json',2000,2000,2000,2000)`,
		`INSERT INTO quota_cooldowns
			(auth_file_name,recover_at_ms,owner,status,disabled_at_ms,recovered_at_ms,created_at_ms,updated_at_ms)
			VALUES('old.json',1,'test','recovered',1,100,1,100),
			('active.json',1,'test','active',1,NULL,1,100),
			('new.json',2000,'test','recovered',2000,2000,2000,2000)`,
		`INSERT INTO account_quota_observations
			(observation_hash,account_key,provider,source,inventory_scope_key,inventory_mode,
			observed_at_ms,created_at_ms) VALUES('protected-observation','account','openai',
			'test','scope','full',100,100)`,
		`INSERT INTO dead_letter_events(payload,error,created_at_ms) VALUES('{}','failed',100)`,
		`INSERT INTO codex_inspection_disable_ownership
			(file_name,disabled_at_ms,updated_at_ms) VALUES('owned.json',100,100)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	watermark := markCacheRuntimeOutboxApplied(t, replication)
	data := NewSQLiteCacheCleanupData(db)
	preview, err := data.Preview(ctx, cutoffMS)
	if err != nil {
		t.Fatal(err)
	}
	if got := cachePreviewFor(t, preview, "account_action_candidates"); got.Rows != 1 || got.ProtectedRows != 1 {
		t.Fatalf("account action preview=%#v", got)
	}
	if got := cachePreviewFor(t, preview, "quota_cooldowns"); got.Rows != 1 || got.ProtectedRows != 1 {
		t.Fatalf("cooldown preview=%#v", got)
	}
	for _, table := range []string{"account_quota_observations", "dead_letter_events",
		"codex_inspection_disable_ownership"} {
		got := cachePreviewFor(t, preview, table)
		if got.Rows != 0 || got.ProtectedRows != 1 {
			t.Fatalf("protected %s preview=%#v", table, got)
		}
	}
	constraints := databasemigration.CleanupConstraints{CutoffMS: cutoffMS,
		SynchronizedWatermark: watermark, ProtectActiveAndIncomplete: true, Limit: 1}
	for _, table := range []string{"account_action_candidates", "quota_cooldowns"} {
		first, err := data.DeleteBatch(ctx, table, constraints)
		if err != nil || first.Deleted != 1 || first.Done {
			t.Fatalf("first %s cleanup=%#v, err=%v", table, first, err)
		}
		second, err := data.DeleteBatch(ctx, table, constraints)
		if err != nil || second.Deleted != 0 || !second.Done {
			t.Fatalf("completion %s cleanup=%#v, err=%v", table, second, err)
		}
		assertCacheCount(t, db, table, 2)
	}
	for _, table := range []string{"account_quota_observations", "dead_letter_events",
		"codex_inspection_disable_ownership"} {
		result, err := data.DeleteBatch(ctx, table, constraints)
		if err != nil || result.Deleted != 0 || !result.Done {
			t.Fatalf("protected %s cleanup=%#v, err=%v", table, result, err)
		}
		assertCacheCount(t, db, table, 1)
	}
}

func TestSQLiteCacheCleanupRespectsInspectionAndQuotaDependencyOrder(t *testing.T) {
	db, replication := openCacheRuntimeDB(t)
	ctx := context.Background()
	cutoffMS := int64(1_000)
	tx, err := outboxcontext.Begin(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statements := []string{
		`INSERT INTO codex_inspection_runs
			(id,trigger_type,status,started_at_ms,finished_at_ms,settings_json,created_at_ms,updated_at_ms)
			VALUES(1,'manual','completed',1,100,'{}',1,100),
			(2,'manual','completed',1,100,'{}',1,100),
			(3,'manual','running',1,NULL,'{}',1,100),
			(4,'manual','completed',1,100,'{}',1,100)`,
		`INSERT INTO codex_inspection_results
			(run_id,account_key,file_name,display_account,action,action_status,created_at_ms)
			VALUES(1,'eligible','eligible.json','eligible','keep','resolved',100),
			(2,'pending','pending.json','pending','disable','pending',100)`,
		`INSERT INTO codex_inspection_logs(run_id,level,message,created_at_ms)
			VALUES(1,'info','eligible',100)`,
		`INSERT INTO codex_inspection_leases(id,run_id,owner_id,heartbeat_at_ms,lease_expires_at_ms)
			VALUES(1,4,'owner',100,200)`,
		`INSERT INTO account_quota_observations
			(id,observation_hash,account_key,provider,source,inventory_scope_key,inventory_mode,
			observed_at_ms,created_at_ms) VALUES
			(1,'quota-stale','account','openai','test','scope','full',100,100),
			(2,'quota-current','account','openai','test','scope','full',200,200)`,
		`INSERT INTO account_quota_windows
			(id,account_key,provider,provider_window_id,window_kind,window_mode,model_scope_kind,
			scope_fingerprint,inventory_scope_key,availability,first_seen_at_ms,last_seen_at_ms,
			deactivated_at_ms,last_observation_id,created_at_ms,updated_at_ms)
			VALUES(1,'account','openai','window','primary','rolling','all','fingerprint','scope',
			'inactive',1,100,100,2,1,100)`,
		`INSERT INTO account_quota_window_activations
			(id,window_id,generation,status,activated_at_ms,deactivated_at_ms,activation_accuracy,
			activate_observation_id,deactivate_observation_id,created_at_ms,updated_at_ms)
			VALUES(1,1,1,'inactive',1,100,'exact',1,2,1,100)`,
		`INSERT INTO account_quota_cycles
			(id,activation_id,provider_cycle_key,state,actual_start_ms,actual_end_ms,
			boundary_accuracy,first_observation_id,last_observation_id,parent_cycle_id,created_at_ms,updated_at_ms)
			VALUES(1,1,'parent','ended',1,100,'exact',1,2,NULL,1,100),
			(2,1,'child','ended',1,100,'exact',1,2,1,1,100)`,
		`INSERT INTO account_quota_snapshots
			(observation_id,logical_window_id,activation_id,cycle_id,account_key,provider,
			provider_window_id,window_kind,window_mode,model_scope_kind,source,observed_at_ms,
			boundary_accuracy,created_at_ms)
			VALUES(1,1,1,2,'account','openai','window','primary','rolling','all','test',100,'exact',100)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	watermark := markCacheRuntimeOutboxApplied(t, replication)
	data := NewSQLiteCacheCleanupData(db)
	constraints := databasemigration.CleanupConstraints{CutoffMS: cutoffMS,
		SynchronizedWatermark: watermark, ProtectActiveAndIncomplete: true, Limit: 1}

	for _, table := range []string{"codex_inspection_results", "codex_inspection_logs", "codex_inspection_runs"} {
		first, err := data.DeleteBatch(ctx, table, constraints)
		if err != nil || first.Deleted != 1 || first.Done {
			t.Fatalf("first inspection %s cleanup=%#v, err=%v", table, first, err)
		}
		second, err := data.DeleteBatch(ctx, table, constraints)
		if err != nil || second.Deleted != 0 || !second.Done {
			t.Fatalf("completion inspection %s cleanup=%#v, err=%v", table, second, err)
		}
	}
	assertCacheIDs(t, db, "codex_inspection_runs", []int64{2, 3, 4})
	assertCacheCount(t, db, "codex_inspection_results", 1)
	assertCacheCount(t, db, "codex_inspection_leases", 1)
	leaseResult, err := data.DeleteBatch(ctx, "codex_inspection_leases", constraints)
	if err != nil || !leaseResult.Done || leaseResult.Deleted != 0 {
		t.Fatalf("lease protection=%#v, err=%v", leaseResult, err)
	}

	for _, table := range []string{"account_quota_snapshots", "account_quota_cycles",
		"account_quota_window_activations", "account_quota_windows"} {
		deleted := int64(0)
		for attempts := 0; attempts < 4; attempts++ {
			batch, err := data.DeleteBatch(ctx, table, constraints)
			if err != nil {
				t.Fatalf("quota %s cleanup: %v", table, err)
			}
			deleted += batch.Deleted
			if batch.Done {
				break
			}
			if attempts == 3 {
				t.Fatalf("quota %s cleanup did not complete", table)
			}
		}
		want := int64(1)
		if table == "account_quota_cycles" {
			want = 2
		}
		if deleted != want {
			t.Fatalf("quota %s deleted=%d, want %d", table, deleted, want)
		}
		assertCacheCount(t, db, table, 0)
	}
	assertCacheCount(t, db, "account_quota_observations", 2)
}

func TestSQLiteCacheCleanupRejectsChangedWatermarkWithoutMutation(t *testing.T) {
	db, replication := openCacheRuntimeDB(t)
	ctx := context.Background()
	tx, err := outboxcontext.Begin(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_action_candidates
		(action_type,status,auth_file_name,first_seen_at_ms,last_seen_at_ms,created_at_ms,updated_at_ms)
		VALUES('disable','resolved','old.json',1,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, _, _, watermark, err := replication.OutboxBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data := NewSQLiteCacheCleanupData(db)
	_, err = data.DeleteBatch(ctx, "account_action_candidates", databasemigration.CleanupConstraints{
		CutoffMS: 100, SynchronizedWatermark: watermark, ProtectActiveAndIncomplete: true, Limit: 10,
	})
	if !errors.Is(err, outboxcontext.ErrReplicationWatermarkChanged) {
		t.Fatalf("pending watermark cleanup error=%v", err)
	}
	assertCacheCount(t, db, "account_action_candidates", 1)
	if err := outboxcontext.Audit(ctx, db); err != nil {
		t.Fatalf("journal after rejected cleanup: %v", err)
	}
}

func TestSQLiteCacheCleanupRollsBackWhenDerivedSafetyStateIsMissing(t *testing.T) {
	db, replication := openCacheRuntimeDB(t)
	ctx := context.Background()
	tx, err := outboxcontext.Begin(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_events
		(id,event_hash,timestamp_ms,timestamp,model,created_at_ms)
		VALUES(1,'rollback-old',100,'old','gpt-test',100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_event_identity_ledger
		(event_hash,raw_event_id,timestamp_ms,bucket_ms,first_seen_at_ms,updated_at_ms)
		VALUES('rollback-old',1,100,0,100,100)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	watermark := markCacheRuntimeOutboxApplied(t, replication)
	if _, err := db.ExecContext(ctx, `DELETE FROM usage_hourly_aggregate_state
		WHERE aggregate_name='hourly_core'`); err != nil {
		t.Fatal(err)
	}
	data := NewSQLiteCacheCleanupData(db)
	_, err = data.DeleteBatch(ctx, "usage_events", databasemigration.CleanupConstraints{
		CutoffMS: 1_000, SynchronizedWatermark: watermark,
		ProtectActiveAndIncomplete: true, Limit: 10,
	})
	if err == nil {
		t.Fatal("cleanup accepted a missing derived safety state")
	}
	assertCacheCount(t, db, "usage_events", 1)
	var accountingStatus string
	if err := db.QueryRow(`SELECT status FROM usage_data_migrations
		WHERE name='usage_cache_accounting_v2'`).Scan(&accountingStatus); err != nil {
		t.Fatal(err)
	}
	if accountingStatus != "completed" {
		t.Fatalf("rolled back accounting status=%q", accountingStatus)
	}
	var earliestAtMS, latestAtMS int64
	if err := db.QueryRow(`SELECT earliest_at_ms,latest_at_ms FROM database_cache_coverage
		WHERE id=1`).Scan(&earliestAtMS, &latestAtMS); err != nil {
		t.Fatal(err)
	}
	if earliestAtMS != 0 || latestAtMS != 0 {
		t.Fatalf("rolled back coverage=%d..%d", earliestAtMS, latestAtMS)
	}
	if err := outboxcontext.Audit(ctx, db); err != nil {
		t.Fatalf("journal after derived-state rollback: %v", err)
	}
}

func openCacheRuntimeDB(t *testing.T) (*sql.DB, *databasemigration.SQLRepository) {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "cache-runtime.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	replication := databasemigration.NewSQLRepository(db, databasemigration.DialectSQLite)
	if err := replication.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	current := schema.Current()
	authoritative := current.AuthoritativeTables()
	known := make(map[string]bool, len(authoritative))
	for _, table := range authoritative {
		known[table.Name] = true
	}
	manifest := make(databasemigration.StaticManifest, 0, len(authoritative))
	for _, table := range authoritative {
		spec := databasemigration.TableSpec{Name: table.Name}
		for _, column := range table.Columns {
			spec.Columns = append(spec.Columns, databasemigration.ColumnSpec{Name: column.Name,
				Nullable:    column.Nullable && column.PrimaryKeyPosition == 0,
				LogicalType: string(column.Kind), DefaultSQL: column.Default})
			if column.PrimaryKeyPosition > 0 {
				spec.PrimaryKey = append(spec.PrimaryKey, column.Name)
			}
		}
		for _, foreignKey := range table.ForeignKeys {
			if foreignKey.RefTable != table.Name && known[foreignKey.RefTable] &&
				!cacheContains(spec.Dependencies, foreignKey.RefTable) {
				spec.Dependencies = append(spec.Dependencies, foreignKey.RefTable)
			}
		}
		manifest = append(manifest, spec)
	}
	installer := databasemigration.SQLiteJournalInstaller{DB: db, Manifest: manifest,
		Provider: &outboxcontext.SQLiteProvider{SchemaVersion: current.Version}, SchemaVersion: current.Version}
	if err := installer.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if err := outboxcontext.Enable(ctx, db, 1); err != nil {
		t.Fatal(err)
	}
	return db, replication
}

func markCacheRuntimeOutboxApplied(t *testing.T, repository *databasemigration.SQLRepository) int64 {
	t.Helper()
	ctx := context.Background()
	groups, err := repository.PendingOutbox(ctx, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		if err := repository.MarkOutboxApplied(ctx, group, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	rows, _, _, watermark, err := repository.OutboxBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("outbox still has %d pending rows", rows)
	}
	return watermark
}

func cachePreviewFor(t *testing.T, previews []databasemigration.TableCleanupPreview, table string) databasemigration.TableCleanupPreview {
	t.Helper()
	for _, preview := range previews {
		if preview.Table == table {
			return preview
		}
	}
	t.Fatalf("preview omitted %s", table)
	return databasemigration.TableCleanupPreview{}
}

func assertCacheCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s row count=%d, want %d", table, got, want)
	}
}

func assertCacheIDs(t *testing.T, db *sql.DB, table string, want []int64) {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM "` + table + `" ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("%s ids=%v, want %v", table, got, want)
	}
	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("%s ids=%v, want %v", table, got, want)
		}
	}
}

func cacheContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
