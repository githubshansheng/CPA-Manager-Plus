package usageevent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
)

func (r *repository) upsertMySQLProjectionIDs(
	ctx context.Context,
	tx *sql.Tx,
	eventIDs []int64,
	nowMS int64,
) error {
	if tx == nil {
		return sql.ErrTxDone
	}
	encoded, err := json.Marshal(eventIDs)
	if err != nil {
		return err
	}
	if err := upsertMySQLEventProjection(ctx, tx, string(encoded), nowMS); err != nil {
		return err
	}
	return upsertMySQLHeaderProjection(ctx, tx, string(encoded), nowMS)
}

var mysqlEventProjectionColumns = []string{
	"event_id", "timestamp_ms", "search_text", "account_key", "provider", "executor_type", "model",
	"requested_model", "analytics_model", "resolved_model", "auth_index", "source", "source_hash", "api_key_hash",
	"account_snapshot", "auth_label_snapshot", "auth_file_snapshot", "auth_provider_snapshot", "auth_project_id_snapshot",
	"reasoning_effort", "service_tier", "failed", "latency_ms", "input_tokens", "output_tokens", "reasoning_tokens",
	"cached_tokens", "cache_tokens", "cache_read_tokens", "cache_creation_tokens", "normalized_total_input_tokens",
	"total_tokens", "header_quota_plan_type", "header_error_kind", "header_error_code", "header_trace_id", "updated_at_ms",
}

func upsertMySQLEventProjection(ctx context.Context, tx *sql.Tx, encodedIDs string, nowMS int64) error {
	requestedModel := "coalesce(nullif(requested_model, ''), model, '')"
	analyticsModel := mysqlAnalyticsModelExpression(requestedModel)
	searchText := mysqlSearchTextExpression("", analyticsModel)
	accountKey := mysqlAccountKeyExpression("")
	selectExpressions := []string{
		"id", "timestamp_ms", searchText, accountKey, "coalesce(provider, '')", "coalesce(executor_type, '')",
		"coalesce(model, '')", requestedModel, analyticsModel, "coalesce(resolved_model, '')", "coalesce(auth_index, '')",
		"coalesce(source, '')", "coalesce(source_hash, '')", "coalesce(api_key_hash, '')", "coalesce(account_snapshot, '')",
		"coalesce(auth_label_snapshot, '')", "coalesce(auth_file_snapshot, '')", "coalesce(auth_provider_snapshot, '')",
		"coalesce(auth_project_id_snapshot, '')", "coalesce(reasoning_effort, '')", "coalesce(service_tier, '')",
		"coalesce(failed, 0)", "latency_ms", "coalesce(input_tokens, 0)", "coalesce(output_tokens, 0)",
		"coalesce(reasoning_tokens, 0)", "coalesce(cached_tokens, 0)", "coalesce(cache_tokens, 0)",
		"coalesce(cache_read_tokens, 0)", "coalesce(cache_creation_tokens, 0)",
		"coalesce(normalized_total_input_tokens, input_tokens, 0)", "coalesce(total_tokens, 0)",
		"coalesce(header_quota_plan_type, '')", "coalesce(header_error_kind, '')", "coalesce(header_error_code, '')",
		"coalesce(header_trace_id, '')", "?",
	}
	projected := make([]string, len(mysqlEventProjectionColumns))
	updates := make([]string, 0, len(mysqlEventProjectionColumns)-1)
	for index, column := range mysqlEventProjectionColumns {
		projected[index] = "source.`" + column + "`"
		if column != "event_id" {
			updates = append(updates, "`"+column+"` = source.`"+column+"`")
		}
	}
	query := fmt.Sprintf(`insert into %s (%s)
select %s from (
	select %s from usage_events
	where id in (select value from json_table(?, '$[*]' columns(value bigint path '$')) as event_ids)
) as source
where true
on duplicate key update %s`,
		usageprojection.EventTable,
		quoteMySQLColumns(mysqlEventProjectionColumns),
		strings.Join(projected, ", "),
		strings.Join(aliasExpressions(selectExpressions, mysqlEventProjectionColumns), ",\n\t"),
		strings.Join(updates, ", "),
	)
	_, err := tx.ExecContext(ctx, query, nowMS, encodedIDs)
	return err
}

func mysqlSearchTextExpression(prefix, analyticsModel string) string {
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
	"account_snapshot", "auth_label_snapshot", "auth_provider_snapshot", "auth_project_id_snapshot", "source", "source_hash",
	"response_metadata_json", "header_quota_recover_at_ms", "header_quota_used_percent", "header_quota_plan_type",
	"header_error_kind", "header_error_code", "header_trace_id", "updated_at_ms",
}

