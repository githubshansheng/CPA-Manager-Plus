package dialect_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/apikeyalias"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/modelprice"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	storepkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type configurationFixture struct {
	db       *sql.DB
	settings setting.Repository
	prices   modelprice.Repository
	aliases  apikeyalias.Repository
}

func TestSQLiteConfigurationRepositoryConformance(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "configuration.sqlite"))
	if err != nil {
		t.Fatalf("open SQLite fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	runConfigurationRepositoryConformance(t, newConfigurationFixture(t, db, database.BackendSQLite))
}

func TestMySQLConfigurationRepositoryConformance(t *testing.T) {
	if os.Getenv("CPAMP_MYSQL_INTEGRATION") != "1" {
		t.Skip("set CPAMP_MYSQL_INTEGRATION=1 to run against supported MySQL 8.x (8.0.12 minimum)")
	}
	config := mysqlIntegrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := dbmysql.Test(ctx, config); err != nil {
		t.Fatalf("validate MySQL integration target: %v", err)
	}
	db, err := dbmysql.Open(ctx, config)
	if err != nil {
		t.Fatalf("open MySQL integration target: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := schema.Ensure(ctx, db); err != nil {
		t.Fatalf("ensure MySQL schema: %v", err)
	}
	fixture := newConfigurationFixture(t, db, database.BackendMySQL)
	clearConfigurationTables(t, db)
	t.Cleanup(func() { clearConfigurationTables(t, db) })
	runConfigurationRepositoryConformance(t, fixture)
}

func newConfigurationFixture(t *testing.T, db *sql.DB, backend database.BackendKind) configurationFixture {
	t.Helper()
	protector, err := security.NewProtector(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatalf("create settings protector: %v", err)
	}
	configuredStore := storepkg.NewWithBackend(database.NewSQLBackend(backend, db), protector)
	return configurationFixture{
		db:       db,
		settings: configuredStore.Settings,
		prices:   configuredStore.ModelPrices,
		aliases:  configuredStore.APIKeyAliases,
	}
}

func runConfigurationRepositoryConformance(t *testing.T, fixture configurationFixture) {
	t.Helper()
	clearConfigurationTables(t, fixture.db)
	t.Run("settings complete fields and encrypted long text", func(t *testing.T) {
		testSettingsConformance(t, fixture)
	})
	t.Run("model prices replace upsert relations and atomicity", func(t *testing.T) {
		testModelPriceConformance(t, fixture)
	})
	t.Run("api key aliases unicode upsert cleanup delete and atomicity", func(t *testing.T) {
		testAPIKeyAliasConformance(t, fixture)
	})
}

func testSettingsConformance(t *testing.T, fixture configurationFixture) {
	ctx := context.Background()
	if _, found, err := fixture.settings.LoadSetup(ctx); err != nil || found {
		t.Fatalf("initial LoadSetup found=%v err=%v", found, err)
	}
	if _, found, err := fixture.settings.LoadManagerConfig(ctx); err != nil || found {
		t.Fatalf("initial LoadManagerConfig found=%v err=%v", found, err)
	}

	secret := "管理密钥🔐 " + strings.Repeat("密", 20_000) + "  "
	setup := model.Setup{
		CPAUpstreamURL: "https://例子.invalid/尾部  ",
		ManagementKey:  secret,
		Queue:          "队列-α ",
		PopSide:        "right ",
	}
	if err := fixture.settings.SaveSetup(ctx, setup); err != nil {
		t.Fatalf("SaveSetup: %v", err)
	}
	loadedSetup, found, err := fixture.settings.LoadSetup(ctx)
	wantSetup := setup
	wantSetup.ManagementKey = strings.TrimSpace(wantSetup.ManagementKey)
	if err != nil || !found || loadedSetup != wantSetup {
		t.Fatalf("LoadSetup found=%v err=%v upstreamMatch=%v secretBytes=%d/%d queueMatch=%v popSideMatch=%v",
			found, err, loadedSetup.CPAUpstreamURL == wantSetup.CPAUpstreamURL,
			len(loadedSetup.ManagementKey), len(wantSetup.ManagementKey), loadedSetup.Queue == wantSetup.Queue,
			loadedSetup.PopSide == wantSetup.PopSide)
	}
	var rawSetup string
	if err := fixture.db.QueryRowContext(ctx, "select `value` from settings where `key` = 'setup'").Scan(&rawSetup); err != nil {
		t.Fatalf("read protected setup: %v", err)
	}
	if strings.Contains(rawSetup, secret) || strings.Contains(rawSetup, "管理密钥") {
		t.Fatal("settings storage contains plaintext management key")
	}

	enabled := false
	managerConfig := model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{
			CPABaseURL:    "https://配置.invalid/path  ",
			ManagementKey: "另一个秘密🔑  ",
		},
		Collector: model.ManagerCollectorConfig{
			Enabled: &enabled, CollectorMode: "http ", Queue: "队列 ", PopSide: "left ",
			BatchSize: 37, PollIntervalMS: 809, QueryLimit: 1234, TLSSkipVerify: true,
		},
		CodexInspection: model.ManagerCodexInspectionConfig{
			Enabled: &enabled,
			Schedule: model.ManagerCodexInspectionScheduleConfig{
				Mode: "time_points", TimePoints: []string{"01:02", "23:59"},
				IntervalMinutes: 91, TimeZone: "Asia/Shanghai",
			},
			TargetTypes: []string{"codex", "xai"}, TargetType: "codex", Workers: 7,
			DeleteWorkers: 3, Timeout: 45_678, Retries: 4, UserAgent: "客户端/测试  ",
			XAIInferenceUserAgent: "xai-agent  ", XAIInferenceEnabled: true,
			XAIInferenceModel: "grok-测试", XAIInferencePrompt: "回答 OK。  ",
			UsedPercentThreshold: 87.5, SampleSize: 19, AutoActionMode: "disable",
			AutoRecoverEnabled: true,
		},
		ExternalUsageService: model.ManagerExternalUsageServiceConfig{
			Enabled: true, ServiceBase: "https://用量.invalid/  ",
		},
	}
	if err := fixture.settings.SaveManagerConfig(ctx, managerConfig); err != nil {
		t.Fatalf("SaveManagerConfig: %v", err)
	}
	loadedManager, found, err := fixture.settings.LoadManagerConfig(ctx)
	if err != nil || !found {
		t.Fatalf("LoadManagerConfig found=%v err=%v", found, err)
	}
	managerConfig.UpdatedAtMS = loadedManager.UpdatedAtMS
	managerConfig.CPAConnection.ManagementKey = strings.TrimSpace(managerConfig.CPAConnection.ManagementKey)
	if loadedManager.UpdatedAtMS <= 0 || !reflect.DeepEqual(loadedManager, managerConfig) {
		t.Fatalf("manager config round trip mismatch:\n got %#v\nwant %#v", loadedManager, managerConfig)
	}

	trueValue := true
	automation := model.AutomationSettings{
		QuotaCooldownEnabled: &trueValue, AccountActionsEnabled: &enabled,
		AccountActionsAutoDisable: &trueValue,
	}
	savedAutomation, err := fixture.settings.SaveAutomationSettings(ctx, automation)
	if err != nil || savedAutomation.UpdatedAtMS <= 0 {
		t.Fatalf("SaveAutomationSettings value=%#v err=%v", savedAutomation, err)
	}
	loadedAutomation, found, err := fixture.settings.LoadAutomationSettings(ctx)
	if err != nil || !found || !reflect.DeepEqual(loadedAutomation, savedAutomation) {
		t.Fatalf("LoadAutomationSettings found=%v err=%v value=%#v", found, err, loadedAutomation)
	}

	credential := model.AdminCredential{
		Version: 3, Salt: "盐🧂 ", KeyHash: strings.Repeat("a", 64), Iterations: 610_001,
		CreatedAtMS: 111, RotatedAtMS: 222, Source: "迁移  ",
	}
	if err := fixture.settings.SaveAdminCredential(ctx, credential); err != nil {
		t.Fatalf("SaveAdminCredential: %v", err)
	}
	loadedCredential, found, err := fixture.settings.LoadAdminCredential(ctx)
	if err != nil || !found || loadedCredential != credential {
		t.Fatalf("LoadAdminCredential found=%v err=%v value=%#v", found, err, loadedCredential)
	}

	bootstrap := model.BootstrapState{
		Version: 2, Status: "ready  ", AdminReady: true, ProjectInitialized: true,
		DataKeyReady: true, MigratedLegacy: true, HasHistoricalData: true,
	}
	if err := fixture.settings.SaveBootstrapState(ctx, bootstrap); err != nil {
		t.Fatalf("SaveBootstrapState: %v", err)
	}
	loadedBootstrap, found, err := fixture.settings.LoadBootstrapState(ctx)
	if err != nil || !found {
		t.Fatalf("LoadBootstrapState found=%v err=%v", found, err)
	}
	bootstrap.UpdatedAtMS = loadedBootstrap.UpdatedAtMS
	if loadedBootstrap.UpdatedAtMS <= 0 || loadedBootstrap != bootstrap {
		t.Fatalf("bootstrap state = %#v, want %#v", loadedBootstrap, bootstrap)
	}

	if err := fixture.settings.SaveSetup(ctx, model.Setup{
		CPAUpstreamURL: "https://replacement.invalid", ManagementKey: "替换秘密",
	}); err != nil {
		t.Fatalf("replace setup: %v", err)
	}
	replaced, found, err := fixture.settings.LoadSetup(ctx)
	if err != nil || !found || replaced.CPAUpstreamURL != "https://replacement.invalid" ||
		replaced.ManagementKey != "替换秘密" || replaced.Queue != "" {
		t.Fatalf("replaced setup found=%v err=%v value=%#v", found, err, replaced)
	}

	var count int
	if err := fixture.db.QueryRowContext(ctx, `select count(*) from settings`).Scan(&count); err != nil || count != 5 {
		t.Fatalf("settings row count=%d err=%v, want 5", count, err)
	}
	historical, err := fixture.settings.HasHistoricalData(ctx)
	if err != nil || !historical {
		t.Fatalf("HasHistoricalData=%v err=%v", historical, err)
	}
}

func testModelPriceConformance(t *testing.T, fixture configurationFixture) {
	ctx := context.Background()
	deleteModelPrices(t, fixture.db)
	longRawJSON := `{"说明":"` + strings.Repeat("雪", 20_000) + `  "}`
	syncedAt := int64(1_700_000_000_123)
	modelID := "模型-α  "
	price := model.ModelPrice{
		Prompt: 1.25, Completion: 2.5, Cache: 0.375, CacheRead: 0.125, CacheCreation: 3.75,
		PromptConfigured: true, CompletionConfigured: true, CacheReadConfigured: true,
		CacheCreationConfigured: true, Source: "来源  ", SourceModelID: "上游-模型  ",
		RawJSON: longRawJSON, SyncedAtMS: &syncedAt,
		ContextTiers: []model.ModelPriceContextTier{
			{ThresholdTokens: 200_000, Prompt: 11, Completion: 12, Cache: 13, CacheRead: 14,
				CacheCreation: 15, PromptConfigured: true, CompletionConfigured: true,
				CacheConfigured: true, CacheReadConfigured: true, CacheCreationConfigured: true},
			{ThresholdTokens: 32_000, Prompt: 0, PromptConfigured: true},
		},
		ServiceTiers: []model.ModelPriceServiceTier{
			{Mode: " FAST ", ServiceTier: " PRIORITY ", Prompt: 21, Completion: 22, Cache: 23,
				CacheRead: 24, CacheCreation: 25, PromptConfigured: true, CompletionConfigured: true,
				CacheConfigured: true, CacheReadConfigured: true, CacheCreationConfigured: true},
		},
	}
	prices := map[string]model.ModelPrice{
		modelID: price,
		"empty-optionals": {
			Prompt: 0, Completion: 0, Cache: 0, PromptConfigured: true, CompletionConfigured: true,
		},
	}
	if err := fixture.prices.ReplaceAll(ctx, prices); err != nil {
		t.Fatalf("ReplaceAll: %v", err)
	}
	loaded, err := fixture.prices.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	assertCompleteModelPrice(t, loaded, modelID, price)
	if len(loaded) != 2 || loaded["empty-optionals"].Source != "" ||
		loaded["empty-optionals"].RawJSON != "" || loaded["empty-optionals"].SyncedAtMS != nil {
		t.Fatalf("empty optional model price = %#v", loaded["empty-optionals"])
	}
	var sourceNull, sourceModelNull, rawNull, syncedNull bool
	if err := fixture.db.QueryRowContext(ctx, `select source is null, source_model_id is null,
		raw_json is null, synced_at_ms is null from model_prices where model = ?`, "empty-optionals").Scan(
		&sourceNull, &sourceModelNull, &rawNull, &syncedNull,
	); err != nil || !sourceNull || !sourceModelNull || !rawNull || !syncedNull {
		t.Fatalf("physical NULL semantics source=%v sourceModel=%v raw=%v synced=%v err=%v",
			sourceNull, sourceModelNull, rawNull, syncedNull, err)
	}

	tx, err := fixture.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin read transaction: %v", err)
	}
	loadedTx, err := fixture.prices.LoadAllTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("LoadAllTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit read transaction: %v", err)
	}
	if !reflect.DeepEqual(loadedTx, loaded) {
		t.Fatal("LoadAllTx and LoadAll returned different field values")
	}

	updated := model.ModelPrice{
		Prompt: 31, Completion: 32, Cache: 33, PromptConfigured: true,
		Source: "同步源", RawJSON: `{"sync":true}`,
		ContextTiers: []model.ModelPriceContextTier{
			{ThresholdTokens: 64_000, CacheRead: 0, CacheReadConfigured: true},
		},
		ServiceTiers: []model.ModelPriceServiceTier{
			{Mode: "batch", ServiceTier: "flex", Completion: 0, CompletionConfigured: true},
		},
	}
	result, err := fixture.prices.UpsertSynced(ctx, map[string]model.ModelPrice{
		modelID: updated,
		"invalid": {
			Prompt: -1,
		},
	})
	if err != nil || result.Imported != 1 || result.Skipped != 1 {
		t.Fatalf("UpsertSynced result=%#v err=%v", result, err)
	}
	afterSync, err := fixture.prices.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll after sync: %v", err)
	}
	synced := afterSync[modelID]
	if synced.Prompt != 31 || synced.Source != "同步源" || synced.SourceModelID != modelID ||
		synced.RawJSON != `{"sync":true}` || synced.SyncedAtMS == nil ||
		len(synced.ContextTiers) != 1 || synced.ContextTiers[0].ThresholdTokens != 64_000 ||
		len(synced.ServiceTiers) != 1 || synced.ServiceTiers[0].Mode != "batch" {
		t.Fatalf("synced price = %#v", synced)
	}
	if _, ok := afterSync["empty-optionals"]; !ok {
		t.Fatal("UpsertSynced removed an unrelated model")
	}

	beforeInvalid := clonePrices(afterSync)
	err = fixture.prices.ReplaceAll(ctx, map[string]model.ModelPrice{
		"would-replace": {Prompt: 1, PromptConfigured: true},
		"invalid":       {Prompt: -1},
	})
	if err == nil {
		t.Fatal("ReplaceAll accepted an invalid model price")
	}
	afterInvalid, loadErr := fixture.prices.LoadAll(ctx)
	if loadErr != nil || !reflect.DeepEqual(afterInvalid, beforeInvalid) {
		t.Fatalf("invalid ReplaceAll was not atomic: err=%v values=%#v", loadErr, afterInvalid)
	}

	if err := fixture.prices.ReplaceAll(ctx, map[string]model.ModelPrice{
		"replacement": {Prompt: 9, Completion: 8, PromptConfigured: true},
	}); err != nil {
		t.Fatalf("replacement ReplaceAll: %v", err)
	}
	replaced, err := fixture.prices.LoadAll(ctx)
	if err != nil || len(replaced) != 1 || replaced["replacement"].Prompt != 9 {
		t.Fatalf("replacement prices=%#v err=%v", replaced, err)
	}
	assertRelationCounts(t, fixture.db, 1, 0, 0)

	if err := fixture.prices.ReplaceAll(ctx, nil); err != nil {
		t.Fatalf("empty ReplaceAll: %v", err)
	}
	empty, err := fixture.prices.LoadAll(ctx)
	if err != nil || len(empty) != 0 {
		t.Fatalf("prices after empty replacement=%#v err=%v", empty, err)
	}
}

