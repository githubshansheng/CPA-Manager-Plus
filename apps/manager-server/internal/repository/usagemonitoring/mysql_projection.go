package usagemonitoring

import (
	"context"
	"fmt"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/dialect"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

var mysqlEventProjectionColumns = []string{
	"event_id", "timestamp_ms", "search_text", "account_key", "provider", "executor_type", "model",
	"requested_model", "analytics_model", "resolved_model", "auth_index", "source", "source_hash", "api_key_hash",
	"account_snapshot", "auth_label_snapshot", "auth_file_snapshot", "auth_provider_snapshot", "auth_account_id_snapshot", "auth_project_id_snapshot",
	"reasoning_effort", "service_tier", "failed", "latency_ms", "input_tokens", "output_tokens", "reasoning_tokens",
	"cached_tokens", "cache_tokens", "cache_read_tokens", "cache_creation_tokens", "normalized_total_input_tokens",
	"total_tokens", "header_quota_plan_type", "header_error_kind", "header_error_code", "header_trace_id", "updated_at_ms",
}

func upsertMySQLEventProjectionRange(
	ctx context.Context,
	tx *dialect.Tx,
	afterID, throughID, nowMS int64,
) error {
	if throughID <= afterID {
		return nil
	}
	mysql := dialect.MySQL()
	requestedModel := "coalesce(nullif(requested_model, ''), model, '')"
	analyticsModel := mysql.RewriteQuery(usageidentity.SQLAnalyticsModelExpression(requestedModel))
	accountKey := mysql.RewriteQuery(usageidentity.SQLAccountKeyExpression(""))
	selectExpressions := []string{
		"id", "timestamp_ms", mysqlProjectionSearchText("", analyticsModel), accountKey,
		"coalesce(provider, '')", "coalesce(executor_type, '')", "coalesce(model, '')",
		requestedModel, analyticsModel, "coalesce(resolved_model, '')", "coalesce(auth_index, '')",
		"coalesce(source, '')", "coalesce(source_hash, '')", "coalesce(api_key_hash, '')",
		"coalesce(account_snapshot, '')", "coalesce(auth_label_snapshot, '')",
		"coalesce(auth_file_snapshot, '')", "coalesce(auth_provider_snapshot, '')",
		"coalesce(auth_account_id_snapshot, '')", "coalesce(auth_project_id_snapshot, '')", "coalesce(reasoning_effort, '')",
		"coalesce(service_tier, '')", "coalesce(failed, 0)", "latency_ms",
		"coalesce(input_tokens, 0)", "coalesce(output_tokens, 0)", "coalesce(reasoning_tokens, 0)",
		"coalesce(cached_tokens, 0)", "coalesce(cache_tokens, 0)", "coalesce(cache_read_tokens, 0)",
		"coalesce(cache_creation_tokens, 0)", "coalesce(normalized_total_input_tokens, input_tokens, 0)",
		"coalesce(total_tokens, 0)", "coalesce(header_quota_plan_type, '')",
		"coalesce(header_error_kind, '')", "coalesce(header_error_code, '')",
		"coalesce(header_trace_id, '')", "?",
	}
	projected := make([]string, len(mysqlEventProjectionColumns))
	aliased := make([]string, len(mysqlEventProjectionColumns))
	updates := make([]string, 0, len(mysqlEventProjectionColumns)-1)
	for index, column := range mysqlEventProjectionColumns {
		projected[index] = "source." + column
		aliased[index] = selectExpressions[index] + " as " + column
		if column != "event_id" {
			updates = append(updates, column+" = source."+column)
		}
	}
	query := fmt.Sprintf(
		"insert into %s (%s)\nselect %s from (\nselect %s from usage_events "+
			"where id > ? and id <= ?\n) as source\nwhere true\non duplicate key update %s",
		usageprojection.EventTable,
		strings.Join(mysqlEventProjectionColumns, ", "),
		strings.Join(projected, ", "),
		strings.Join(aliased, ",\n"),
		strings.Join(updates, ", "),
	)
	if _, err := tx.ExecContext(ctx, query, nowMS, afterID, throughID); err != nil {
		return err
	}
	searchQuery := "insert into " + usageprojection.SearchIndexTable +
		" (event_id, search_text) select event_id, search_text from " +
		usageprojection.EventTable + " where event_id > ? and event_id <= ? " +
		"on duplicate key update search_text = values(search_text)"
	_, err := tx.ExecContext(ctx, searchQuery, afterID, throughID)
	return err
}

func mysqlProjectionSearchText(prefix, analyticsModel string) string {
	parts := make([]string, 0, len(usageprojection.SearchColumns)*2-1)
	for index, column := range usageprojection.SearchColumns {
		if index > 0 {
			parts = append(parts, "char(31)")
		}
		expression := prefix + column
		if column == "analytics_model" {
			expression = analyticsModel
		}
		parts = append(parts, "coalesce("+expression+", '')")
	}
	return "lower(concat(" + strings.Join(parts, ", ") + "))"
}

var mysqlHeaderProjectionColumns = []string{
	"snapshot_key", "event_id", "event_hash", "timestamp_ms", "auth_file_snapshot", "auth_index",
	"account_snapshot", "auth_label_snapshot", "auth_provider_snapshot", "auth_account_id_snapshot", "auth_project_id_snapshot", "source", "source_hash",
	"response_metadata_json", "header_quota_recover_at_ms", "header_quota_used_percent", "header_quota_plan_type",
	"header_error_kind", "header_error_code", "header_trace_id", "updated_at_ms",
}

func upsertMySQLHeaderProjectionRange(
	ctx context.Context,
	tx *dialect.Tx,
	afterID, throughID, nowMS int64,
) error {
	if throughID <= afterID {
		return nil
	}
	candidates := mysqlHeaderCandidatesRangeSQL()
	assignments := make([]string, 0, len(mysqlHeaderProjectionColumns)-1)
	projected := make([]string, len(mysqlHeaderProjectionColumns))
	for index, column := range mysqlHeaderProjectionColumns {
		projected[index] = "candidate." + column
		if column != "snapshot_key" {
			assignments = append(assignments, "target."+column+" = candidate."+column)
		}
	}
	newer := "(candidate.event_id = target.event_id or candidate.timestamp_ms > target.timestamp_ms " +
		"or (candidate.timestamp_ms = target.timestamp_ms and candidate.event_id > target.event_id))"
	updateQuery := "update " + usageprojection.HeaderTable + " as target\njoin (" + candidates +
		") as candidate on candidate.snapshot_key = target.snapshot_key\nset " +
		strings.Join(assignments, ", ") + "\nwhere " + newer
	if _, err := tx.ExecContext(ctx, updateQuery, nowMS, afterID, throughID); err != nil {
		return err
	}
	insertQuery := "insert into " + usageprojection.HeaderTable + " (" +
		strings.Join(mysqlHeaderProjectionColumns, ", ") + ")\nselect " +
		strings.Join(projected, ", ") + " from (" + candidates +
		") as candidate\nwhere true\non duplicate key update snapshot_key = values(snapshot_key)"
	_, err := tx.ExecContext(ctx, insertQuery, nowMS, afterID, throughID)
	return err
}

func mysqlHeaderCandidatesRangeSQL() string {
	snapshotKey := mysqlSnapshotKeyExpression("")
	baseColumns := []string{
		"id as event_id", "event_hash", "timestamp_ms",
		"coalesce(auth_file_snapshot, '') as auth_file_snapshot",
		"coalesce(auth_index, '') as auth_index", "coalesce(account_snapshot, '') as account_snapshot",
		"coalesce(auth_label_snapshot, '') as auth_label_snapshot",
		"coalesce(nullif(auth_provider_snapshot, ''), provider, '') as auth_provider_snapshot",
		"coalesce(auth_account_id_snapshot, '') as auth_account_id_snapshot",
		"coalesce(auth_project_id_snapshot, '') as auth_project_id_snapshot",
		"coalesce(source, '') as source", "coalesce(source_hash, '') as source_hash",
		"coalesce(response_metadata_json, '') as response_metadata_json",
		"header_quota_recover_at_ms", "header_quota_used_percent",
		"coalesce(header_quota_plan_type, '') as header_quota_plan_type",
		"coalesce(header_error_kind, '') as header_error_kind",
		"coalesce(header_error_code, '') as header_error_code",
		"coalesce(header_trace_id, '') as header_trace_id", snapshotKey + " as snapshot_key",
		"? as updated_at_ms",
	}
	return "select ranked.snapshot_key, ranked.event_id, ranked.event_hash, ranked.timestamp_ms, " +
		"ranked.auth_file_snapshot, ranked.auth_index, ranked.account_snapshot, ranked.auth_label_snapshot, " +
		"ranked.auth_provider_snapshot, ranked.auth_account_id_snapshot, ranked.auth_project_id_snapshot, ranked.source, ranked.source_hash, " +
		"ranked.response_metadata_json, ranked.header_quota_recover_at_ms, ranked.header_quota_used_percent, " +
		"ranked.header_quota_plan_type, ranked.header_error_kind, ranked.header_error_code, ranked.header_trace_id, " +
		"ranked.updated_at_ms from (select candidates.*, row_number() over (" +
		"partition by candidates.snapshot_key order by candidates.timestamp_ms desc, candidates.event_id desc" +
		") as rn from (select " + strings.Join(baseColumns, ", ") + " from usage_events " +
		"where id > ? and id <= ? and (" +
		"coalesce(response_metadata_json, '') <> '' or header_quota_recover_at_ms is not null or " +
		"header_quota_used_percent is not null or coalesce(header_quota_plan_type, '') <> '' or " +
		"coalesce(header_error_kind, '') <> '' or coalesce(header_error_code, '') <> '' or " +
		"coalesce(header_trace_id, '') <> '') and (" +
		"coalesce(auth_file_snapshot, '') <> '' or coalesce(auth_index, '') <> '' or " +
		"coalesce(account_snapshot, '') <> '' or coalesce(source_hash, '') <> '')) as candidates" +
		") as ranked where ranked.rn = 1"
}

func mysqlSnapshotKeyExpression(prefix string) string {
	column := func(name string) string { return prefix + name }
	return fmt.Sprintf(
		"case when coalesce(%[1]s, '') <> '' and coalesce(%[2]s, '') <> '' "+
			"then concat(coalesce(%[1]s, ''), '::', coalesce(%[2]s, '')) "+
			"when coalesce(%[1]s, '') <> '' then concat('file::', coalesce(%[1]s, '')) "+
			"when coalesce(%[2]s, '') <> '' then concat('auth::', coalesce(%[2]s, '')) "+
			"when coalesce(%[3]s, '') <> '' then concat('account::', lower(coalesce(%[3]s, ''))) "+
			"when coalesce(%[4]s, '') <> '' then concat('source::', coalesce(%[4]s, '')) "+
			"else concat('event::', %[5]s) end",
		column("auth_file_snapshot"), column("auth_index"), column("account_snapshot"),
		column("source_hash"), column("event_hash"),
	)
}