func upsertMySQLHeaderProjection(ctx context.Context, tx *sql.Tx, encodedIDs string, nowMS int64) error {
	candidates := mysqlHeaderCandidatesSQL()
	assignments := make([]string, 0, len(mysqlHeaderProjectionColumns)-1)
	for _, column := range mysqlHeaderProjectionColumns[1:] {
		assignments = append(assignments, "target.`"+column+"` = candidate.`"+column+"`")
	}
	newer := `(candidate.event_id = target.event_id or candidate.timestamp_ms > target.timestamp_ms
		or (candidate.timestamp_ms = target.timestamp_ms and candidate.event_id > target.event_id))`
	updateQuery := `update ` + usageprojection.HeaderTable + ` as target
join (` + candidates + `) as candidate on candidate.snapshot_key = target.snapshot_key
set ` + strings.Join(assignments, ", ") + `
where ` + newer
	if _, err := tx.ExecContext(ctx, updateQuery, nowMS, encodedIDs); err != nil {
		return err
	}
	projected := make([]string, len(mysqlHeaderProjectionColumns))
	for index, column := range mysqlHeaderProjectionColumns {
		projected[index] = "candidate.`" + column + "`"
	}
	insertQuery := `insert into ` + usageprojection.HeaderTable + ` (` + quoteMySQLColumns(mysqlHeaderProjectionColumns) + `)
select ` + strings.Join(projected, ", ") + ` from (` + candidates + `) as candidate
where true
on duplicate key update ` + usageprojection.HeaderTable + `.snapshot_key = ` + usageprojection.HeaderTable + `.snapshot_key`
	_, err := tx.ExecContext(ctx, insertQuery, nowMS, encodedIDs)
	return err
}

func mysqlHeaderCandidatesSQL() string {
	snapshotKey := mysqlSnapshotKeyExpression("")
	baseColumns := []string{
		"id as event_id", "event_hash", "timestamp_ms", "coalesce(auth_file_snapshot, '') as auth_file_snapshot",
		"coalesce(auth_index, '') as auth_index", "coalesce(account_snapshot, '') as account_snapshot",
		"coalesce(auth_label_snapshot, '') as auth_label_snapshot",
		"coalesce(nullif(auth_provider_snapshot, ''), provider, '') as auth_provider_snapshot",
		"coalesce(auth_project_id_snapshot, '') as auth_project_id_snapshot", "coalesce(source, '') as source",
		"coalesce(source_hash, '') as source_hash", "coalesce(response_metadata_json, '') as response_metadata_json",
		"header_quota_recover_at_ms", "header_quota_used_percent",
		"coalesce(header_quota_plan_type, '') as header_quota_plan_type",
		"coalesce(header_error_kind, '') as header_error_kind", "coalesce(header_error_code, '') as header_error_code",
		"coalesce(header_trace_id, '') as header_trace_id", snapshotKey + " as snapshot_key", "? as updated_at_ms",
	}
	return `select ranked.snapshot_key, ranked.event_id, ranked.event_hash, ranked.timestamp_ms,
	ranked.auth_file_snapshot, ranked.auth_index, ranked.account_snapshot, ranked.auth_label_snapshot,
	ranked.auth_provider_snapshot, ranked.auth_project_id_snapshot, ranked.source, ranked.source_hash,
	ranked.response_metadata_json, ranked.header_quota_recover_at_ms, ranked.header_quota_used_percent,
	ranked.header_quota_plan_type, ranked.header_error_kind, ranked.header_error_code, ranked.header_trace_id,
	ranked.updated_at_ms
from (
	select candidates.*, row_number() over (
		partition by candidates.snapshot_key order by candidates.timestamp_ms desc, candidates.event_id desc
	) as rn
	from (
		select ` + strings.Join(baseColumns, ",\n\t\t") + `
		from usage_events
		where id in (select value from json_table(?, '$[*]' columns(value bigint path '$')) as event_ids)
		and (coalesce(response_metadata_json, '') <> '' or header_quota_recover_at_ms is not null
			or header_quota_used_percent is not null or coalesce(header_quota_plan_type, '') <> ''
			or coalesce(header_error_kind, '') <> '' or coalesce(header_error_code, '') <> ''
			or coalesce(header_trace_id, '') <> '')
		and (coalesce(auth_file_snapshot, '') <> '' or coalesce(auth_index, '') <> ''
			or coalesce(account_snapshot, '') <> '' or coalesce(source_hash, '') <> '')
	) as candidates
) as ranked where ranked.rn = 1`
}

func mysqlSnapshotKeyExpression(prefix string) string {
	column := func(name string) string { return prefix + name }
	return fmt.Sprintf(`case
	when coalesce(%[1]s, '') <> '' and coalesce(%[2]s, '') <> '' then concat(coalesce(%[1]s, ''), '::', coalesce(%[2]s, ''))
	when coalesce(%[1]s, '') <> '' then concat('file::', coalesce(%[1]s, ''))
	when coalesce(%[2]s, '') <> '' then concat('auth::', coalesce(%[2]s, ''))
	when coalesce(%[3]s, '') <> '' then concat('account::', lower(coalesce(%[3]s, '')))
	when coalesce(%[4]s, '') <> '' then concat('source::', coalesce(%[4]s, ''))
	else concat('event::', %[5]s)
end`, column("auth_file_snapshot"), column("auth_index"), column("account_snapshot"), column("source_hash"), column("event_hash"))
}

func aliasExpressions(expressions, columns []string) []string {
	result := make([]string, len(columns))
	for index := range columns {
		result[index] = expressions[index] + " as `" + columns[index] + "`"
	}
	return result
}

func quoteMySQLColumns(columns []string) string {
	quoted := make([]string, len(columns))
	for index, column := range columns {
		quoted[index] = "`" + column + "`"
	}
	return strings.Join(quoted, ", ")
}
