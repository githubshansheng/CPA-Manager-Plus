package dialect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var mysqlConflictUpdatePattern = regexp.MustCompile("(?is)on\\s+conflict\\s*\\([^)]*\\)\\s+do\\s+update\\s+set")
var mysqlExcludedColumnPattern = regexp.MustCompile("(?i)\\bexcluded\\.([A-Za-z_][A-Za-z0-9_]*)")

// Dialect contains the small set of SQL grammar differences used by shared
// repositories. It does not rewrite arbitrary SQL and only accepts validated
// internal identifiers.
type Dialect struct {
	kind database.BackendKind
}

func ForBackend(kind database.BackendKind) Dialect {
	switch kind {
	case database.BackendSQLite, database.BackendMySQL:
		return Dialect{kind: kind}
	default:
		panic(fmt.Sprintf("unsupported repository database backend %q", kind))
	}
}

func SQLite() Dialect { return ForBackend(database.BackendSQLite) }

func MySQL() Dialect { return ForBackend(database.BackendMySQL) }

func (d Dialect) Kind() database.BackendKind {
	if d.kind == "" {
		return database.BackendSQLite
	}
	return d.kind
}

func (d Dialect) IsMySQL() bool { return d.Kind() == database.BackendMySQL }

// ValidateMySQLSession checks the lossless repository contract on an already
// opened connection pool. Connection creation remains owned by database/mysql;
// this guard protects direct repository construction in tests and tools.
func ValidateMySQLSession(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("mysql repository database is required")
	}
	var version, versionComment, sqlMode, characterSet, collation, timeZone string
	if err := db.QueryRowContext(ctx, `select @@version, @@version_comment, @@session.sql_mode,
		@@session.character_set_connection, @@session.collation_connection, @@session.time_zone`).Scan(
		&version, &versionComment, &sqlMode, &characterSet, &collation, &timeZone,
	); err != nil {
		return fmt.Errorf("inspect mysql repository session: %w", err)
	}
	parsedVersion, err := database.ParseSupportedMySQLVersion(version, versionComment)
	if err != nil {
		return err
	}
	modes := "," + strings.ToUpper(strings.ReplaceAll(sqlMode, " ", "")) + ","
	if !strings.Contains(modes, ",STRICT_TRANS_TABLES,") &&
		!strings.Contains(modes, ",STRICT_ALL_TABLES,") {
		return errors.New("mysql strict SQL mode is required for lossless repository writes")
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

// UpsertClause follows one VALUES row. VALUES(column) remains supported in
// MySQL 8.0/8.4 and is required for compatibility with MySQL 8.0.12, which
// predates INSERT row aliases.
func (d Dialect) UpsertClause(conflictColumns, updateColumns []string) string {
	if len(conflictColumns) == 0 || len(updateColumns) == 0 {
		panic("repository upsert requires conflict and update columns")
	}
	for _, column := range append(append([]string(nil), conflictColumns...), updateColumns...) {
		if !identifierPattern.MatchString(column) {
			panic(fmt.Sprintf("invalid repository SQL identifier %q", column))
		}
	}
	assignments := make([]string, len(updateColumns))
	if d.IsMySQL() {
		for index, column := range updateColumns {
			assignments[index] = quoteMySQL(column) + " = VALUES(" + quoteMySQL(column) + ")"
		}
		return " ON DUPLICATE KEY UPDATE " + strings.Join(assignments, ", ")
	}
	for index, column := range updateColumns {
		assignments[index] = quoteSQLite(column) + " = excluded." + quoteSQLite(column)
	}
	quotedConflicts := make([]string, len(conflictColumns))
	for index, column := range conflictColumns {
		quotedConflicts[index] = quoteSQLite(column)
	}
	return " ON CONFLICT (" + strings.Join(quotedConflicts, ", ") + ") DO UPDATE SET " +
		strings.Join(assignments, ", ")
}

// ForUpdate locks the rows (and, under MySQL's default InnoDB isolation, the
// scanned key range) while application-defined uniqueness is checked.
func (d Dialect) ForUpdate() string {
	if d.IsMySQL() {
		return " FOR UPDATE"
	}
	return ""
}

// NowMillisExpression returns the backend clock as Unix milliseconds. It is
// used when a timestamp must be captured after waiting for a database lock.
func (d Dialect) NowMillisExpression() string {
	if d.IsMySQL() {
		return "CAST(UNIX_TIMESTAMP(CURRENT_TIMESTAMP(3)) * 1000 AS SIGNED)"
	}
	return "cast(unixepoch('subsec') * 1000 as integer)"
}

// InsertDoNothingClause follows one VALUES row and makes a duplicate logical
// key a no-op. MySQL has no ON CONFLICT DO NOTHING form, so it performs a
// stable self-assignment on a validated column.
func (d Dialect) InsertDoNothingClause(conflictColumns []string, stableColumn string) string {
	if len(conflictColumns) == 0 || !identifierPattern.MatchString(stableColumn) {
		panic("repository insert-ignore requires conflict and stable columns")
	}
	for _, column := range conflictColumns {
		if !identifierPattern.MatchString(column) {
			panic(fmt.Sprintf("invalid repository SQL identifier %q", column))
		}
	}
	if d.IsMySQL() {
		quoted := quoteMySQL(stableColumn)
		return " ON DUPLICATE KEY UPDATE " + quoted + " = " + quoted
	}
	quotedConflicts := make([]string, len(conflictColumns))
	for index, column := range conflictColumns {
		quotedConflicts[index] = quoteSQLite(column)
	}
	return " ON CONFLICT (" + strings.Join(quotedConflicts, ", ") + ") DO NOTHING"
}

// MaxWithParameter returns a scalar maximum expression for one trusted column
// and one bound parameter. SQLite exposes max as both an aggregate and scalar
// function, while MySQL names the scalar form GREATEST.
func (d Dialect) MaxWithParameter(column string) string {
	if !identifierPattern.MatchString(column) {
		panic(fmt.Sprintf("invalid repository SQL identifier %q", column))
	}
	if d.IsMySQL() {
		return "GREATEST(" + quoteMySQL(column) + ", ?)"
	}
	return "max(" + quoteSQLite(column) + ", ?)"
}

// MinWithParameter returns a scalar minimum expression for one trusted column
// and one bound parameter. MySQL calls the scalar function LEAST rather than
// SQLite's overloaded min function.
func (d Dialect) MinWithParameter(column string) string {
	if !identifierPattern.MatchString(column) {
		panic(fmt.Sprintf("invalid repository SQL identifier %q", column))
	}
	if d.IsMySQL() {
		return "LEAST(" + quoteMySQL(column) + ", ?)"
	}
	return "min(" + quoteSQLite(column) + ", ?)"
}

// RewriteQuery translates the finite SQLite expression grammar shared by the
// usage-derived repositories into MySQL 8 SQL. It deliberately leaves SQLite
// byte-for-byte unchanged and does not attempt to be a general SQL parser.
// Repository-specific constructs such as UPDATE ... FROM and bounded rowid
// deletes remain explicit at their call sites.
func (d Dialect) RewriteQuery(query string) string {
	if !d.IsMySQL() {
		return query
	}
	query = replaceAnalyticsModelCalls(query)
	query = strings.ReplaceAll(query, usageidentity.SQLAccountKeyExpression("e"), mysqlAccountKeyExpression("e"))
	query = strings.ReplaceAll(query, usageidentity.SQLAccountKeyExpression(""), mysqlAccountKeyExpression(""))
	query = replaceScalarFunction(query, "max", "greatest")
	query = replaceScalarFunction(query, "min", "least")
	query = rewriteLeadingCTEInsert(query)
	query = strings.NewReplacer(
		" not indexed", "",
		"select rowid from usage_monitoring_event_search_v1",
		"select event_id from usage_monitoring_event_search_v1",
		"update usage_event_identity_ledger as ledger set",
		"update usage_event_identity_ledger as ledger join usage_events as e on ledger.event_hash = e.event_hash set",
		"\n\tfrom usage_events as e\n\twhere ledger.event_hash = e.event_hash\n\t\tand ",
		"\n\twhere ",
		"select value from json_each(?)",
		"select value from json_table(?, '$[*]' columns(value longtext path '$')) as cpamp_values",
		"coalesce(auth_file_snapshot, '') || '::' || coalesce(auth_index, '')",
		"concat(coalesce(auth_file_snapshot, ''), '::', coalesce(auth_index, ''))",
		"'file::' || coalesce(auth_file_snapshot, '')",
		"concat('file::', coalesce(auth_file_snapshot, ''))",
		"'auth::' || coalesce(auth_index, '')",
		"concat('auth::', coalesce(auth_index, ''))",
		"'account::' || lower(coalesce(account_snapshot, ''))",
		"concat('account::', lower(coalesce(account_snapshot, '')))",
		"'source::' || coalesce(source_hash, '')",
		"concat('source::', coalesce(source_hash, ''))",
		"'event::' || event_hash", "concat('event::', event_hash)",
		"cast((timestamp_ms - ?) / ? as integer)", "floor((timestamp_ms - ?) / ?)",
		"(timestamp_ms / 3600000) * 3600000", "floor(timestamp_ms / 3600000) * 3600000",
		"(bucket_ms / ?) * ?", "floor(bucket_ms / ?) * ?",
		"((e.timestamp_ms - (e.timestamp_ms % 3600000)) / ?) * ?",
		"floor((e.timestamp_ms - (e.timestamp_ms % 3600000)) / ?) * ?",
		"((sample_count * 95) + 99) / 100", "floor(((sample_count * 95) + 99) / 100)",
	).Replace(query)
	// MySQL stores the logical LONGTEXT primary key of the identity ledger in
	// a generated SHA-256 column. Comparing event_hash alone cannot use that
	// unique index and makes INSERT ... SELECT lock-scan the entire ledger.
	// Keep the exact comparison as a collision guard while using the generated
	// hash to make correlated lookups and UPDATE joins indexable.
	query = strings.ReplaceAll(query,
		"ledger.event_hash = e.event_hash",
		"ledger.__cpamp_pk_hash = UNHEX(SHA2(CAST(JSON_ARRAY(e.event_hash) AS CHAR CHARACTER SET utf8mb4), 256)) and ledger.event_hash = e.event_hash",
	)
	query = strings.ReplaceAll(query,
		"ledger.event_hash = usage_events.event_hash",
		"ledger.__cpamp_pk_hash = UNHEX(SHA2(CAST(JSON_ARRAY(usage_events.event_hash) AS CHAR CHARACTER SET utf8mb4), 256)) and ledger.event_hash = usage_events.event_hash",
	)
	query = qualifyIdentityLedgerUpdateAssignments(query)
	query = mysqlConflictUpdatePattern.ReplaceAllString(query, "on duplicate key update")
	query = mysqlExcludedColumnPattern.ReplaceAllString(query, "values($1)")
	return query
}

func qualifyIdentityLedgerUpdateAssignments(query string) string {
	if !strings.Contains(query, "update usage_event_identity_ledger as ledger join usage_events as e") {
		return query
	}
	// MySQL resolves unqualified names in a multi-table UPDATE against both the
	// ledger and usage_events. Keep target columns explicit so shared names such
	// as timestamp_ms are never rejected as ambiguous.
	return strings.NewReplacer(
		"\n\t\traw_event_id = e.id,", "\n\t\tledger.raw_event_id = e.id,",
		"\n\t\ttimestamp_ms = e.timestamp_ms,", "\n\t\tledger.timestamp_ms = e.timestamp_ms,",
		"\n\t\tbucket_ms = e.timestamp_ms", "\n\t\tledger.bucket_ms = e.timestamp_ms",
		"\n\t\taggregate_schema_version = ?", "\n\t\tledger.aggregate_schema_version = ?",
		"\n\t\taggregate_structure_revision = ?", "\n\t\tledger.aggregate_structure_revision = ?",
		"\n\t\tupdated_at_ms = ?", "\n\t\tledger.updated_at_ms = ?",
	).Replace(query)
}

// MySQL places a WITH clause used by INSERT ... SELECT after the INSERT target
// and column list, while SQLite accepts it before INSERT.
func rewriteLeadingCTEInsert(query string) string {
	trimmedStart := len(query) - len(strings.TrimLeft(query, " \t\r\n"))
	if !strings.HasPrefix(strings.ToLower(query[trimmedStart:]), "with ") {
		return query
	}
	depth, quoted, insertAt := 0, byte(0), -1
	lower := strings.ToLower(query)
	for index := trimmedStart; index < len(query); index++ {
		char := query[index]
		if quoted != 0 {
			if char == quoted {
				if index+1 < len(query) && query[index+1] == quoted {
					index++
					continue
				}
				quoted = 0
			}
			continue
		}
		switch char {
		case '\'', '"':
			quoted = char
		case '(':
			depth++
		case ')':
			depth--
		default:
			if depth == 0 && strings.HasPrefix(lower[index:], "insert into ") {
				insertAt = index
				index = len(query)
			}
		}
	}
	if insertAt < 0 {
		return query
	}
	open := strings.Index(query[insertAt:], "(")
	if open < 0 {
		return query
	}
	open += insertAt
	depth, quoted = 1, 0
	closeAt := -1
	for index := open + 1; index < len(query); index++ {
		char := query[index]
		if quoted != 0 {
			if char == quoted {
				quoted = 0
			}
			continue
		}
		switch char {
		case '\'', '"':
			quoted = char
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				closeAt = index
				index = len(query)
			}
		}
	}
	if closeAt < 0 {
		return query
	}
	return query[:trimmedStart] + query[insertAt:closeAt+1] + "\n" +
		query[trimmedStart:insertAt] + query[closeAt+1:]
}

