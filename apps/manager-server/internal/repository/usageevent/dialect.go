package usageevent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/outboxcontext"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

// NewSQLite explicitly constructs the legacy SQLite repository. New remains
// an alias for this constructor so existing single-database deployments keep
// exactly the same behavior.
func NewSQLite(db *sql.DB) Repository {
	return &repository{db: db, backend: database.BackendSQLite}
}

// NewMySQL constructs a repository only after checking the session contract
// required to prevent lossy writes. It intentionally performs no DDL and does
// not enable routing, replication, or a write-readiness gate.
func NewMySQL(db *sql.DB) (Repository, error) {
	if db == nil {
		return nil, errors.New("usage event mysql repository requires a database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := validateMySQLSession(ctx, db); err != nil {
		return nil, err
	}
	return &repository{db: db, backend: database.BackendMySQL}, nil
}

// NewForBackend exposes a checked dialect constructor for routed-store wiring.
func NewForBackend(db *sql.DB, backend database.BackendKind) (Repository, error) {
	switch backend {
	case database.BackendSQLite:
		if db == nil {
			return nil, errors.New("usage event sqlite repository requires a database")
		}
		return NewSQLite(db), nil
	case database.BackendMySQL:
		return NewMySQL(db)
	default:
		return nil, fmt.Errorf("unsupported usage event database backend %q", backend)
	}
}

func validateMySQLSession(ctx context.Context, db *sql.DB) error {
	var version, versionComment, sqlMode, characterSet, collation, timeZone string
	if err := db.QueryRowContext(ctx, `select @@version, @@version_comment, @@session.sql_mode,
		@@session.character_set_connection, @@session.collation_connection, @@session.time_zone`).Scan(
		&version, &versionComment, &sqlMode, &characterSet, &collation, &timeZone,
	); err != nil {
		return fmt.Errorf("inspect usage event mysql session: %w", err)
	}
	parsedVersion, err := database.ParseSupportedMySQLVersion(version, versionComment)
	if err != nil {
		return err
	}
	modes := "," + strings.ToUpper(strings.ReplaceAll(sqlMode, " ", "")) + ","
	if !strings.Contains(modes, ",STRICT_TRANS_TABLES,") && !strings.Contains(modes, ",STRICT_ALL_TABLES,") {
		return errors.New("mysql strict SQL mode is required for lossless usage event writes")
	}
	if !strings.EqualFold(characterSet, database.MySQLCharacterSet) {
		return fmt.Errorf("mysql connection character set is %q, want %s", characterSet, database.MySQLCharacterSet)
	}
	requiredCollation := parsedVersion.StorageCollation()
	if !strings.EqualFold(collation, requiredCollation) {
		return fmt.Errorf("mysql connection collation is %q, want %s", collation, requiredCollation)
	}
	if timeZone != "+00:00" && !strings.EqualFold(timeZone, "UTC") {
		return fmt.Errorf("mysql session time zone is %q, want +00:00", timeZone)
	}
	return nil
}

func validateMySQLVersion(version, comment string) error {
	_, err := database.ParseSupportedMySQLVersion(version, comment)
	return err
}

func (r *repository) isMySQL() bool {
	return r != nil && r.backend == database.BackendMySQL
}

func (r *repository) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return r.db.QueryContext(ctx, r.querySQL(query), args...)
}

func (r *repository) queryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return r.db.QueryRowContext(ctx, r.querySQL(query), args...)
}

type usageEventTx interface {
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	Commit() error
	Rollback() error
}

func (r *repository) beginMySQLWriteTx(ctx context.Context) (usageEventTx, *sql.Tx, error) {
	tx, err := outboxcontext.Begin(ctx, r.db, nil)
	if err != nil {
		return nil, nil, err
	}
	return tx, tx.Tx, nil
}

func (r *repository) insertIfAbsentSQL(sqliteSQL string) string {
	if !r.isMySQL() {
		return sqliteSQL
	}
	query := strings.Replace(sqliteSQL, "insert or ignore into", "insert into", 1)
	if strings.Contains(query, "usage_event_identity_ledger") {
		return query + " on duplicate key update event_hash = event_hash"
	}
	return query + " on duplicate key update event_hash = event_hash"
}

