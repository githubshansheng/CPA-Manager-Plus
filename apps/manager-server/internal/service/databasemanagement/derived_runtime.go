package databasemanagement

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagepricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	mysqlDerivedAggregateRevision = "mysql-derived-v1"
	mysqlDerivedHourMS            = int64(time.Hour / time.Millisecond)
	mysqlDerivedDayMS             = int64(24 * time.Hour / time.Millisecond)
	mysqlLongContextThreshold     = usage.LongContextInputTokenThreshold
)

// derivedRebuildWatermark is stored in every derived table's migration
// progress row. All tables are therefore rebuilt from one immutable usage
// event boundary even when the online Outbox continues applying newer rows.
type derivedRebuildWatermark struct {
	UsageEventID    int64  `json:"usageEventId"`
	PricingRevision string `json:"pricingRevision"`
}

type derivedProgressRepository interface {
	TableProgress(context.Context, string, string) (databasemigration.TableProgress, bool, error)
	InitializeTableProgress(context.Context, string, string, int64, json.RawMessage, int) (databasemigration.Migration, databasemigration.TableProgress, error)
	SaveTableProgress(context.Context, string, int64, databasemigration.TableProgress) (databasemigration.Migration, error)
	AdvanceMigration(context.Context, string, int64, databasemigration.MigrationPhase) (databasemigration.Migration, error)
}

type derivedPhaseExecutor interface {
	Tables() []string
	CaptureWatermark(context.Context) (derivedRebuildWatermark, error)
	RebuildTable(context.Context, string, derivedRebuildWatermark) (int64, error)
}

func (r *Runtime) rebuildMySQLDerivedOnce(
	ctx context.Context,
	repository *databasemigration.SQLRepository,
	migration databasemigration.Migration,
	mysqlDB *sql.DB,
) {
	next, advanced, err := runDerivedRebuildStep(
		ctx, repository, migration, trackedDerivedExecutor{
			runtime: r, repository: repository, migration: migration,
			delegate: newMySQLDerivedExecutor(mysqlDB),
		},
	)
	if err != nil {
		if isSupersededMigrationWorkError(err) {
			return
		}
		latest, latestErr := repository.Migration(ctx, migration.ID)
		if latestErr != nil || latest.Status != databasemigration.StatusRunning {
			return
		}
		failed, failErr := repository.FailMigration(ctx, latest.ID, latest.Generation, err)
		if failErr == nil {
			r.updateControlMigration(failed)
		}
		return
	}
	if advanced {
		r.updateControlMigration(next)
	}
}

// runDerivedRebuildStep performs at most one durable state transition or one
// table rebuild. A target-table transaction may commit immediately before a
// metadata CAS conflict; rerunning that table is safe because each rebuild
// replaces, rather than increments, its complete watermark-bounded contents.
func runDerivedRebuildStep(
	ctx context.Context,
	repository derivedProgressRepository,
	migration databasemigration.Migration,
	executor derivedPhaseExecutor,
) (databasemigration.Migration, bool, error) {
	if repository == nil || executor == nil {
		return migration, false, errors.New("derived rebuild requires progress repository and executor")
	}
	if migration.Status != databasemigration.StatusRunning || migration.Phase != databasemigration.PhaseRebuildDerived {
		return migration, false, fmt.Errorf("%w: derived rebuild requires a running rebuild_derived phase", databasemigration.ErrInvalidTransition)
	}
	tables := executor.Tables()
	if len(tables) == 0 {
		return migration, false, errors.New("derived rebuild plan is empty")
	}

	firstProgress, firstExists, err := repository.TableProgress(ctx, migration.ID, tables[0])
	if err != nil {
		return migration, false, err
	}
	if !firstExists {
		watermark, captureErr := executor.CaptureWatermark(ctx)
		if captureErr != nil {
			return migration, false, captureErr
		}
		encoded, encodeErr := json.Marshal(watermark)
		if encodeErr != nil {
			return migration, false, encodeErr
		}
		next, _, initializeErr := repository.InitializeTableProgress(
			ctx, migration.ID, tables[0], migration.Generation, encoded, 1,
		)
		return next, false, initializeErr
	}
	var watermark derivedRebuildWatermark
	if err := json.Unmarshal(firstProgress.SourceWatermark, &watermark); err != nil {
		return migration, false, fmt.Errorf("decode mysql derived rebuild watermark: %w", err)
	}
	if watermark.UsageEventID < 0 || strings.TrimSpace(watermark.PricingRevision) == "" {
		return migration, false, errors.New("mysql derived rebuild watermark is incomplete")
	}

	for _, table := range tables {
		progress, exists, progressErr := repository.TableProgress(ctx, migration.ID, table)
		if progressErr != nil {
			return migration, false, progressErr
		}
		if !exists {
			next, _, initializeErr := repository.InitializeTableProgress(
				ctx, migration.ID, table, migration.Generation, firstProgress.SourceWatermark, 1,
			)
			return next, false, initializeErr
		}
		if !jsonEqual(progress.SourceWatermark, firstProgress.SourceWatermark) {
			return migration, false, fmt.Errorf("derived table %s changed its rebuild watermark", table)
		}
		if progress.Completed {
			continue
		}
		rows, rebuildErr := rebuildDerivedTableWithRetry(ctx, executor, table, watermark)
		if rebuildErr != nil {
			return migration, false, fmt.Errorf("rebuild mysql derived table %s: %w", table, rebuildErr)
		}
		progress.Checkpoint = json.RawMessage("{\"completed\":true}")
		progress.RowsCopied = rows
		progress.Requests++
		progress.BatchSize = 1
		progress.Completed = true
		next, saveErr := repository.SaveTableProgress(ctx, migration.ID, migration.Generation, progress)
		return next, false, saveErr
	}

	next, err := repository.AdvanceMigration(ctx, migration.ID, migration.Generation, databasemigration.PhaseValidate)
	return next, err == nil, err
}

func jsonEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftJSON, _ := json.Marshal(leftValue)
	rightJSON, _ := json.Marshal(rightValue)
	return string(leftJSON) == string(rightJSON)
}

type mysqlDerivedExecutor struct {
	db  *sql.DB
	now func() time.Time
}

func newMySQLDerivedExecutor(db *sql.DB) mysqlDerivedExecutor {
	return mysqlDerivedExecutor{db: db, now: time.Now}
}

func (e mysqlDerivedExecutor) Tables() []string {
	steps := mysqlDerivedRebuildPlan(derivedRebuildWatermark{}, 1)
	tables := make([]string, len(steps))
	for index, step := range steps {
		tables[index] = step.Table
	}
	return tables
}

func (e mysqlDerivedExecutor) CaptureWatermark(ctx context.Context) (derivedRebuildWatermark, error) {
	if e.db == nil {
		return derivedRebuildWatermark{}, errors.New("mysql database is unavailable")
	}
	var watermark derivedRebuildWatermark
	if err := e.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM usage_events").Scan(&watermark.UsageEventID); err != nil {
		return derivedRebuildWatermark{}, fmt.Errorf("capture mysql usage event watermark: %w", err)
	}
	revision, err := usagepricing.StructureRevision(ctx, e.db)
	if err != nil {
		return derivedRebuildWatermark{}, fmt.Errorf("capture mysql pricing structure revision: %w", err)
	}
	watermark.PricingRevision = revision
	return watermark, nil
}