func testAPIKeyAliasConformance(t *testing.T, fixture configurationFixture) {
	ctx := context.Background()
	if _, err := fixture.db.ExecContext(ctx, `delete from api_key_aliases`); err != nil {
		t.Fatalf("clear aliases: %v", err)
	}
	const (
		hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		hashC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		hashD = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		hashE = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		hashF = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	)
	seed := []model.APIKeyAlias{
		{APIKeyHash: strings.ToUpper(hashA), Alias: " Žeta  ", UpdatedAtMS: 101},
		{APIKeyHash: hashB, Alias: "alpha", UpdatedAtMS: 202},
		{APIKeyHash: hashC, Alias: "βeta", UpdatedAtMS: 303},
		// These two distinguish SQLite's ASCII-only NOCASE order from a
		// Unicode-lowercase order and must be identical on MySQL.
		{APIKeyHash: hashE, Alias: "Äzulu", UpdatedAtMS: 304},
		{APIKeyHash: hashF, Alias: "äalpha", UpdatedAtMS: 305},
	}
	if err := fixture.aliases.UpsertMany(ctx, seed, nil, false); err != nil {
		t.Fatalf("seed aliases: %v", err)
	}
	aliases, err := fixture.aliases.LoadAll(ctx)
	if err != nil || len(aliases) != 5 {
		t.Fatalf("LoadAll aliases=%#v err=%v", aliases, err)
	}
	wantAliases := []model.APIKeyAlias{
		{APIKeyHash: hashA, Alias: "Žeta", UpdatedAtMS: 101},
		{APIKeyHash: hashB, Alias: "alpha", UpdatedAtMS: 202},
		{APIKeyHash: hashC, Alias: "βeta", UpdatedAtMS: 303},
		{APIKeyHash: hashE, Alias: "Äzulu", UpdatedAtMS: 304},
		{APIKeyHash: hashF, Alias: "äalpha", UpdatedAtMS: 305},
	}
	sort.Slice(wantAliases, func(i, j int) bool {
		left, right := sqliteNoCaseSortKey(wantAliases[i].Alias), sqliteNoCaseSortKey(wantAliases[j].Alias)
		if left != right {
			return left < right
		}
		return wantAliases[i].APIKeyHash < wantAliases[j].APIKeyHash
	})
	if !reflect.DeepEqual(aliases, wantAliases) {
		t.Fatalf("alias normalization/order mismatch:\n got %#v\nwant %#v", aliases, wantAliases)
	}

	if err := fixture.aliases.UpsertMany(ctx, []model.APIKeyAlias{
		{APIKeyHash: hashD, Alias: "žETA"},
	}, nil, false); err == nil || err.Error() != "api key alias already exists" {
		t.Fatalf("Unicode case-insensitive duplicate error = %v", err)
	}

	if err := fixture.aliases.UpsertMany(ctx, []model.APIKeyAlias{
		{APIKeyHash: hashA, Alias: "changed", UpdatedAtMS: 404},
		{APIKeyHash: hashD, Alias: " ALPHA "},
	}, nil, false); err == nil || err.Error() != "api key alias already exists" {
		t.Fatalf("atomic conflict error = %v", err)
	}
	afterConflict, err := fixture.aliases.LoadAll(ctx)
	if err != nil || !reflect.DeepEqual(afterConflict, aliases) {
		t.Fatalf("conflicting alias batch was not atomic: aliases=%#v err=%v", afterConflict, err)
	}

	if err := fixture.aliases.UpsertMany(ctx, []model.APIKeyAlias{
		{APIKeyHash: hashD, Alias: "Žeta", UpdatedAtMS: 505},
	}, []string{hashB, hashC, hashD}, true); err != nil {
		t.Fatalf("orphan alias migration: %v", err)
	}
	afterMigration, err := fixture.aliases.LoadAll(ctx)
	if err != nil {
		t.Fatalf("load migrated aliases: %v", err)
	}
	byAlias := make(map[string]model.APIKeyAlias, len(afterMigration))
	for _, alias := range afterMigration {
		byAlias[alias.Alias] = alias
	}
	if len(afterMigration) != 5 || byAlias["Žeta"].APIKeyHash != hashD ||
		byAlias["Žeta"].UpdatedAtMS != 505 {
		t.Fatalf("migrated aliases = %#v", afterMigration)
	}

	maxAlias := strings.Repeat("界", 120)
	if err := fixture.aliases.UpsertMany(ctx, []model.APIKeyAlias{
		{APIKeyHash: hashD, Alias: maxAlias, UpdatedAtMS: 606},
	}, nil, false); err != nil {
		t.Fatalf("120-rune alias: %v", err)
	}
	if err := fixture.aliases.UpsertMany(ctx, []model.APIKeyAlias{
		{APIKeyHash: hashD, Alias: strings.Repeat("界", 121)},
	}, nil, false); err == nil {
		t.Fatal("alias longer than 120 runes was accepted")
	}
	afterTooLong, err := fixture.aliases.LoadAll(ctx)
	if err != nil {
		t.Fatalf("load aliases after rejected long alias: %v", err)
	}
	longAliasPreserved := false
	for _, alias := range afterTooLong {
		if alias.APIKeyHash == hashD && alias.Alias == maxAlias && alias.UpdatedAtMS == 606 {
			longAliasPreserved = true
		}
	}
	if !longAliasPreserved {
		t.Fatal("rejected overlong alias changed the existing 120-rune value")
	}
	if err := fixture.aliases.Delete(ctx, "not-a-hash"); err == nil {
		t.Fatal("Delete accepted an invalid hash")
	}
	if err := fixture.aliases.Delete(ctx, strings.ToUpper(hashD)); err != nil {
		t.Fatalf("Delete valid uppercase hash: %v", err)
	}
	finalAliases, err := fixture.aliases.LoadAll(ctx)
	if err != nil || len(finalAliases) != 4 {
		t.Fatalf("aliases after delete=%#v err=%v", finalAliases, err)
	}
}