// querySQL maps the finite SQLite grammar used by this repository to concrete
// MySQL 8 SQL. It is deliberately not a general SQL rewriter: every rewrite
// corresponds to a production query in this package and is regression-tested.
func (r *repository) querySQL(query string) string {
	if !r.isMySQL() {
		return query
	}
	query = replaceAnalyticsModelCalls(query)
	query = strings.ReplaceAll(query, usageidentity.SQLAccountKeyExpression("e"), mysqlAccountKeyExpression("e"))
	query = strings.ReplaceAll(query, usageidentity.SQLAccountKeyExpression(""), mysqlAccountKeyExpression(""))
	query = strings.NewReplacer(
		"max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0)",
		"greatest(greatest(cached_tokens, cache_tokens) - greatest(cache_read_tokens, 0) - greatest(cache_creation_tokens, 0), 0)",
		"max(max(f.cached_tokens, f.cache_tokens) - max(f.cache_read_tokens, 0) - max(f.cache_creation_tokens, 0), 0)",
		"greatest(greatest(f.cached_tokens, f.cache_tokens) - greatest(f.cache_read_tokens, 0) - greatest(f.cache_creation_tokens, 0), 0)",
		"max(max(e.cached_tokens, e.cache_tokens) - max(e.cache_read_tokens, 0) - max(e.cache_creation_tokens, 0), 0)",
		"greatest(greatest(e.cached_tokens, e.cache_tokens) - greatest(e.cache_read_tokens, 0) - greatest(e.cache_creation_tokens, 0), 0)",
		"cast((timestamp_ms - ?) / ? as integer)", "floor((timestamp_ms - ?) / ?)",
		"(timestamp_ms / 3600000) * 3600000", "floor(timestamp_ms / 3600000) * 3600000",
		"((sample_count * 95) + 99) / 100", "floor(((sample_count * 95) + 99) / 100)",
		" not indexed", "",
		"select value from json_each(?)", "select value from json_table(?, '$[*]' columns(value longtext path '$')) as cpamp_values",
		"json_type(metadata_json, '", "json_type(json_extract(metadata_json, '",
		"') is null", "')) is null",
		"coalesce(timestamp, '') || '|' || coalesce(source_hash, '') || '|' || coalesce(auth_index, '')",
		"concat(coalesce(timestamp, ''), '|', coalesce(source_hash, ''), '|', coalesce(auth_index, ''))",
		"coalesce(auth_file_snapshot, '') || '::' || coalesce(auth_index, '')",
		"concat(coalesce(auth_file_snapshot, ''), '::', coalesce(auth_index, ''))",
		"'file::' || coalesce(auth_file_snapshot, '')", "concat('file::', coalesce(auth_file_snapshot, ''))",
		"'auth::' || coalesce(auth_index, '')", "concat('auth::', coalesce(auth_index, ''))",
		"'account::' || lower(coalesce(account_snapshot, ''))", "concat('account::', lower(coalesce(account_snapshot, '')))",
		"'source::' || coalesce(source_hash, '')", "concat('source::', coalesce(source_hash, ''))",
		"'event::' || event_hash", "concat('event::', event_hash)",
		"e.auth_file_snapshot collate nocase = ?", "lower(e.auth_file_snapshot) = lower(?)",
		"e.auth_index collate nocase = ?", "lower(e.auth_index) = lower(?)",
		"e.auth_index collate nocase = ''", "lower(e.auth_index) = ''",
		"e.source collate nocase = ?", "lower(e.source) = lower(?)",
		"e.auth_file_snapshot collate nocase = t.auth_file_snapshot", "lower(e.auth_file_snapshot) = lower(t.auth_file_snapshot)",
		"coalesce(e.auth_index, '') collate nocase = t.auth_index", "lower(coalesce(e.auth_index, '')) = lower(t.auth_index)",
		"e.source collate nocase = t.auth_file_snapshot", "lower(e.source) = lower(t.auth_file_snapshot)",
	).Replace(query)
	return query
}