func (e mysqlDerivedExecutor) RebuildTable(ctx context.Context, table string, watermark derivedRebuildWatermark) (int64, error) {
	if e.db == nil {
		return 0, errors.New("mysql database is unavailable")
	}
	now := e.now
	if now == nil {
		now = time.Now
	}
	var selected *mysqlDerivedRebuildStep
	for _, candidate := range mysqlDerivedRebuildPlan(watermark, now().UnixMilli()) {
		if candidate.Table == table {
			copy := candidate
			selected = &copy
			break
		}
	}
	if selected == nil {
		return 0, fmt.Errorf("unknown mysql derived table %q", table)
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, statement := range selected.Statements {
		if _, err := tx.ExecContext(ctx, statement.Query, statement.Args...); err != nil {
			return 0, err
		}
	}
	var rows int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+mysqlQuote(table)).Scan(&rows); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return rows, nil
}

type mysqlDerivedStatement struct {
	Query string
	Args  []any
}

type mysqlDerivedRebuildStep struct {
	Table      string
	Statements []mysqlDerivedStatement
}

func mysqlDerivedStep(table, insert string, args ...any) mysqlDerivedRebuildStep {
	statements := []mysqlDerivedStatement{{Query: "DELETE FROM " + mysqlQuote(table)}}
	if strings.TrimSpace(insert) != "" {
		statements = append(statements, mysqlDerivedStatement{Query: insert, Args: args})
	}
	return mysqlDerivedRebuildStep{Table: table, Statements: statements}
}

func mysqlDerivedRebuildPlan(watermark derivedRebuildWatermark, nowMS int64) []mysqlDerivedRebuildStep {
	return []mysqlDerivedRebuildStep{
		mysqlAccountModelRollupStep(watermark, nowMS),
		mysqlDashboardHourlyRollupStep(watermark, nowMS),
		mysqlIdentityLedgerStep(watermark, nowMS),
		mysqlHourlyAggregateStep(watermark, nowMS),
		mysqlHourlyAggregateStateStep(watermark, nowMS),
		mysqlMonitoringAccountDailyStep(watermark, nowMS),
		mysqlMonitoringAPIKeyDailyStep(watermark, nowMS),
		mysqlMonitoringProjectionStep(watermark, nowMS),
		mysqlMonitoringSearchStep(watermark),
		mysqlMonitoringHeaderStep(watermark, nowMS),
		mysqlMonitoringSelectorStep(watermark, nowMS),
		mysqlCodexLegacyIdentityEvidenceStep(watermark),
		mysqlMonitoringStateStep(watermark, nowMS),
		mysqlMonitoringSearchStateStep(nowMS),
		mysqlPricingAccountStep(watermark, nowMS),
		mysqlPricingHourlyStep(watermark, nowMS),
		mysqlPricingStateStep(watermark, nowMS),
		mysqlRollupCheckpointsStep(watermark, nowMS),
		mysqlDerivedStep("usage_rollup_rebuild_state", ""),
	}
}

func mysqlAnalyticsModelExpression(prefix string) string {
	value := "COALESCE(NULLIF(" + prefix + "requested_model, ''), " + prefix + "model, '')"
	suffix := "SUBSTRING_INDEX(LEFT(" + value + ",CHAR_LENGTH(" + value + ")-1),'(',-1)"
	known := "LOWER(" + suffix + ") IN ('none','auto','-1','minimal','low','medium','high','xhigh','max')"
	numeric := "(REGEXP_LIKE(" + suffix + ",'^([+]?[0-9]+|-0+)$') AND " +
		"CAST(" + suffix + " AS UNSIGNED)<=9223372036854775807)"
	return "CASE WHEN REGEXP_LIKE(" + value + ",'^.+[(][^()]+[)]$') AND (" + known + " OR " + numeric + ") " +
		"THEN LEFT(" + value + ",CHAR_LENGTH(" + value + ")-CHAR_LENGTH(" + suffix + ")-2) ELSE " + value + " END"
}

func mysqlCompatibleCachedExpression(prefix string) string {
	return "GREATEST(GREATEST(COALESCE(" + prefix + "cached_tokens,0),COALESCE(" + prefix + "cache_tokens,0))-" +
		"GREATEST(COALESCE(" + prefix + "cache_read_tokens,0),0)-GREATEST(COALESCE(" + prefix + "cache_creation_tokens,0),0),0)"
}

// mysqlGroupHash is an additional GROUP BY discriminator, not a replacement
// for the complete source values. It prevents MySQL's PAD SPACE collation and
// max_sort_length prefix comparison from merging distinct unbounded keys.
func mysqlGroupHash(expressions ...string) string {
	return "UNHEX(SHA2(CAST(JSON_ARRAY(" + strings.Join(expressions, ",") +
		") AS CHAR CHARACTER SET utf8mb4),256))"
}

func mysqlAccountKeyExpression(prefix string) string {
	column := func(name string) string { return "TRIM(COALESCE(" + prefix + name + ",''))" }
	authFileSnapshot := column("auth_file_snapshot")
	authIndex := column("auth_index")
	source := column("source")
	account := column("account_snapshot")
	label := column("auth_label_snapshot")
	project := column("auth_project_id_snapshot")
	providerSource := "COALESCE(NULLIF(" + column("auth_provider_snapshot") + ",'')," + column("provider") + ",'')"
	provider := "CASE LOWER(REPLACE(TRIM(" + providerSource + "),'_','-')) WHEN 'x-ai' THEN 'xai' WHEN 'grok' THEN 'xai' ELSE LOWER(REPLACE(TRIM(" + providerSource + "),'_','-')) END"
	authFile := "CASE WHEN " + authFileSnapshot + "<>'' THEN " + authFileSnapshot + " WHEN " + source + "<>'' AND " + source + "<>" + account + " AND " + source + "<>" + label + " THEN " + source + " ELSE '' END"
	key := func(kind string, values ...string) string {
		parts := []string{"'usage-account-history:" + usageidentity.FormatVersion + ":" + kind + ":'"}
		for index, value := range values {
			if index > 0 {
				parts = append(parts, "':'")
			}
			parts = append(parts, "HEX("+value+")")
		}
		return "CONCAT(" + strings.Join(parts, ",") + ")"
	}
	return "CASE " +
		"WHEN " + authFile + "<>'' AND " + authIndex + "<>'' THEN " + key("file-index", authFile, authIndex) + " " +
		"WHEN " + authFile + "<>'' AND " + project + "<>'' THEN " + key("file-project", authFile, provider, project) + " " +
		"WHEN " + authFile + "<>'' AND " + account + "<>'' THEN " + key("file-account", authFile, provider, account) + " " +
		"WHEN " + authFile + "<>'' AND " + label + "<>'' THEN " + key("file-label", authFile, provider, label) + " " +
		"WHEN " + authFile + "<>'' THEN " + key("file", authFile, provider) + " " +
		"WHEN " + authIndex + "<>'' THEN " + key("auth-index", provider, authIndex) + " " +
		"WHEN " + project + "<>'' THEN " + key("project", provider, project) + " " +
		"WHEN " + account + "<>'' THEN " + key("account", provider, account) + " " +
		"WHEN " + label + "<>'' THEN " + key("label", provider, label) + " ELSE '' END"
}

func mysqlSearchTextExpression(prefix string) string {
	analytics := mysqlAnalyticsModelExpression(prefix)
	columns := []string{
		"request_id", "event_hash", "model", "requested_model", "@analytics_model", "resolved_model",
		"endpoint", "method", "path", "client_ip", "x_forwarded_for", "user_agent", "source",
		"source_hash", "api_key_hash", "auth_index", "account_snapshot", "auth_label_snapshot",
		"auth_file_snapshot", "auth_provider_snapshot", "auth_project_id_snapshot", "reasoning_effort",
		"service_tier", "executor_type", "fail_summary", "header_quota_plan_type", "header_error_kind",
		"header_error_code", "header_trace_id",
	}
	parts := make([]string, 0, len(columns))
	for _, column := range columns {
		expression := prefix + column
		if column == "@analytics_model" {
			expression = analytics
		}
		parts = append(parts, "COALESCE("+expression+",'')")
	}
	return "LOWER(CONCAT_WS(CHAR(31 USING utf8mb4)," + strings.Join(parts, ",") + "))"
}