func assertCompleteModelPrice(
	t *testing.T,
	prices map[string]model.ModelPrice,
	modelID string,
	want model.ModelPrice,
) {
	t.Helper()
	got, ok := prices[modelID]
	if !ok {
		t.Fatalf("model %q missing from %#v", modelID, prices)
	}
	want.UpdatedAtMS = got.UpdatedAtMS
	want.ContextTiers, _ = model.NormalizeModelPriceContextTiers(want.ContextTiers)
	want.ServiceTiers, _ = model.NormalizeModelPriceServiceTiers(want.ServiceTiers)
	if got.UpdatedAtMS <= 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("model price round trip mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func clonePrices(source map[string]model.ModelPrice) map[string]model.ModelPrice {
	result := make(map[string]model.ModelPrice, len(source))
	for key, price := range source {
		price.ContextTiers = append([]model.ModelPriceContextTier(nil), price.ContextTiers...)
		price.ServiceTiers = append([]model.ModelPriceServiceTier(nil), price.ServiceTiers...)
		if price.SyncedAtMS != nil {
			value := *price.SyncedAtMS
			price.SyncedAtMS = &value
		}
		result[key] = price
	}
	return result
}

func assertRelationCounts(t *testing.T, db *sql.DB, prices, contextTiers, serviceTiers int) {
	t.Helper()
	for table, want := range map[string]int{
		"model_prices": prices, "model_price_context_tiers": contextTiers,
		"model_price_service_tiers": serviceTiers,
	} {
		var got int
		if err := db.QueryRow(`select count(*) from ` + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count=%d err=%v, want %d", table, got, err, want)
		}
	}
}

func clearConfigurationTables(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	statements := []string{
		`delete from model_price_service_tiers`,
		`delete from model_price_context_tiers`,
		`delete from model_prices`,
		`delete from api_key_aliases`,
		`delete from settings`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("configuration fixture cleanup failed: %v", err)
		}
	}
}

