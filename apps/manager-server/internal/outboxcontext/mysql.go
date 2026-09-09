package outboxcontext

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
)

type authorityBackend int32

const (
	authorityBackendSQLite authorityBackend = iota
	authorityBackendMySQL

	mysqlJournalProviderContract = "cpamp-mysql-authority-context-v1"
)

// PrepareMySQLJournal installs an inert reverse-Outbox contract. It is safe
// while SQLite is the write primary: every trigger first reads the durable
// routing row and becomes a no-op unless MySQL is the fenced primary. DDL is
// deliberately forbidden after MySQL is primary so a live journal can never
// be dropped and recreated underneath authoritative writes.
func PrepareMySQLJournal(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("mysql journal requires a database")
	}
	repository := databasemigration.NewSQLRepository(db, databasemigration.DialectMySQL)
	if err := repository.EnsureSchema(ctx); err != nil {
		return err
	}
	routing, err := repository.Routing(ctx)
	if err != nil {
		return err
	}
	if routing.WritePrimary == databasemigration.BackendMySQL {
		return errors.New("cannot install mysql journal DDL while mysql is the write primary")
	}
	if err := ensureMySQLAuthorityCounter(ctx, db); err != nil {
		return err
	}
	manifest := canonicalMigrationManifest()
	byName := make(map[string]databasemigration.TableSpec, len(manifest))
	for _, table := range manifest.Tables() {
		byName[table.Name] = table
	}
	for _, table := range schema.Current().AuthoritativeTables() {
		statements, buildErr := buildMySQLJournalTriggers(table)
		if buildErr != nil {
			return buildErr
		}
		for _, name := range mysqlJournalTriggerNames(table.Name) {
			if _, err := db.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+mysqlQuoteIdentifier(name)); err != nil {
				return fmt.Errorf("drop stale mysql journal trigger %s: %w", name, err)
			}
		}
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("install mysql journal trigger for %s: %w", table.Name, err)
			}
		}
		spec, ok := byName[table.Name]
		if !ok {
			return fmt.Errorf("mysql journal manifest omits authoritative table %s", table.Name)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO database_journal_contracts
			(table_name,manifest_hash,provider_contract,installed_at_ms)
			VALUES(?,?,?,CAST(UNIX_TIMESTAMP(CURRENT_TIMESTAMP(3))*1000 AS SIGNED))
			ON DUPLICATE KEY UPDATE manifest_hash=VALUES(manifest_hash),
			provider_contract=VALUES(provider_contract),installed_at_ms=VALUES(installed_at_ms)`,
			table.Name, databasemigration.ManifestSchemaHash(spec), mysqlJournalProviderContract); err != nil {
			return fmt.Errorf("record mysql journal contract for %s: %w", table.Name, err)
		}
	}
	return AuditMySQLJournal(ctx, db)
}

// EnableMySQL activates application transaction contexts only after the
// durable routing epoch and the complete trigger contract agree. It performs
// no DDL and therefore is suitable for the fenced failover critical section.
func EnableMySQL(ctx context.Context, db *sql.DB, epoch int64) error {
	if db == nil {
		return errors.New("mysql authority context requires a database")
	}
	if epoch <= 0 {
		return errors.New("mysql authority context epoch must be positive")
	}
	state := stateFor(db)
	state.enabled.Store(false)
	if err := AuditMySQLJournal(ctx, db); err != nil {
		return err
	}
	var primary string
	var actualEpoch int64
	if err := db.QueryRowContext(ctx, `SELECT write_primary,epoch FROM database_routing_state WHERE id=1`).Scan(
		&primary, &actualEpoch,
	); err != nil {
		return fmt.Errorf("read mysql routing fence: %w", err)
	}
	if primary != string(databasemigration.BackendMySQL) || actualEpoch != epoch {
		return fmt.Errorf("mysql routing fence is %s epoch %d, want mysql epoch %d", primary, actualEpoch, epoch)
	}
	state.backend.Store(int32(authorityBackendMySQL))
	state.epoch.Store(epoch)
	state.enabled.Store(true)
	return nil
}

// DisableMySQL prevents new repository transactions from claiming a MySQL
// authority context. Durable triggers remain installed and route-gated so
// forward SQLite replication can continue without DDL churn.
func DisableMySQL(db *sql.DB) {
	if db == nil {
		return
	}
	state := stateFor(db)
	state.enabled.Store(false)
	state.epoch.Store(0)
}

func AuditMySQLJournal(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return errors.New("mysql journal audit requires a database")
	}
	manifest := canonicalMigrationManifest()
	byName := make(map[string]databasemigration.TableSpec, len(manifest))
	for _, table := range manifest.Tables() {
		byName[table.Name] = table
	}
	var missing []string
	for _, table := range schema.Current().AuthoritativeTables() {
		spec, ok := byName[table.Name]
		if !ok {
			missing = append(missing, table.Name+":manifest")
			continue
		}
		var contracts int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM database_journal_contracts
			WHERE table_name=? AND manifest_hash=? AND provider_contract=?`, table.Name,
			databasemigration.ManifestSchemaHash(spec), mysqlJournalProviderContract).Scan(&contracts); err != nil {
			return err
		}
		if contracts != 1 {
			missing = append(missing, table.Name+":contract")
		}
		for _, name := range mysqlJournalTriggerNames(table.Name) {
			var count int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.triggers
				WHERE trigger_schema=DATABASE() AND trigger_name=?`, name).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				missing = append(missing, table.Name+":"+name)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: missing %s", ErrJournalNotReady, strings.Join(missing, ", "))
	}
	var counters int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM database_mysql_authority_counter WHERE id=1`).Scan(&counters); err != nil {
		return err
	}
	if counters != 1 {
		return fmt.Errorf("%w: mysql authority row-version counter is missing", ErrJournalNotReady)
	}
	return nil
}

