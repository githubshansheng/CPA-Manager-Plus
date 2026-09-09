package databasemigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SQLiteJournalSQLProvider connects SQLite triggers to application-owned
// transaction context. Every expression must be stable for the duration of an
// authoritative SQL transaction; SequenceExpression must advance exactly once
// per emitted mutation. DigestExpression receives the envelope alias and must
// calculate the same logical mutation digest used by the target.
//
// A provider is intentionally mandatory: SQLite itself has no reliable
// cross-table transaction identifier, so installing triggers without one
// would falsely claim atomic transaction grouping.
type SQLiteJournalSQLProvider interface {
	ContractID() string
	TransactionIDExpression() string
	MutationIDExpression() string
	SequenceExpression() string
	EpochExpression() string
	RowVersionExpression(table string, operation Operation, rowAlias string) string
	NowMSExpression() string
	DigestExpression(envelopeAlias string) string
}

type SQLiteJournalInstaller struct {
	DB            *sql.DB
	Manifest      Manifest
	Provider      SQLiteJournalSQLProvider
	SchemaVersion int
	Now           Clock
}

func (i SQLiteJournalInstaller) Install(ctx context.Context) error {
	if i.DB == nil {
		return errors.New("sqlite journal installer requires a database")
	}
	if err := ValidateManifest(i.Manifest); err != nil {
		return err
	}
	if err := validateJournalProvider(i.Provider); err != nil {
		return err
	}
	if i.SchemaVersion <= 0 {
		return errors.New("sqlite journal schema version must be positive")
	}
	if err := ValidateSQLiteManifestParity(ctx, i.DB, i.Manifest); err != nil {
		return err
	}
	now := timeNow(i.Now).UnixMilli()
	tx, err := i.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range i.Manifest.Tables() {
		if table.Derived {
			continue
		}
		manifestHash := ManifestSchemaHash(table)
		var storedHash, storedProvider string
		err := tx.QueryRowContext(ctx, `SELECT manifest_hash, provider_contract
			FROM database_journal_contracts WHERE table_name = ?`, table.Name).Scan(&storedHash, &storedProvider)
		if err == nil {
			if storedHash != manifestHash || storedProvider != i.Provider.ContractID() {
				return fmt.Errorf("journal contract for table %q changed; explicit coordinated reinstall is required", table.Name)
			}
			if err := ensureJournalTriggersExist(ctx, tx, table.Name); err != nil {
				return err
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		statements, err := buildJournalTriggers(table, i.Provider, i.SchemaVersion)
		if err != nil {
			return err
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("install journal trigger for table %q: %w", table.Name, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO database_journal_contracts
			(table_name, manifest_hash, provider_contract, installed_at_ms) VALUES (?, ?, ?, ?)`,
			table.Name, manifestHash, i.Provider.ContractID(), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ValidateSQLiteManifestParity(ctx context.Context, db *sql.DB, manifest Manifest) error {
	if db == nil {
		return errors.New("sqlite manifest validation requires a database")
	}
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	for _, table := range manifest.Tables() {
		if table.Derived {
			continue
		}
		rows, err := db.QueryContext(ctx, `PRAGMA table_xinfo(`+quoteSQLiteIdentifier(table.Name)+`)`)
		if err != nil {
			return fmt.Errorf("inspect sqlite table %q: %w", table.Name, err)
		}
		type sqliteColumn struct {
			name         string
			declaredType string
			notNull      bool
			defaultSQL   *string
			pk           int
		}
		actual := make(map[string]sqliteColumn)
		for rows.Next() {
			var cid, notNull, pk, hidden int
			var name, declaredType string
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &pk, &hidden); err != nil {
				rows.Close()
				return err
			}
			if hidden == 0 {
				var defaultSQL *string
				if defaultValue.Valid {
					value := defaultValue.String
					defaultSQL = &value
				}
				actual[name] = sqliteColumn{name: name, declaredType: declaredType,
					notNull: notNull != 0, defaultSQL: defaultSQL, pk: pk}
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(actual) == 0 {
			return fmt.Errorf("authoritative sqlite table %q does not exist", table.Name)
		}
		if len(actual) != len(table.Columns) {
			return fmt.Errorf("sqlite table %q has %d writable columns but manifest has %d; journaling would omit fields",
				table.Name, len(actual), len(table.Columns))
		}
		pkOrder := make(map[string]int, len(table.PrimaryKey))
		for index, key := range table.PrimaryKey {
			pkOrder[key] = index + 1
		}
		for _, column := range table.Columns {
			found, exists := actual[column.Name]
			if !exists {
				return fmt.Errorf("sqlite table %q is missing manifest column %q", table.Name, column.Name)
			}
			if wantPK := pkOrder[column.Name]; found.pk != wantPK {
				return fmt.Errorf("sqlite table %q column %q primary-key position is %d, want %d", table.Name, column.Name, found.pk, wantPK)
			}
			// SQLite reports INTEGER PRIMARY KEY as notnull=0 even though NULL is
			// replaced by an allocated key. Treat all PK members as non-null.
			logicalNotNull := found.notNull || found.pk > 0
			if logicalNotNull == column.Nullable {
				return fmt.Errorf("sqlite table %q column %q NULL semantics differ from manifest", table.Name, column.Name)
			}
			if sqliteTypeAffinity(found.declaredType) != logicalTypeAffinity(column.LogicalType) {
				return fmt.Errorf("sqlite table %q column %q type %q is incompatible with logical type %q",
					table.Name, column.Name, found.declaredType, column.LogicalType)
			}
			if !equalOptionalString(found.defaultSQL, column.DefaultSQL) {
				return fmt.Errorf("sqlite table %q column %q default differs from manifest", table.Name, column.Name)
			}
		}
	}
	return nil
}

func sqliteTypeAffinity(declaredType string) string {
	value := strings.ToUpper(declaredType)
	switch {
	case strings.Contains(value, "INT"):
		return "integer"
	case strings.Contains(value, "CHAR"), strings.Contains(value, "CLOB"), strings.Contains(value, "TEXT"):
		return "text"
	case strings.Contains(value, "BLOB"), value == "":
		return "blob"
	case strings.Contains(value, "REAL"), strings.Contains(value, "FLOA"), strings.Contains(value, "DOUB"):
		return "real"
	default:
		return "numeric"
	}
}

func logicalTypeAffinity(logicalType string) string {
	value := strings.ToLower(logicalType)
	switch {
	case strings.Contains(value, "int"):
		return "integer"
	case strings.Contains(value, "text"), strings.Contains(value, "string"), strings.Contains(value, "json"), strings.Contains(value, "char"):
		return "text"
	case strings.Contains(value, "blob"), strings.Contains(value, "byte"), strings.Contains(value, "binary"):
		return "blob"
	case strings.Contains(value, "real"), strings.Contains(value, "float"), strings.Contains(value, "double"):
		return "real"
	default:
		return "numeric"
	}
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func buildJournalTriggers(table TableSpec, provider SQLiteJournalSQLProvider, schemaVersion int) ([]string, error) {
	insertBody, err := buildJournalInsert(table, provider, schemaVersion, OperationInsert, "NEW")
	if err != nil {
		return nil, err
	}
	deleteBody, err := buildJournalInsert(table, provider, schemaVersion, OperationDelete, "OLD")
	if err != nil {
		return nil, err
	}
	updateBody, err := buildJournalInsert(table, provider, schemaVersion, OperationUpdate, "NEW")
	if err != nil {
		return nil, err
	}
	pkEqual := primaryKeyComparison(table, true)
	pkChanged := primaryKeyComparison(table, false)
	quotedTable := quoteSQLiteIdentifier(table.Name)
	names := JournalTriggerNames(table.Name)
	return []string{
		fmt.Sprintf(`CREATE TRIGGER %s AFTER INSERT ON %s BEGIN %s END`, quoteSQLiteIdentifier(names[0]), quotedTable, insertBody),
		fmt.Sprintf(`CREATE TRIGGER %s AFTER DELETE ON %s BEGIN %s END`, quoteSQLiteIdentifier(names[1]), quotedTable, deleteBody),
		fmt.Sprintf(`CREATE TRIGGER %s AFTER UPDATE ON %s WHEN %s BEGIN %s END`, quoteSQLiteIdentifier(names[2]), quotedTable, pkEqual, updateBody),
		fmt.Sprintf(`CREATE TRIGGER %s AFTER UPDATE ON %s WHEN %s BEGIN %s %s END`, quoteSQLiteIdentifier(names[3]), quotedTable, pkChanged, deleteBody, insertBody),
	}, nil
}

func buildJournalInsert(table TableSpec, provider SQLiteJournalSQLProvider, schemaVersion int, operation Operation, alias string) (string, error) {
	primaryKey := sqliteTypedJSONObject(table, table.PrimaryKey, alias)
	columns := make([]string, 0, len(table.Columns))
	for _, column := range table.Columns {
		columns = append(columns, column.Name)
	}
	payload := sqliteTypedJSONObject(table, columns, alias)
	expressions := []string{
		provider.MutationIDExpression(), provider.TransactionIDExpression(), provider.SequenceExpression(),
		provider.EpochExpression(), provider.RowVersionExpression(table.Name, operation, alias), provider.NowMSExpression(),
	}
	for _, expression := range expressions {
		if !safeProviderExpression(expression) {
			return "", fmt.Errorf("journal provider returned an unsafe empty or multi-statement expression")
		}
	}
	digest := provider.DigestExpression("envelope")
	if !safeProviderExpression(digest) {
		return "", errors.New("journal provider returned an unsafe digest expression")
	}
	return fmt.Sprintf(`INSERT INTO database_outbox (mutation_id, mutation_digest,
		transaction_id, sequence_no, source_backend, target_backend, source_epoch, table_name,
		mutation_operation, primary_key_json, payload_json, schema_version, row_version,
		payload_bytes, created_at_ms, applied_at_ms)
		SELECT envelope.mutation_id, %s, envelope.transaction_id, envelope.sequence_no,
		'sqlite', 'mysql', envelope.source_epoch, %s, %s, envelope.primary_key_json,
		envelope.payload_json, %d, envelope.row_version,
		length(envelope.primary_key_json) + length(envelope.payload_json), envelope.created_at_ms, 0
		FROM (SELECT %s AS mutation_id, %s AS transaction_id, %s AS sequence_no,
		%s AS source_epoch, %s AS row_version, %s AS primary_key_json,
		%s AS payload_json, %s AS created_at_ms) AS envelope
		WHERE EXISTS (SELECT 1 FROM database_routing_state WHERE id = 1
		AND write_primary = 'sqlite' AND epoch = envelope.source_epoch);
		SELECT CASE WHEN changes() != 1 THEN RAISE(ABORT, 'sqlite journal write epoch fenced') END;`,
		digest, quoteSQLiteString(table.Name), quoteSQLiteString(string(operation)), schemaVersion,
		expressions[0], expressions[1], expressions[2], expressions[3], expressions[4], primaryKey,
		payload, expressions[5]), nil
}

func sqliteTypedJSONObject(table TableSpec, wanted []string, alias string) string {
	byName := make(map[string]ColumnSpec, len(table.Columns))
	for _, column := range table.Columns {
		byName[column.Name] = column
	}
	parts := make([]string, 0, len(wanted)*2)
	for _, name := range wanted {
		column := byName[name]
		value := alias + "." + quoteSQLiteIdentifier(column.Name)
		typed := fmt.Sprintf(`json(CASE typeof(%s)
			WHEN 'null' THEN json_object('type','null')
			WHEN 'blob' THEN json_object('type','bytes','hex',hex(%s))
			WHEN 'integer' THEN json_object('type','integer','value',CAST(%s AS TEXT))
			WHEN 'real' THEN json_object('type','real','value',printf('%%!.17g',%s))
			ELSE json_object('type','text','value',%s) END)`, value, value, value, value, value)
		parts = append(parts, quoteSQLiteString(name), typed)
	}
	return "json_object(" + strings.Join(parts, ",") + ")"
}

func primaryKeyComparison(table TableSpec, equal bool) string {
	parts := make([]string, 0, len(table.PrimaryKey))
	for _, key := range table.PrimaryKey {
		operator := "IS"
		if !equal {
			operator = "IS NOT"
		}
		parts = append(parts, "OLD."+quoteSQLiteIdentifier(key)+" "+operator+" NEW."+quoteSQLiteIdentifier(key))
	}
	join := " AND "
	if !equal {
		join = " OR "
	}
	return strings.Join(parts, join)
}

func JournalTriggerNames(table string) []string {
	digest := sha256.Sum256([]byte(table))
	prefix := "cpamp_journal_" + hex.EncodeToString(digest[:6])
	return []string{prefix + "_insert", prefix + "_delete", prefix + "_update", prefix + "_update_pk"}
}

func ensureJournalTriggersExist(ctx context.Context, tx *sql.Tx, table string) error {
	for _, name := range JournalTriggerNames(table) {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
			WHERE type = 'trigger' AND name = ?`, name).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("journal contract exists for table %q but trigger %q is missing", table, name)
		}
	}
	return nil
}

func validateJournalProvider(provider SQLiteJournalSQLProvider) error {
	if provider == nil || provider.ContractID() == "" {
		return errors.New("sqlite journal requires an application transaction GroupID/sequence provider")
	}
	expressions := []string{provider.TransactionIDExpression(), provider.MutationIDExpression(),
		provider.SequenceExpression(), provider.EpochExpression(), provider.NowMSExpression(),
		provider.RowVersionExpression("contract_check", OperationInsert, "NEW"), provider.DigestExpression("envelope")}
	for _, expression := range expressions {
		if !safeProviderExpression(expression) {
			return errors.New("sqlite journal provider returned an unsafe empty or multi-statement expression")
		}
	}
	return nil
}

var providerCommentPattern = regexp.MustCompile(`(?s)--|/\*|\*/`)

func safeProviderExpression(expression string) bool {
	expression = strings.TrimSpace(expression)
	return expression != "" && !strings.Contains(expression, ";") && !providerCommentPattern.MatchString(expression)
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteSQLiteString(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func timeNow(clock Clock) time.Time {
	if clock == nil {
		return time.Now()
	}
	return clock()
}