func deleteModelPrices(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"model_price_service_tiers", "model_price_context_tiers", "model_prices"} {
		if _, err := db.Exec(`delete from ` + table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
}

func mysqlIntegrationConfig(t *testing.T) dbmysql.Config {
	t.Helper()
	port, err := strconv.Atoi(os.Getenv("CPAMP_MYSQL_TEST_PORT"))
	if err != nil {
		t.Fatal("CPAMP_MYSQL_TEST_PORT must be a valid port")
	}
	config := dbmysql.Config{
		Host: os.Getenv("CPAMP_MYSQL_TEST_HOST"), Port: port,
		Database: os.Getenv("CPAMP_MYSQL_TEST_DATABASE"), Username: os.Getenv("CPAMP_MYSQL_TEST_USERNAME"),
		Password: os.Getenv("CPAMP_MYSQL_TEST_PASSWORD"), TLSMode: dbmysql.TLSDisabled,
	}
	if config.Host == "" || config.Database == "" || config.Username == "" || config.Password == "" {
		t.Fatal("CPAMP_MYSQL_TEST_HOST/PORT/DATABASE/USERNAME/PASSWORD are required")
	}
	return config
}

func sqliteNoCaseSortKey(value string) string {
	bytes := []byte(value)
	for index, char := range bytes {
		if char >= 'A' && char <= 'Z' {
			bytes[index] = char + ('a' - 'A')
		}
	}
	return string(bytes)
}