func ensureMySQLAuthorityCounter(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS database_mysql_authority_counter (
		id INT NOT NULL PRIMARY KEY,
		last_row_version BIGINT NOT NULL
	) ENGINE=InnoDB`); err != nil {
		return fmt.Errorf("create mysql authority row-version counter: %w", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO database_mysql_authority_counter(id,last_row_version)
		SELECT 1,0 WHERE NOT EXISTS(SELECT 1 FROM database_mysql_authority_counter WHERE id=1)`); err != nil {
		return fmt.Errorf("initialize mysql authority row-version counter: %w", err)
	}
	return nil
}

func beginMySQLAuthority(ctx context.Context, tx *sql.Tx, state *dbState) (*Tx, error) {
	epoch := state.epoch.Load()
	var primary string
	var actualEpoch int64
	if err := tx.QueryRowContext(ctx, `SELECT write_primary,epoch FROM database_routing_state
		WHERE id=1 FOR UPDATE`).Scan(&primary, &actualEpoch); err != nil {
		_ = tx.Rollback()
		state.end()
		return nil, fmt.Errorf("read mysql authority epoch: %w", err)
	}
	if primary != string(databasemigration.BackendMySQL) || actualEpoch != epoch {
		_ = tx.Rollback()
		state.end()
		return nil, fmt.Errorf("mysql authority epoch fenced: expected %d, found %s epoch %d",
			epoch, primary, actualEpoch)
	}
	groupID, err := randomGroupID()
	if err != nil {
		_ = tx.Rollback()
		state.end()
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SET @cpamp_transaction_id=?,
		@cpamp_source_epoch=?,@cpamp_sequence=0`, groupID, epoch); err != nil {
		_ = tx.Rollback()
		state.end()
		return nil, fmt.Errorf("establish mysql authority transaction context: %w", err)
	}
	return &Tx{Tx: tx, journal: true, state: state, backend: authorityBackendMySQL}, nil
}

func clearMySQLAuthorityContext(tx *sql.Tx) error {
	if tx == nil {
		return sql.ErrTxDone
	}
	if _, err := tx.Exec(`SET @cpamp_transaction_id=NULL,
		@cpamp_source_epoch=NULL,@cpamp_sequence=NULL`); err != nil {
		return fmt.Errorf("clear mysql authority transaction context: %w", err)
	}
	return nil
}

func buildMySQLJournalTriggers(table schema.Table) ([]string, error) {
	names := mysqlJournalTriggerNames(table.Name)
	insert, err := mysqlTriggerBody(table, databasemigration.OperationInsert, "NEW")
	if err != nil {
		return nil, err
	}
	deleted, err := mysqlTriggerBody(table, databasemigration.OperationDelete, "OLD")
	if err != nil {
		return nil, err
	}
	updated, err := mysqlUpdateTriggerBody(table)
	if err != nil {
		return nil, err
	}
	quotedTable := mysqlQuoteIdentifier(table.Name)
	return []string{
		"CREATE TRIGGER " + mysqlQuoteIdentifier(names[0]) + " AFTER INSERT ON " + quotedTable +
			" FOR EACH ROW " + insert,
		"CREATE TRIGGER " + mysqlQuoteIdentifier(names[1]) + " AFTER DELETE ON " + quotedTable +
			" FOR EACH ROW " + deleted,
		"CREATE TRIGGER " + mysqlQuoteIdentifier(names[2]) + " AFTER UPDATE ON " + quotedTable +
			" FOR EACH ROW " + updated,
	}, nil
}

func mysqlTriggerBody(table schema.Table, operation databasemigration.Operation, alias string) (string, error) {
	emission, err := mysqlMutationEmission(table, operation, alias)
	if err != nil {
		return "", err
	}
	return mysqlTriggerPreamble(emission), nil
}

func mysqlUpdateTriggerBody(table schema.Table) (string, error) {
	updated, err := mysqlMutationEmission(table, databasemigration.OperationUpdate, "NEW")
	if err != nil {
		return "", err
	}
	deleted, err := mysqlMutationEmission(table, databasemigration.OperationDelete, "OLD")
	if err != nil {
		return "", err
	}
	inserted, err := mysqlMutationEmission(table, databasemigration.OperationInsert, "NEW")
	if err != nil {
		return "", err
	}
	comparisons := make([]string, 0, len(table.PrimaryKey()))
	for _, column := range table.PrimaryKey() {
		quoted := mysqlQuoteIdentifier(column.Name)
		comparisons = append(comparisons, "NOT (OLD."+quoted+" <=> NEW."+quoted+")")
	}
	if len(comparisons) == 0 {
		return "", fmt.Errorf("authoritative table %s has no primary key", table.Name)
	}
	body := "IF " + strings.Join(comparisons, " OR ") + " THEN " + deleted + inserted +
		" ELSE " + updated + " END IF;"
	return mysqlTriggerPreamble(body), nil
}

func mysqlTriggerPreamble(emission string) string {
	return `BEGIN
		DECLARE cpamp_primary VARCHAR(16);
		DECLARE cpamp_epoch BIGINT;
		DECLARE cpamp_row_version BIGINT;
		DECLARE cpamp_sequence_no BIGINT;
		DECLARE cpamp_mutation_id CHAR(64);
		DECLARE cpamp_mutation_digest CHAR(64);
		DECLARE cpamp_primary_key LONGTEXT;
		DECLARE cpamp_payload LONGTEXT;
		SELECT write_primary,epoch INTO cpamp_primary,cpamp_epoch
			FROM database_routing_state WHERE id=1;
		IF cpamp_primary='mysql' THEN
			IF @cpamp_transaction_id IS NULL OR @cpamp_source_epoch IS NULL OR
				@cpamp_source_epoch<>cpamp_epoch THEN
				SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='mysql journal write epoch fenced';
			END IF;
			` + emission + `
		END IF;
	END`
}

func mysqlMutationEmission(table schema.Table, operation databasemigration.Operation, alias string) (string, error) {
	primaryKeyColumns := table.PrimaryKey()
	if len(primaryKeyColumns) == 0 {
		return "", fmt.Errorf("authoritative table %s has no primary key", table.Name)
	}
	primaryKey := mysqlTypedJSONObject(primaryKeyColumns, alias)
	payload := mysqlTypedJSONObject(table.Columns, alias)
	tableLiteral := mysqlQuoteString(table.Name)
	operationLiteral := mysqlQuoteString(string(operation))
	schemaVersion := schema.Current().Version
	sequenceText := "CAST(cpamp_sequence_no AS CHAR CHARACTER SET ascii)"
	epochText := "CAST(@cpamp_source_epoch AS CHAR CHARACTER SET ascii)"
	schemaText := mysqlQuoteString(strconv.Itoa(schemaVersion))
	rowVersionText := "CAST(cpamp_row_version AS CHAR CHARACTER SET ascii)"
	mutationDigest := mysqlMutationDigestExpression([]string{
		"cpamp_mutation_id", "@cpamp_transaction_id", sequenceText,
		mysqlQuoteString("mysql"), mysqlQuoteString("sqlite"), epochText,
		tableLiteral, operationLiteral, "cpamp_primary_key", "cpamp_payload",
		schemaText, rowVersionText,
	})
	return fmt.Sprintf(`
		SET cpamp_sequence_no=COALESCE(@cpamp_sequence,0);
		UPDATE database_mysql_authority_counter SET last_row_version=last_row_version+1 WHERE id=1;
		SELECT last_row_version INTO cpamp_row_version FROM database_mysql_authority_counter WHERE id=1;
		SET cpamp_primary_key=%s;
		SET cpamp_payload=%s;
		SET cpamp_mutation_id=SHA2(CONCAT(@cpamp_transaction_id,':',cpamp_sequence_no),256);
		SET cpamp_mutation_digest=%s;
		INSERT INTO database_outbox (mutation_id,mutation_digest,transaction_id,sequence_no,
			source_backend,target_backend,source_epoch,table_name,mutation_operation,
			primary_key_json,payload_json,schema_version,row_version,payload_bytes,
			created_at_ms,applied_at_ms)
		VALUES (cpamp_mutation_id,cpamp_mutation_digest,@cpamp_transaction_id,cpamp_sequence_no,
			'mysql','sqlite',@cpamp_source_epoch,%s,%s,cpamp_primary_key,cpamp_payload,
			%d,cpamp_row_version,OCTET_LENGTH(cpamp_primary_key)+OCTET_LENGTH(cpamp_payload),
			CAST(UNIX_TIMESTAMP(CURRENT_TIMESTAMP(3))*1000 AS SIGNED),0);
		SET @cpamp_sequence=cpamp_sequence_no+1;
	`, primaryKey, payload, mutationDigest, tableLiteral, operationLiteral, schemaVersion), nil
}

func mysqlTypedJSONObject(columns []schema.Column, alias string) string {
	// database/sql's canonical mutation digest sorts every JSON object key.
	// Build that representation directly in the trigger instead of relying on
	// MySQL JSON_OBJECT's serialization order or whitespace. The exact text is
	// both persisted and hashed, so the target can verify the digest without a
	// dialect-specific normalization pass.
	ordered := append([]schema.Column(nil), columns...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	parts := make([]string, 0, len(ordered)*2+1)
	parts = append(parts, mysqlQuoteString("{"))
	for index, column := range ordered {
		if index != 0 {
			parts = append(parts, mysqlQuoteString(","))
		}
		value := alias + "." + mysqlQuoteIdentifier(column.Name)
		var nonNull string
		switch column.Kind {
		case schema.KindInteger:
			nonNull = "CONCAT(" + mysqlQuoteString(`{"type":"integer","value":`) + "," +
				mysqlCanonicalJSONString("CAST("+value+" AS CHAR CHARACTER SET ascii)") + "," + mysqlQuoteString("}") + ")"
		case schema.KindReal:
			nonNull = "CONCAT(" + mysqlQuoteString(`{"type":"real","value":`) + "," +
				mysqlCanonicalJSONString("CAST("+value+" AS CHAR CHARACTER SET ascii)") + "," + mysqlQuoteString("}") + ")"
		case schema.KindBlob:
			nonNull = "CONCAT(" + mysqlQuoteString(`{"hex":`) + "," +
				mysqlCanonicalJSONString("HEX("+value+")") + "," + mysqlQuoteString(`,"type":"bytes"}`) + ")"
		default:
			nonNull = "CONCAT(" + mysqlQuoteString(`{"type":"text","value":`) + "," +
				mysqlCanonicalJSONString(value) + "," + mysqlQuoteString("}") + ")"
		}
		typed := "IF(" + value + " IS NULL," + mysqlQuoteString(`{"type":"null"}`) + "," + nonNull + ")"
		parts = append(parts, mysqlQuoteString(strconv.Quote(column.Name)+":"), typed)
	}
	parts = append(parts, mysqlQuoteString("}"))
	return "CONCAT(" + strings.Join(parts, ",") + ")"
}

// mysqlCanonicalJSONString matches encoding/json's string escaping for valid
// UTF-8 strings. Source preflight rejects invalid UTF-8 before MySQL can become
// authoritative. JSON_QUOTE handles control characters, quotes and slashes;
// encoding/json additionally escapes HTML-sensitive runes and U+2028/U+2029.
func mysqlCanonicalJSONString(expression string) string {
	replacement := func(suffix string) string { return "CONCAT(CHAR(92)," + mysqlQuoteString(suffix) + ")" }
	return "REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(JSON_QUOTE(" + expression + ")," +
		mysqlQuoteString("&") + "," + replacement("u0026") + ")," +
		mysqlQuoteString("<") + "," + replacement("u003c") + ")," +
		mysqlQuoteString(">") + "," + replacement("u003e") + ")," +
		"CONVERT(0xE280A8 USING utf8mb4)," + replacement("u2028") + ")," +
		"CONVERT(0xE280A9 USING utf8mb4)," + replacement("u2029") + ")"
}

func mysqlMutationDigestExpression(fields []string) string {
	encoded := make([]string, 0, len(fields))
	for _, field := range fields {
		encoded = append(encoded, mysqlLengthPrefixedDigestField(field))
	}
	return "SHA2(CONCAT(" + strings.Join(encoded, ",") + "),256)"
}

func mysqlLengthPrefixedDigestField(expression string) string {
	bytes := "CAST(" + expression + " AS BINARY)"
	return "CONCAT(UNHEX(LPAD(HEX(OCTET_LENGTH(" + bytes + ")),16,'0'))," + bytes + ")"
}

func mysqlJournalTriggerNames(table string) []string {
	digest := sha256.Sum256([]byte(table))
	prefix := "cpamp_mysql_journal_" + hex.EncodeToString(digest[:6])
	return []string{prefix + "_insert", prefix + "_delete", prefix + "_update"}
}

func mysqlQuoteIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func mysqlQuoteString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