// replaceScalarFunction changes only calls with two or more top-level
// arguments. Aggregate min/max calls have one argument and are preserved.
func replaceScalarFunction(query, source, target string) string {
	marker := source + "("
	for offset := 0; offset < len(query); {
		relative := strings.Index(strings.ToLower(query[offset:]), marker)
		if relative < 0 {
			return query
		}
		start := offset + relative
		if start > 0 && isIdentifierByte(query[start-1]) {
			offset = start + len(marker)
			continue
		}
		argumentStart := start + len(marker)
		depth, end, comma := 1, -1, false
		quoted := byte(0)
		for index := argumentStart; index < len(query); index++ {
			char := query[index]
			if quoted != 0 {
				if char == quoted {
					if index+1 < len(query) && query[index+1] == quoted {
						index++
						continue
					}
					quoted = 0
				}
				continue
			}
			switch char {
			case '\'', '"':
				quoted = char
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = index
					index = len(query)
				}
			case ',':
				if depth == 1 {
					comma = true
				}
			}
		}
		if end < 0 {
			return query
		}
		if comma {
			query = query[:start] + target + query[start+len(source):]
			offset = start + len(target) + 1
		} else {
			offset = end + 1
		}
	}
	return query
}

func isIdentifierByte(value byte) bool {
	return value == '_' || value >= '0' && value <= '9' ||
		value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func replaceAnalyticsModelCalls(query string) string {
	const marker = "cpamp_analytics_model("
	for {
		start := strings.Index(query, marker)
		if start < 0 {
			return query
		}
		argumentStart := start + len(marker)
		depth, end := 1, -1
		quoted := false
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
		query = query[:start] + mysqlAnalyticsModelExpression(query[argumentStart:end]) + query[end+1:]
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
		"when " + label + " <> '' then " + key("label", provider, label) + " " +
		"else '' end"
}

// IsRetryableWriteConflict recognizes only transaction conflicts which are
// safe for a repository to replay from the beginning. Duplicate keys are
// included because concurrent application-level upserts can both observe a
// missing row before the target unique identity becomes visible.
func (d Dialect) IsRetryableWriteConflict(err error) bool {
	if !d.IsMySQL() || err == nil {
		return false
	}
	var mysqlErr *mysqldriver.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	switch mysqlErr.Number {
	case 1062, 1205, 1213:
		return true
	default:
		return false
	}
}

func quoteSQLite(value string) string { return `"` + value + `"` }

func quoteMySQL(value string) string { return "`" + value + "`" }