func mysqlSnapshotKeyExpression(prefix string) string {
	authFile := "COALESCE(" + prefix + "auth_file_snapshot,'')"
	authIndex := "COALESCE(" + prefix + "auth_index,'')"
	account := "COALESCE(" + prefix + "account_snapshot,'')"
	sourceHash := "COALESCE(" + prefix + "source_hash,'')"
	eventHash := prefix + "event_hash"
	return "CASE WHEN " + authFile + "<>'' AND " + authIndex + "<>'' THEN CONCAT(" + authFile + ",'::'," + authIndex + ") " +
		"WHEN " + authFile + "<>'' THEN CONCAT('file::'," + authFile + ") " +
		"WHEN " + authIndex + "<>'' THEN CONCAT('auth::'," + authIndex + ") " +
		"WHEN " + account + "<>'' THEN CONCAT('account::',LOWER(" + account + ")) " +
		"WHEN " + sourceHash + "<>'' THEN CONCAT('source::'," + sourceHash + ") ELSE CONCAT('event::'," + eventHash + ") END"
}

// mysqlDerivedSourceProjection deliberately excludes large authority-only
// payloads such as raw_json and fail_body. Carrying e.* through GROUP BY and
// window-function CTEs makes MySQL materialize and sort hundreds of megabytes
// of text that no derived table reads, which can make a resumed rebuild appear
// stalled for many minutes.
func mysqlDerivedSourceProjection(prefix string, includeID bool) string {
	columns := []string{
		"timestamp_ms", "requested_model", "model", "resolved_model",
		"normalized_total_input_tokens", "input_tokens", "output_tokens", "reasoning_tokens",
		"cached_tokens", "cache_tokens", "cache_read_tokens", "cache_creation_tokens", "total_tokens",
		"latency_ms", "failed", "service_tier", "account_snapshot", "auth_label_snapshot", "provider",
		"auth_provider_snapshot", "auth_account_id_snapshot", "auth_index", "source", "source_hash",
		"auth_file_snapshot", "auth_project_id_snapshot", "api_key_hash", "executor_type",
	}
	if includeID {
		columns = append([]string{"id"}, columns...)
	}
	qualified := make([]string, len(columns))
	for index, column := range columns {
		qualified[index] = prefix + column
	}
	return strings.Join(qualified, ",")
}

func mysqlBandedEventsCTE(watermark int64, includeAccount bool) (string, []any) {
	requested := "COALESCE(NULLIF(e.requested_model,''),e.model,'')"
	analytics := mysqlAnalyticsModelExpression("e.")
	account := ""
	if includeAccount {
		account = "," + mysqlAccountKeyExpression("e.") + " AS account_key_value"
	}
	query := "WITH base_events AS (\n" +
		"\tSELECT " + mysqlDerivedSourceProjection("e.", false) + "," +
		requested + " AS requested_model_value," + analytics + " AS analytics_model_value," +
		"COALESCE(NULLIF(e.resolved_model,'')," + analytics + ") AS billing_model_value," +
		"COALESCE(e.normalized_total_input_tokens,e.input_tokens,0) AS normalized_input_tokens_value," +
		mysqlCompatibleCachedExpression("e.") + " AS compatible_cached_tokens_value" + account +
		" FROM usage_events e WHERE e.id<=?\n), priced_events AS (\n" +
		"\tSELECT base_events.*,CASE WHEN billing_price.model IS NOT NULL THEN billing_model_value " +
		"WHEN analytics_price.model IS NOT NULL THEN analytics_model_value " +
		"WHEN display_price.model IS NOT NULL THEN requested_model_value ELSE billing_model_value END AS pricing_model_value " +
		"FROM base_events LEFT JOIN model_prices billing_price ON billing_price.model=base_events.billing_model_value " +
		"LEFT JOIN model_prices analytics_price ON analytics_price.model=base_events.analytics_model_value " +
		"LEFT JOIN model_prices display_price ON display_price.model=base_events.requested_model_value\n), banded_events AS (\n" +
		"\tSELECT priced_events.*,COALESCE((SELECT MAX(tier.threshold_tokens) FROM model_price_context_tiers tier " +
		"WHERE tier.model=priced_events.pricing_model_value AND priced_events.normalized_input_tokens_value>tier.threshold_tokens),?) " +
		"AS context_threshold_tokens_value FROM priced_events\n) "
	return query, []any{watermark, model.ModelPriceBaseContextThreshold}
}

func mysqlAccountModelRollupStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	analytics := mysqlAnalyticsModelExpression("e.")
	accountKey := mysqlAccountKeyExpression("e.")
	compatibleCached := mysqlCompatibleCachedExpression("e.")
	groupHash := mysqlGroupHash(
		"account_key_value", "model_value", "billing_model_value", "TRIM(COALESCE(service_tier,''))",
	)
	query := `INSERT INTO usage_account_model_rollups (
		account_key,account_snapshot,auth_label_snapshot,auth_provider_snapshot,auth_index,
		source,source_hash,model,billing_model,service_tier,calls,success_calls,failure_calls,
		input_tokens,output_tokens,reasoning_tokens,cached_tokens,cache_read_tokens,
		cache_creation_tokens,long_input_tokens,long_output_tokens,long_cached_tokens,
		long_cache_read_tokens,long_cache_creation_tokens,total_tokens,first_seen_ms,last_seen_ms,updated_at_ms
	)
	WITH base AS (
		SELECT ` + mysqlDerivedSourceProjection("e.", true) + `,` + accountKey + ` AS account_key_value,
			` + analytics + ` AS analytics_model_value,
			COALESCE(NULLIF(e.resolved_model,''),` + analytics + `) AS raw_billing_model_value,
			COALESCE(e.normalized_total_input_tokens,e.input_tokens,0) AS normalized_input_tokens_value,
			` + compatibleCached + ` AS compatible_cached_tokens_value
		FROM usage_events e WHERE e.id<=?
	), normalized AS (
		SELECT base.*,
			CASE WHEN raw_billing_model_value='' THEN '-' ELSE raw_billing_model_value END AS billing_model_value,
			CASE WHEN analytics_model_value='' THEN
				CASE WHEN raw_billing_model_value='' THEN '-' ELSE raw_billing_model_value END
				ELSE analytics_model_value END AS model_value
		FROM base
	), scored AS (
		SELECT normalized.*,` + groupHash + ` AS group_key FROM normalized
	), annotated AS (
		SELECT scored.*,
			MIN(CASE WHEN COALESCE(account_snapshot,'')<>'' THEN id END)
				OVER (PARTITION BY scored.group_key) AS first_account_id,
			MIN(CASE WHEN COALESCE(auth_label_snapshot,'')<>'' THEN id END)
				OVER (PARTITION BY scored.group_key) AS first_label_id,
			MIN(CASE WHEN COALESCE(NULLIF(auth_provider_snapshot,''),provider,'')<>'' THEN id END)
				OVER (PARTITION BY scored.group_key) AS first_provider_id,
			MIN(CASE WHEN COALESCE(auth_index,'')<>'' THEN id END)
				OVER (PARTITION BY scored.group_key) AS first_auth_index_id,
			MIN(CASE WHEN COALESCE(source,'')<>'' THEN id END)
				OVER (PARTITION BY scored.group_key) AS first_source_id,
			MIN(CASE WHEN COALESCE(source_hash,'')<>'' THEN id END)
				OVER (PARTITION BY scored.group_key) AS first_source_hash_id
		FROM scored
	)
	SELECT account_key_value,
		MAX(CASE WHEN id=first_account_id THEN NULLIF(account_snapshot,'') END),
		MAX(CASE WHEN id=first_label_id THEN NULLIF(auth_label_snapshot,'') END),
		MAX(CASE WHEN id=first_provider_id
			THEN NULLIF(COALESCE(NULLIF(auth_provider_snapshot,''),provider,''),'') END),
		MAX(CASE WHEN id=first_auth_index_id THEN NULLIF(auth_index,'') END),
		MAX(CASE WHEN id=first_source_id THEN NULLIF(source,'') END),
		MAX(CASE WHEN id=first_source_hash_id THEN NULLIF(source_hash,'') END),
		model_value,billing_model_value,TRIM(COALESCE(service_tier,'')),COUNT(*),
		COALESCE(SUM(CASE WHEN failed=0 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN failed=1 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(normalized_input_tokens_value),0),COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(reasoning_tokens),0),COALESCE(SUM(compatible_cached_tokens_value),0),
		COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(cache_creation_tokens),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN normalized_input_tokens_value ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN output_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN compatible_cached_tokens_value ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN cache_read_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN cache_creation_tokens ELSE 0 END),0),
		COALESCE(SUM(total_tokens),0),MIN(timestamp_ms),MAX(timestamp_ms),?
	FROM annotated WHERE account_key_value<>''
	GROUP BY account_key_value,model_value,billing_model_value,TRIM(COALESCE(service_tier,'')),group_key`
	return mysqlDerivedStep("usage_account_model_rollups", query,
		watermark.UsageEventID,
		mysqlLongContextThreshold, mysqlLongContextThreshold, mysqlLongContextThreshold,
		mysqlLongContextThreshold, mysqlLongContextThreshold, nowMS,
	)
}

func mysqlDashboardHourlyRollupStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	analytics := mysqlAnalyticsModelExpression("e.")
	compatibleCached := mysqlCompatibleCachedExpression("e.")
	bucket := fmt.Sprintf("timestamp_ms-(timestamp_ms MOD %d)", mysqlDerivedHourMS)
	groupHash := mysqlGroupHash(
		bucket,
		"model_value", "billing_model_value", "COALESCE(service_tier,'')",
	)
	query := `INSERT INTO usage_dashboard_hourly_rollups (
		bucket_ms,model,billing_model,service_tier,calls,success_calls,failure_calls,
		input_tokens,output_tokens,reasoning_tokens,cached_tokens,cache_read_tokens,
		cache_creation_tokens,long_input_tokens,long_output_tokens,long_cached_tokens,
		long_cache_read_tokens,long_cache_creation_tokens,total_tokens,latency_sum_ms,
		latency_samples,zero_token_calls,updated_at_ms
	)
	WITH base AS (
		SELECT e.*,` + analytics + ` AS model_value,
			COALESCE(NULLIF(e.resolved_model,''),` + analytics + `) AS billing_model_value,
			COALESCE(e.normalized_total_input_tokens,e.input_tokens,0) AS normalized_input_tokens_value,
			` + compatibleCached + ` AS compatible_cached_tokens_value
		FROM usage_events e WHERE e.id<=?
	)
	SELECT ` + bucket + `,model_value,billing_model_value,COALESCE(service_tier,''),
		COUNT(*),COALESCE(SUM(CASE WHEN failed=0 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN failed=1 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(normalized_input_tokens_value),0),COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(reasoning_tokens),0),COALESCE(SUM(compatible_cached_tokens_value),0),
		COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(cache_creation_tokens),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN normalized_input_tokens_value ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN output_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN compatible_cached_tokens_value ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN cache_read_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN cache_creation_tokens ELSE 0 END),0),
		COALESCE(SUM(total_tokens),0),
		COALESCE(SUM(CASE WHEN latency_ms IS NOT NULL AND latency_ms<>0 THEN latency_ms ELSE 0 END),0),
		COUNT(NULLIF(latency_ms,0)),
		COALESCE(SUM(CASE WHEN total_tokens=0 AND failed=0 THEN 1 ELSE 0 END),0),?
	FROM base
	GROUP BY ` + bucket + `,model_value,billing_model_value,COALESCE(service_tier,''),` + groupHash
	return mysqlDerivedStep("usage_dashboard_hourly_rollups", query,
		watermark.UsageEventID,
		mysqlLongContextThreshold, mysqlLongContextThreshold, mysqlLongContextThreshold,
		mysqlLongContextThreshold, mysqlLongContextThreshold, nowMS,
	)
}

func mysqlIdentityLedgerStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	query := `INSERT INTO usage_event_identity_ledger (
		event_hash,raw_event_id,timestamp_ms,bucket_ms,aggregate_schema_version,
		aggregate_structure_revision,first_seen_at_ms,updated_at_ms
	)
	SELECT event_hash,id,timestamp_ms,timestamp_ms-(timestamp_ms MOD ?),3,?,
		CASE WHEN created_at_ms>0 THEN created_at_ms ELSE ? END,?
	FROM usage_events WHERE id<=?`
	return mysqlDerivedStep("usage_event_identity_ledger", query,
		mysqlDerivedHourMS, mysqlDerivedAggregateRevision, nowMS, nowMS, watermark.UsageEventID,
	)
}

func mysqlHourlyAggregateStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	analytics := mysqlAnalyticsModelExpression("e.")
	compatibleCached := mysqlCompatibleCachedExpression("e.")
	bucket := fmt.Sprintf("timestamp_ms-(timestamp_ms MOD %d)", mysqlDerivedHourMS)
	groupHash := mysqlGroupHash(
		bucket,
		"model_value", "billing_model_value", "COALESCE(service_tier,'')", "failed",
	)
	query := `INSERT INTO usage_hourly_aggregate_v1 (
		bucket_ms,model,billing_model,service_tier,failed,calls,input_tokens,output_tokens,
		reasoning_tokens,cached_tokens,cache_read_tokens,cache_creation_tokens,long_input_tokens,
		long_output_tokens,long_cached_tokens,long_cache_read_tokens,long_cache_creation_tokens,
		total_tokens,latency_sum_ms,latency_samples,zero_token_calls,updated_at_ms
	)
	WITH base AS (
		SELECT e.*,` + analytics + ` AS model_value,
			COALESCE(NULLIF(e.resolved_model,''),` + analytics + `) AS billing_model_value,
			COALESCE(e.normalized_total_input_tokens,e.input_tokens,0) AS normalized_input_tokens_value,
			` + compatibleCached + ` AS compatible_cached_tokens_value
		FROM usage_events e WHERE e.id<=?
	)
	SELECT ` + bucket + `,model_value,billing_model_value,COALESCE(service_tier,''),
		failed,COUNT(*),COALESCE(SUM(normalized_input_tokens_value),0),COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(reasoning_tokens),0),COALESCE(SUM(compatible_cached_tokens_value),0),
		COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(cache_creation_tokens),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN normalized_input_tokens_value ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN output_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN compatible_cached_tokens_value ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN cache_read_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN cache_creation_tokens ELSE 0 END),0),
		COALESCE(SUM(total_tokens),0),
		COALESCE(SUM(CASE WHEN latency_ms IS NOT NULL AND latency_ms<>0 THEN latency_ms ELSE 0 END),0),
		COUNT(NULLIF(latency_ms,0)),
		COALESCE(SUM(CASE WHEN total_tokens=0 AND failed=0 THEN 1 ELSE 0 END),0),?
	FROM base
	GROUP BY ` + bucket + `,model_value,billing_model_value,COALESCE(service_tier,''),failed,` + groupHash
	return mysqlDerivedStep("usage_hourly_aggregate_v1", query,
		watermark.UsageEventID,
		mysqlLongContextThreshold, mysqlLongContextThreshold, mysqlLongContextThreshold,
		mysqlLongContextThreshold, mysqlLongContextThreshold, nowMS,
	)
}

func mysqlHourlyAggregateStateStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	query := `INSERT INTO usage_hourly_aggregate_state (
		aggregate_name,schema_version,structure_revision,status,backfill_last_event_id,
		coverage_event_id,target_event_id,processed_events,min_bucket_ms,max_bucket_ms,
		last_run_started_at_ms,updated_at_ms,finished_at_ms,last_error
	)
	SELECT 'hourly_core',3,?,'ready',?,?,?,COUNT(*),
		MIN(timestamp_ms-(timestamp_ms MOD ?)),MAX(timestamp_ms-(timestamp_ms MOD ?)),?,?,?,NULL
	FROM usage_events WHERE id<=?`
	return mysqlDerivedStep("usage_hourly_aggregate_state", query,
		mysqlDerivedAggregateRevision,
		watermark.UsageEventID, watermark.UsageEventID, watermark.UsageEventID,
		mysqlDerivedHourMS, mysqlDerivedHourMS, nowMS, nowMS, nowMS, watermark.UsageEventID,
	)
}

func mysqlMonitoringAccountDailyStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	cte, args := mysqlBandedEventsCTE(watermark.UsageEventID, false)
	bucket := fmt.Sprintf("timestamp_ms-(timestamp_ms MOD %d)", mysqlDerivedDayMS)
	groupHash := mysqlGroupHash(
		bucket,
		"COALESCE(account_snapshot,'')", "COALESCE(auth_label_snapshot,'')",
		"COALESCE(provider,'')", "COALESCE(auth_provider_snapshot,'')",
		"COALESCE(auth_account_id_snapshot,'')",
		"COALESCE(auth_index,'')", "COALESCE(source,'')", "COALESCE(source_hash,'')",
		"COALESCE(auth_file_snapshot,'')", "COALESCE(api_key_hash,'')",
		"COALESCE(executor_type,'')", "analytics_model_value", "billing_model_value",
		"pricing_model_value", "COALESCE(service_tier,'')", "context_threshold_tokens_value", "failed",
	)
	query := `INSERT INTO usage_monitoring_account_daily_rollups_v1 (
		structure_revision,bucket_ms,account_snapshot,auth_label_snapshot,provider,
		auth_provider_snapshot,auth_account_id_snapshot,auth_index,source,source_hash,auth_file_snapshot,api_key_hash,
		executor_type,model,billing_model,pricing_model,service_tier,context_threshold_tokens,
		failed,calls,input_tokens,output_tokens,reasoning_tokens,cached_tokens,cache_read_tokens,
		cache_creation_tokens,long_input_tokens,long_output_tokens,long_cached_tokens,
		long_cache_read_tokens,long_cache_creation_tokens,total_tokens,zero_token_calls,
		latency_sum_ms,latency_samples,last_seen_ms,updated_at_ms
	)
	` + cte + `
	SELECT ?,` + bucket + `,COALESCE(account_snapshot,''),
		COALESCE(auth_label_snapshot,''),COALESCE(provider,''),COALESCE(auth_provider_snapshot,''),
		COALESCE(auth_account_id_snapshot,''),COALESCE(auth_index,''),COALESCE(source,''),COALESCE(source_hash,''),
		COALESCE(auth_file_snapshot,''),COALESCE(api_key_hash,''),COALESCE(executor_type,''),
		analytics_model_value,billing_model_value,pricing_model_value,COALESCE(service_tier,''),
		context_threshold_tokens_value,failed,COUNT(*),COALESCE(SUM(normalized_input_tokens_value),0),
		COALESCE(SUM(output_tokens),0),COALESCE(SUM(reasoning_tokens),0),
		COALESCE(SUM(compatible_cached_tokens_value),0),COALESCE(SUM(cache_read_tokens),0),
		COALESCE(SUM(cache_creation_tokens),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN normalized_input_tokens_value ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN output_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN compatible_cached_tokens_value ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN cache_read_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN cache_creation_tokens ELSE 0 END),0),
		COALESCE(SUM(total_tokens),0),
		COALESCE(SUM(CASE WHEN total_tokens=0 AND failed=0 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN latency_ms IS NOT NULL AND latency_ms<>0 THEN latency_ms ELSE 0 END),0),
		COUNT(NULLIF(latency_ms,0)),MAX(timestamp_ms),?
	FROM banded_events
	GROUP BY ` + bucket + `,COALESCE(account_snapshot,''),
		COALESCE(auth_label_snapshot,''),COALESCE(provider,''),COALESCE(auth_provider_snapshot,''),
		COALESCE(auth_account_id_snapshot,''),COALESCE(auth_index,''),COALESCE(source,''),COALESCE(source_hash,''),
		COALESCE(auth_file_snapshot,''),COALESCE(api_key_hash,''),COALESCE(executor_type,''),
		analytics_model_value,billing_model_value,pricing_model_value,COALESCE(service_tier,''),
		context_threshold_tokens_value,failed,` + groupHash
	args = append(args, watermark.PricingRevision,
		mysqlLongContextThreshold, mysqlLongContextThreshold, mysqlLongContextThreshold,
		mysqlLongContextThreshold, mysqlLongContextThreshold, nowMS)
	return mysqlDerivedStep("usage_monitoring_account_daily_rollups_v1", query, args...)
}

func mysqlMonitoringAPIKeyDailyStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	cte, args := mysqlBandedEventsCTE(watermark.UsageEventID, false)
	bucket := fmt.Sprintf("timestamp_ms-(timestamp_ms MOD %d)", mysqlDerivedDayMS)
	groupHash := mysqlGroupHash(
		bucket,
		"COALESCE(api_key_hash,'')", "COALESCE(account_snapshot,'')",
		"COALESCE(auth_label_snapshot,'')", "COALESCE(provider,'')",
		"COALESCE(auth_provider_snapshot,'')", "COALESCE(auth_account_id_snapshot,'')", "COALESCE(auth_index,'')",
		"COALESCE(source,'')", "COALESCE(source_hash,'')", "COALESCE(auth_file_snapshot,'')",
		"COALESCE(executor_type,'')", "analytics_model_value", "billing_model_value",
		"pricing_model_value", "COALESCE(service_tier,'')", "context_threshold_tokens_value", "failed",
	)
	query := `INSERT INTO usage_monitoring_api_key_daily_rollups_v1 (
		structure_revision,bucket_ms,api_key_hash,account_snapshot,auth_label_snapshot,provider,
		auth_provider_snapshot,auth_account_id_snapshot,auth_index,source,source_hash,auth_file_snapshot,executor_type,
		model,billing_model,pricing_model,service_tier,context_threshold_tokens,failed,calls,
		input_tokens,output_tokens,reasoning_tokens,cached_tokens,cache_read_tokens,
		cache_creation_tokens,long_input_tokens,long_output_tokens,long_cached_tokens,
		long_cache_read_tokens,long_cache_creation_tokens,total_tokens,zero_token_calls,
		latency_sum_ms,latency_samples,last_seen_ms,updated_at_ms
	)
	` + cte + `
	SELECT ?,` + bucket + `,COALESCE(api_key_hash,''),
		COALESCE(account_snapshot,''),COALESCE(auth_label_snapshot,''),COALESCE(provider,''),
		COALESCE(auth_provider_snapshot,''),COALESCE(auth_account_id_snapshot,''),COALESCE(auth_index,''),COALESCE(source,''),
		COALESCE(source_hash,''),COALESCE(auth_file_snapshot,''),COALESCE(executor_type,''),
		analytics_model_value,billing_model_value,pricing_model_value,COALESCE(service_tier,''),
		context_threshold_tokens_value,failed,COUNT(*),COALESCE(SUM(normalized_input_tokens_value),0),
		COALESCE(SUM(output_tokens),0),COALESCE(SUM(reasoning_tokens),0),
		COALESCE(SUM(compatible_cached_tokens_value),0),COALESCE(SUM(cache_read_tokens),0),
		COALESCE(SUM(cache_creation_tokens),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN normalized_input_tokens_value ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN output_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN compatible_cached_tokens_value ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN cache_read_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN cache_creation_tokens ELSE 0 END),0),
		COALESCE(SUM(total_tokens),0),
		COALESCE(SUM(CASE WHEN total_tokens=0 AND failed=0 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN latency_ms IS NOT NULL AND latency_ms<>0 THEN latency_ms ELSE 0 END),0),
		COUNT(NULLIF(latency_ms,0)),MAX(timestamp_ms),?
	FROM banded_events
	GROUP BY ` + bucket + `,COALESCE(api_key_hash,''),
		COALESCE(account_snapshot,''),COALESCE(auth_label_snapshot,''),COALESCE(provider,''),
		COALESCE(auth_provider_snapshot,''),COALESCE(auth_account_id_snapshot,''),COALESCE(auth_index,''),COALESCE(source,''),
		COALESCE(source_hash,''),COALESCE(auth_file_snapshot,''),COALESCE(executor_type,''),
		analytics_model_value,billing_model_value,pricing_model_value,COALESCE(service_tier,''),
		context_threshold_tokens_value,failed,` + groupHash
	args = append(args, watermark.PricingRevision,
		mysqlLongContextThreshold, mysqlLongContextThreshold, mysqlLongContextThreshold,
		mysqlLongContextThreshold, mysqlLongContextThreshold, nowMS)
	return mysqlDerivedStep("usage_monitoring_api_key_daily_rollups_v1", query, args...)
}

func mysqlMonitoringProjectionStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	analytics := mysqlAnalyticsModelExpression("e.")
	query := `INSERT INTO usage_monitoring_event_projection_v1 (
		event_id,timestamp_ms,search_text,account_key,provider,executor_type,model,requested_model,
		analytics_model,resolved_model,auth_index,source,source_hash,api_key_hash,account_snapshot,
		auth_label_snapshot,auth_file_snapshot,auth_provider_snapshot,auth_account_id_snapshot,auth_project_id_snapshot,
		reasoning_effort,service_tier,failed,latency_ms,input_tokens,output_tokens,reasoning_tokens,
		cached_tokens,cache_tokens,cache_read_tokens,cache_creation_tokens,
		normalized_total_input_tokens,total_tokens,header_quota_plan_type,header_error_kind,
		header_error_code,header_trace_id,updated_at_ms
	)
	SELECT e.id,e.timestamp_ms,` + mysqlSearchTextExpression("e.") + `,
		` + mysqlAccountKeyExpression("e.") + `,COALESCE(e.provider,''),COALESCE(e.executor_type,''),
		COALESCE(e.model,''),COALESCE(NULLIF(e.requested_model,''),e.model,''),
		` + analytics + `,COALESCE(e.resolved_model,''),COALESCE(e.auth_index,''),
		COALESCE(e.source,''),COALESCE(e.source_hash,''),COALESCE(e.api_key_hash,''),
		COALESCE(e.account_snapshot,''),COALESCE(e.auth_label_snapshot,''),
		COALESCE(e.auth_file_snapshot,''),COALESCE(e.auth_provider_snapshot,''),
		COALESCE(e.auth_account_id_snapshot,''),COALESCE(e.auth_project_id_snapshot,''),COALESCE(e.reasoning_effort,''),
		COALESCE(e.service_tier,''),COALESCE(e.failed,0),e.latency_ms,
		COALESCE(e.input_tokens,0),COALESCE(e.output_tokens,0),COALESCE(e.reasoning_tokens,0),
		COALESCE(e.cached_tokens,0),COALESCE(e.cache_tokens,0),COALESCE(e.cache_read_tokens,0),
		COALESCE(e.cache_creation_tokens,0),COALESCE(e.normalized_total_input_tokens,e.input_tokens,0),
		COALESCE(e.total_tokens,0),COALESCE(e.header_quota_plan_type,''),
		COALESCE(e.header_error_kind,''),COALESCE(e.header_error_code,''),
		COALESCE(e.header_trace_id,''),?
	FROM usage_events e WHERE e.id<=?`
	return mysqlDerivedStep("usage_monitoring_event_projection_v1", query, nowMS, watermark.UsageEventID)
}

func mysqlMonitoringSearchStep(watermark derivedRebuildWatermark) mysqlDerivedRebuildStep {
	query := `INSERT INTO usage_monitoring_event_search_v1 (event_id,search_text)
		SELECT event_id,search_text FROM usage_monitoring_event_projection_v1 WHERE event_id<=?`
	return mysqlDerivedStep("usage_monitoring_event_search_v1", query, watermark.UsageEventID)
}

func mysqlMonitoringHeaderStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	snapshotKey := mysqlSnapshotKeyExpression("e.")
	query := `INSERT INTO usage_monitoring_header_latest_v1 (
		snapshot_key,event_id,event_hash,timestamp_ms,auth_file_snapshot,auth_index,
		account_snapshot,auth_label_snapshot,auth_provider_snapshot,auth_account_id_snapshot,auth_project_id_snapshot,
		source,source_hash,response_metadata_json,header_quota_recover_at_ms,
		header_quota_used_percent,header_quota_plan_type,header_error_kind,
		header_error_code,header_trace_id,updated_at_ms
	)
	WITH candidates AS (
		SELECT ` + snapshotKey + ` AS snapshot_key,e.id AS event_id,e.event_hash,e.timestamp_ms,
			COALESCE(e.auth_file_snapshot,'') AS auth_file_snapshot,
			COALESCE(e.auth_index,'') AS auth_index,COALESCE(e.account_snapshot,'') AS account_snapshot,
			COALESCE(e.auth_label_snapshot,'') AS auth_label_snapshot,
			COALESCE(NULLIF(e.auth_provider_snapshot,''),e.provider,'') AS auth_provider_snapshot,
			COALESCE(e.auth_account_id_snapshot,'') AS auth_account_id_snapshot,
			COALESCE(e.auth_project_id_snapshot,'') AS auth_project_id_snapshot,
			COALESCE(e.source,'') AS source,COALESCE(e.source_hash,'') AS source_hash,
			COALESCE(e.response_metadata_json,'') AS response_metadata_json,
			e.header_quota_recover_at_ms,e.header_quota_used_percent,
			COALESCE(e.header_quota_plan_type,'') AS header_quota_plan_type,
			COALESCE(e.header_error_kind,'') AS header_error_kind,
			COALESCE(e.header_error_code,'') AS header_error_code,
			COALESCE(e.header_trace_id,'') AS header_trace_id
		FROM usage_events e
		WHERE e.id<=?
			AND (COALESCE(e.response_metadata_json,'')<>'' OR e.header_quota_recover_at_ms IS NOT NULL
				OR e.header_quota_used_percent IS NOT NULL OR COALESCE(e.header_quota_plan_type,'')<>''
				OR COALESCE(e.header_error_kind,'')<>'' OR COALESCE(e.header_error_code,'')<>''
				OR COALESCE(e.header_trace_id,'')<>'')
			AND (COALESCE(e.auth_file_snapshot,'')<>'' OR COALESCE(e.auth_index,'')<>''
				OR COALESCE(e.account_snapshot,'')<>'' OR COALESCE(e.source_hash,'')<>'')
	), ranked AS (
		SELECT candidates.*,ROW_NUMBER() OVER (
			PARTITION BY candidates.snapshot_key
			ORDER BY candidates.timestamp_ms DESC,candidates.event_id DESC
		) AS row_rank FROM candidates
	)
	SELECT snapshot_key,event_id,event_hash,timestamp_ms,auth_file_snapshot,auth_index,
		account_snapshot,auth_label_snapshot,auth_provider_snapshot,auth_account_id_snapshot,auth_project_id_snapshot,
		source,source_hash,response_metadata_json,header_quota_recover_at_ms,
		header_quota_used_percent,header_quota_plan_type,header_error_kind,
		header_error_code,header_trace_id,?
	FROM ranked WHERE row_rank=1`
	return mysqlDerivedStep("usage_monitoring_header_latest_v1", query, watermark.UsageEventID, nowMS)
}

func mysqlMonitoringSelectorStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	analytics := mysqlAnalyticsModelExpression("e.")
	bucket := fmt.Sprintf("e.timestamp_ms-(e.timestamp_ms MOD %d)", mysqlDerivedDayMS)
	groupHash := mysqlGroupHash(
		bucket,
		analytics, "COALESCE(e.api_key_hash,'')",
		"COALESCE(NULLIF(e.auth_provider_snapshot,''),NULLIF(e.provider,''),'')",
		"COALESCE(e.auth_file_snapshot,'')", "COALESCE(e.account_snapshot,'')",
		"COALESCE(e.auth_label_snapshot,'')", "COALESCE(e.auth_index,'')", "COALESCE(e.source_hash,'')",
	)
	query := `INSERT INTO usage_monitoring_selector_daily_rollups_v1 (
		model_format_revision,bucket_ms,model,api_key_hash,provider,auth_file_snapshot,
		account_snapshot,auth_label_snapshot,auth_index,source,source_hash,updated_at_ms
	)
	SELECT ?,` + bucket + `,` + analytics + `,
		COALESCE(e.api_key_hash,''),COALESCE(NULLIF(e.auth_provider_snapshot,''),NULLIF(e.provider,''),''),
		COALESCE(e.auth_file_snapshot,''),COALESCE(e.account_snapshot,''),
		COALESCE(e.auth_label_snapshot,''),COALESCE(e.auth_index,''),
		COALESCE(MAX(e.source),''),COALESCE(e.source_hash,''),?
	FROM usage_events e WHERE e.id<=?
	GROUP BY ` + bucket + `,` + analytics + `,
		COALESCE(e.api_key_hash,''),COALESCE(NULLIF(e.auth_provider_snapshot,''),NULLIF(e.provider,''),''),
		COALESCE(e.auth_file_snapshot,''),COALESCE(e.account_snapshot,''),
		COALESCE(e.auth_label_snapshot,''),COALESCE(e.auth_index,''),COALESCE(e.source_hash,''),` + groupHash
	return mysqlDerivedStep("usage_monitoring_selector_daily_rollups_v1", query,
		usageidentity.ModelFormatVersion, nowMS, watermark.UsageEventID,
	)
}

func mysqlCodexLegacyIdentityEvidenceStep(watermark derivedRebuildWatermark) mysqlDerivedRebuildStep {
	identityColumns := []string{
		"COALESCE(e.provider,'')",
		"COALESCE(e.auth_provider_snapshot,'')",
		"COALESCE(e.auth_account_id_snapshot,'')",
		"COALESCE(e.auth_project_id_snapshot,'')",
		"COALESCE(e.account_snapshot,'')",
	}
	chronology := `CASE
		WHEN COALESCE(e.auth_snapshot_at_ms,0)>0 THEN e.auth_snapshot_at_ms
		WHEN COALESCE(e.created_at_ms,0)>0 THEN e.created_at_ms
		ELSE NULL
	END`
	physicalSources := []struct {
		kind       int
		expression string
		filter     string
	}{
		{0, "e.auth_file_snapshot", "e.auth_file_snapshot IS NOT NULL AND e.auth_file_snapshot<>''"},
		{1, "e.source", `e.auth_file_snapshot IS NULL
			AND (e.account_snapshot IS NULL OR LOWER(TRIM(e.source))<>LOWER(TRIM(e.account_snapshot)))
			AND (e.auth_label_snapshot IS NULL OR LOWER(TRIM(e.source))<>LOWER(TRIM(e.auth_label_snapshot)))`},
		{2, "e.source", `e.auth_file_snapshot=''
			AND (e.account_snapshot IS NULL OR LOWER(TRIM(e.source))<>LOWER(TRIM(e.account_snapshot)))
			AND (e.auth_label_snapshot IS NULL OR LOWER(TRIM(e.source))<>LOWER(TRIM(e.auth_label_snapshot)))`},
	}
	selects := make([]string, 0, len(physicalSources))
	args := make([]any, 0, len(physicalSources)*2)
	for _, physical := range physicalSources {
		physicalValue := "LOWER(" + physical.expression + ")"
		authIndexValue := "LOWER(e.auth_index)"
		groupValues := append([]string{physicalValue, authIndexValue}, identityColumns...)
		selects = append(selects, fmt.Sprintf(`SELECT ?,%d,%s,%s,%s,
		COALESCE(MIN(%s),0),COALESCE(MAX(%s),0),
		MAX(CASE WHEN COALESCE(e.auth_snapshot_at_ms,0)>0 OR COALESCE(e.created_at_ms,0)>0 THEN 0 ELSE 1 END)
	FROM usage_events e
	WHERE e.id<=? AND COALESCE(e.auth_index,'')<>'' AND COALESCE(%s,'')<>'' AND %s
	GROUP BY %s,%s`, physical.kind, physicalValue, authIndexValue,
			strings.Join(identityColumns, ","), chronology, chronology, physical.expression,
			physical.filter, strings.Join(groupValues, ","), mysqlGroupHash(groupValues...)))
		args = append(args, usageevent.CodexLegacyIdentityEvidenceRevision, watermark.UsageEventID)
	}
	query := `INSERT INTO ` + usageevent.CodexLegacyIdentityEvidenceTable + ` (
		structure_revision,physical_kind,physical_file,auth_index,
		provider,auth_provider_snapshot,auth_account_id_snapshot,
		auth_project_id_snapshot,account_snapshot,min_evidence_at_ms,
		max_evidence_at_ms,chronology_unknown
	)
	` + strings.Join(selects, "\nUNION ALL\n")
	return mysqlDerivedStep(usageevent.CodexLegacyIdentityEvidenceTable, query, args...)
}

func mysqlMonitoringStateStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	query := `INSERT INTO usage_monitoring_rollup_state (
		rollup_name,schema_version,structure_revision,status,backfill_last_event_id,
		coverage_event_id,target_event_id,processed_events,last_run_started_at_ms,
		updated_at_ms,finished_at_ms,last_error
	)
	SELECT 'stats_v1',1,?,'ready',?,?,?,COUNT(*),?,?,?,NULL
		FROM usage_events WHERE id<=?
	UNION ALL
	SELECT 'metadata_v1',1,?,'ready',?,?,?,COUNT(*),?,?,?,NULL
		FROM usage_events WHERE id<=?
	UNION ALL
	SELECT 'projection_v1',1,?,'ready',?,?,?,COUNT(*),?,?,?,NULL
		FROM usage_events WHERE id<=?
	UNION ALL
	SELECT ?,1,?,'ready',?,?,?,COUNT(*),?,?,?,NULL
		FROM usage_events WHERE id<=?`
	args := []any{
		watermark.PricingRevision,
		watermark.UsageEventID, watermark.UsageEventID, watermark.UsageEventID,
		nowMS, nowMS, nowMS, watermark.UsageEventID,
		usageidentity.ModelFormatVersion,
		watermark.UsageEventID, watermark.UsageEventID, watermark.UsageEventID,
		nowMS, nowMS, nowMS, watermark.UsageEventID,
		usageidentity.ModelFormatVersion,
		watermark.UsageEventID, watermark.UsageEventID, watermark.UsageEventID,
		nowMS, nowMS, nowMS, watermark.UsageEventID,
		usageevent.CodexLegacyIdentityRollupName, usageevent.CodexLegacyIdentityEvidenceRevision,
		watermark.UsageEventID, watermark.UsageEventID, watermark.UsageEventID,
		nowMS, nowMS, nowMS, watermark.UsageEventID,
	}
	return mysqlDerivedStep("usage_monitoring_rollup_state", query, args...)
}