func replaceAnalyticsModelCalls(query string) string {
	const marker = "cpamp_analytics_model("
	for {
		start := strings.Index(query, marker)
		if start < 0 {
			return query
		}
		argumentStart := start + len(marker)
		depth := 1
		quoted := false
		end := -1
		for index := argumentStart; index < len(query); index++ {
			switch query[index] {
			case '\'':
				if quoted && index+1 < len(query) && query[index+1] == '\'' {
					index++
					continue
				}
				quoted = !quoted
			case '(':
				if !quoted {
					depth++
				}
			case ')':
				if !quoted {
					depth--
					if depth == 0 {
						end = index
						index = len(query)
					}
				}
			}
		}
		if end < 0 {
			return query
		}
		argument := query[argumentStart:end]
		query = query[:start] + mysqlAnalyticsModelExpression(argument) + query[end+1:]
	}
}

func mysqlAnalyticsModelExpression(value string) string {
	tail := "substring_index(" + value + ", '(', -1)"
	suffix := "left(" + tail + ", char_length(" + tail + ") - 1)"
	validNumeric := "case when regexp_like(" + suffix + ", '^[+]?[0-9]+$') then cast(" + suffix +
		" as decimal(65,0)) <= 9223372036854775807 else false end"
	return "case when right(" + value + ", 1) = ')' and char_length(" + tail + ") < char_length(" + value +
		") - 1 and (lower(" + suffix + ") in ('none','auto','-1','minimal','low','medium','high','xhigh','max')" +
		" or regexp_like(" + suffix + ", '^-0+$') or " + validNumeric + ") then left(" + value +
		", char_length(" + value + ") - char_length(" + tail + ") - 1) else " + value + " end"
}

func mysqlAccountKeyExpression(alias string) string {
	column := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	trimmed := func(name string) string { return "trim(coalesce(" + column(name) + ", ''))" }
	authFileSnapshot, authIndex := trimmed("auth_file_snapshot"), trimmed("auth_index")
	source, account, label := trimmed("source"), trimmed("account_snapshot"), trimmed("auth_label_snapshot")
	projectID := trimmed("auth_project_id_snapshot")
	providerSource := "coalesce(nullif(" + trimmed("auth_provider_snapshot") + ", ''), " + trimmed("provider") + ", '')"
	provider := "case lower(replace(trim(" + providerSource + "), '_', '-')) when 'x-ai' then 'xai' when 'grok' then 'xai' else lower(replace(trim(" + providerSource + "), '_', '-')) end"
	authFile := "case when " + authFileSnapshot + " <> '' then " + authFileSnapshot + " when " + source + " <> '' and " + source + " <> " + account + " and " + source + " <> " + label + " then " + source + " else '' end"
	key := func(kind string, values ...string) string {
		parts := []string{"'usage-account-history:2:" + kind + ":'"}
		for index, value := range values {
			if index > 0 {
				parts = append(parts, "':'")
			}
			parts = append(parts, "hex("+value+")")
		}
		return "concat(" + strings.Join(parts, ", ") + ")"
	}
	return "case " +
		"when " + authFile + " <> '' and " + authIndex + " <> '' then " + key("file-index", authFile, authIndex) + " " +
		"when " + authFile + " <> '' and " + projectID + " <> '' then " + key("file-project", authFile, provider, projectID) + " " +
		"when " + authFile + " <> '' and " + account + " <> '' then " + key("file-account", authFile, provider, account) + " " +
		"when " + authFile + " <> '' and " + label + " <> '' then " + key("file-label", authFile, provider, label) + " " +
		"when " + authFile + " <> '' then " + key("file", authFile, provider) + " " +
		"when " + authIndex + " <> '' then " + key("auth-index", provider, authIndex) + " " +
		"when " + projectID + " <> '' then " + key("project", provider, projectID) + " " +
		"when " + account + " <> '' then " + key("account", provider, account) + " " +
		"when " + label + " <> '' then " + key("label", provider, label) + " else '' end"
}

func (r *repository) valuesCTE(rowWidth, rowCount int) string {
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", rowWidth), ",") + ")"
	if !r.isMySQL() {
		return "values " + strings.TrimSuffix(strings.Repeat(row+",", rowCount), ",")
	}
	selectRow := "select " + strings.Trim(row, "()")
	return strings.TrimSuffix(strings.Repeat(selectRow+" union all ", rowCount), " union all ")
}