func mysqlMonitoringSearchStateStep(nowMS int64) mysqlDerivedRebuildStep {
	// The projection and MySQL FULLTEXT table are populated, but MySQL's
	// tokenizer/LOWER semantics have not passed the SQLite FTS5 trigram
	// conformance suite yet. Keep the semantic readiness bit closed.
	query := "INSERT INTO usage_monitoring_search_index_state (id,ready,updated_at_ms) VALUES (1,0,?)"
	return mysqlDerivedStep("usage_monitoring_search_index_state", query, nowMS)
}

func mysqlPricingAccountStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	cte, args := mysqlBandedEventsCTE(watermark.UsageEventID, true)
	groupHash := mysqlGroupHash(
		"account_key_value", "analytics_model_value", "billing_model_value", "pricing_model_value",
		"COALESCE(service_tier,'')", "context_threshold_tokens_value",
	)
	query := `INSERT INTO usage_pricing_account_rollups_v1 (
		structure_revision,account_key,account_snapshot,auth_label_snapshot,
		auth_provider_snapshot,auth_index,source,source_hash,model,billing_model,
		pricing_model,service_tier,context_threshold_tokens,calls,success_calls,
		failure_calls,input_tokens,output_tokens,reasoning_tokens,cached_tokens,
		cache_read_tokens,cache_creation_tokens,long_input_tokens,long_output_tokens,
		long_cached_tokens,long_cache_read_tokens,long_cache_creation_tokens,total_tokens,
		first_seen_ms,last_seen_ms,updated_at_ms
	)
	` + cte + `
	SELECT ?,account_key_value,MAX(NULLIF(account_snapshot,'')),MAX(NULLIF(auth_label_snapshot,'')),
		MAX(NULLIF(COALESCE(NULLIF(auth_provider_snapshot,''),provider,''),'')),
		MAX(NULLIF(auth_index,'')),MAX(NULLIF(source,'')),MAX(NULLIF(source_hash,'')),
		MIN(analytics_model_value),billing_model_value,pricing_model_value,COALESCE(service_tier,''),
		context_threshold_tokens_value,COUNT(*),
		COALESCE(SUM(CASE WHEN failed=0 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN failed=1 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(normalized_input_tokens_value),0),COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(reasoning_tokens),0),COALESCE(SUM(compatible_cached_tokens_value),0),
		COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(cache_creation_tokens),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN normalized_input_tokens_value ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN output_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN compatible_cached_tokens_value ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN cache_read_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN cache_creation_tokens ELSE 0 END),0),
		COALESCE(SUM(total_tokens),0),MIN(timestamp_ms),MAX(timestamp_ms),?
	FROM banded_events WHERE account_key_value<>''
	GROUP BY account_key_value,analytics_model_value,billing_model_value,pricing_model_value,
		COALESCE(service_tier,''),context_threshold_tokens_value,` + groupHash
	args = append(args, watermark.PricingRevision,
		mysqlLongContextThreshold, mysqlLongContextThreshold, mysqlLongContextThreshold,
		mysqlLongContextThreshold, mysqlLongContextThreshold, nowMS)
	return mysqlDerivedStep("usage_pricing_account_rollups_v1", query, args...)
}

func mysqlPricingHourlyStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	cte, args := mysqlBandedEventsCTE(watermark.UsageEventID, false)
	bucket := fmt.Sprintf("timestamp_ms-(timestamp_ms MOD %d)", mysqlDerivedHourMS)
	groupHash := mysqlGroupHash(
		bucket,
		"analytics_model_value", "billing_model_value", "pricing_model_value",
		"COALESCE(service_tier,'')", "context_threshold_tokens_value", "failed",
	)
	query := `INSERT INTO usage_pricing_hourly_rollups_v1 (
		structure_revision,bucket_ms,model,billing_model,pricing_model,service_tier,
		context_threshold_tokens,failed,calls,input_tokens,output_tokens,reasoning_tokens,
		cached_tokens,cache_read_tokens,cache_creation_tokens,long_input_tokens,
		long_output_tokens,long_cached_tokens,long_cache_read_tokens,long_cache_creation_tokens,
		total_tokens,latency_sum_ms,latency_samples,zero_token_calls,updated_at_ms
	)
	` + cte + `
	SELECT ?,` + bucket + `,analytics_model_value,billing_model_value,
		pricing_model_value,COALESCE(service_tier,''),context_threshold_tokens_value,failed,
		COUNT(*),COALESCE(SUM(normalized_input_tokens_value),0),COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(reasoning_tokens),0),COALESCE(SUM(compatible_cached_tokens_value),0),
		COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(cache_creation_tokens),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN normalized_input_tokens_value ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN output_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN compatible_cached_tokens_value ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN cache_read_tokens ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN normalized_input_tokens_value>? THEN cache_creation_tokens ELSE 0 END),0),
		COALESCE(SUM(total_tokens),0),
		COALESCE(SUM(CASE WHEN latency_ms IS NOT NULL AND latency_ms<>0 THEN latency_ms ELSE 0 END),0),
		COUNT(NULLIF(latency_ms,0)),
		COALESCE(SUM(CASE WHEN total_tokens=0 AND failed=0 THEN 1 ELSE 0 END),0),?
	FROM banded_events
	GROUP BY ` + bucket + `,analytics_model_value,billing_model_value,
		pricing_model_value,COALESCE(service_tier,''),context_threshold_tokens_value,failed,` + groupHash
	args = append(args, watermark.PricingRevision,
		mysqlLongContextThreshold, mysqlLongContextThreshold, mysqlLongContextThreshold,
		mysqlLongContextThreshold, mysqlLongContextThreshold, nowMS)
	return mysqlDerivedStep("usage_pricing_hourly_rollups_v1", query, args...)
}

func mysqlPricingStateStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	query := `INSERT INTO usage_pricing_rollup_state (
		rollup_name,schema_version,structure_revision,status,backfill_last_event_id,
		coverage_event_id,target_event_id,processed_events,min_bucket_ms,max_bucket_ms,
		last_run_started_at_ms,updated_at_ms,finished_at_ms,last_error
	)
	SELECT 'pricing_v1',1,?,'ready',?,?,?,COUNT(*),
		MIN(timestamp_ms-(timestamp_ms MOD ?)),MAX(timestamp_ms-(timestamp_ms MOD ?)),?,?,?,NULL
	FROM usage_events WHERE id<=?`
	return mysqlDerivedStep("usage_pricing_rollup_state", query,
		watermark.PricingRevision,
		watermark.UsageEventID, watermark.UsageEventID, watermark.UsageEventID,
		mysqlDerivedHourMS, mysqlDerivedHourMS, nowMS, nowMS, nowMS, watermark.UsageEventID,
	)
}

func mysqlRollupCheckpointsStep(watermark derivedRebuildWatermark, nowMS int64) mysqlDerivedRebuildStep {
	query := `INSERT INTO usage_rollup_checkpoints (
		name,last_event_id,updated_at_ms,last_error,last_run_started_at_ms,last_run_finished_at_ms
	)
	VALUES ('account_history',?,?,NULL,?,?),('dashboard_hourly',?,?,NULL,?,?)`
	return mysqlDerivedStep("usage_rollup_checkpoints", query,
		watermark.UsageEventID, nowMS, nowMS, nowMS,
		watermark.UsageEventID, nowMS, nowMS, nowMS,
	)
}
