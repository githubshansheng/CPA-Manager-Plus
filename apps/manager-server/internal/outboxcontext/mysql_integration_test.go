package outboxcontext

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	dbmysql "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
)

// TestMySQLAuthorityJournalIntegration is deliberately opt-in because it
// installs the complete trigger contract and temporarily changes the durable
// write-primary fence in the configured integration schema. The test holds a
// schema-scoped advisory lock, restores the original routing values, and only
// deletes rows bearing its cryptographically unique fixture token.
func TestMySQLAuthorityJournalIntegration(t *testing.T) {
	if os.Getenv("CPAMP_MYSQL_INTEGRATION") != "1" {
		t.Skip("set CPAMP_MYSQL_INTEGRATION=1 to run against supported MySQL 8.x (8.0.12 minimum)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	config := mysqlJournalIntegrationConfig(t)
	probe, err := dbmysql.Test(ctx, config)
	if err != nil {
		t.Fatalf("validate official MySQL integration target: %v", err)
	}
	if err := probe.RequireMigrationCapabilities(); err != nil {
		t.Fatalf("MySQL integration target permissions: %v", err)
	}
	db, err := dbmysql.Open(ctx, config)
	if err != nil {
		t.Fatalf("open MySQL integration target: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	lock := acquireMySQLJournalIntegrationLock(t, ctx, db, config.Database)
	t.Cleanup(func() { releaseMySQLJournalIntegrationLock(t, lock) })
	if err := schema.Ensure(ctx, db); err != nil {
		t.Fatalf("ensure MySQL schema: %v", err)
	}
	validation, err := schema.Validate(ctx, db)
	if err != nil {
		t.Fatalf("validate MySQL schema: %v", err)
	}
	if !validation.Valid {
		t.Fatalf("MySQL schema differences: %#v", validation.Differences)
	}

	migrations := databasemigration.NewSQLRepository(db, databasemigration.DialectMySQL)
	if err := migrations.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure migration schema: %v", err)
	}
	originalRouting, err := migrations.Routing(ctx)
	if err != nil {
		t.Fatalf("read original routing: %v", err)
	}
	fixture := newMySQLJournalFixture()
	t.Cleanup(func() {
		cleanupMySQLJournalIntegration(t, db, originalRouting, fixture)
	})

	// Trigger DDL is only legal while SQLite is the durable write primary.
	sqliteRouting := routeMySQLIntegration(t, ctx, migrations, databasemigration.RoutingChange{
		WritePrimary: databasemigration.BackendSQLite,
		BusinessRead: originalRouting.BusinessRead,
		SystemRead:   originalRouting.SystemRead,
	})
	DisableMySQL(db)
	if err := PrepareMySQLJournal(ctx, db); err != nil {
		t.Fatalf("prepare MySQL journal while SQLite is primary: %v", err)
	}
	if err := AuditMySQLJournal(ctx, db); err != nil {
		t.Fatalf("audit prepared MySQL journal: %v", err)
	}

	// The installed reverse-Outbox triggers must be inert before failover.
	if _, err := db.ExecContext(ctx, `INSERT INTO settings(`+"`key`"+`,value,updated_at_ms) VALUES(?,?,?)`,
		fixture.noopKey, "SQLite primary trigger no-op", fixture.timestampMS); err != nil {
		t.Fatalf("write MySQL cache while SQLite is primary: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM settings WHERE `+"`key`"+`=?`, fixture.noopKey); err != nil {
		t.Fatalf("remove SQLite-primary no-op fixture: %v", err)
	}
	assertFixtureOutboxCount(t, ctx, db, fixture.token, 0)

	mysqlRouting := compareAndSwapMySQLIntegration(t, ctx, migrations, sqliteRouting,
		databasemigration.RoutingChange{
			WritePrimary: databasemigration.BackendMySQL,
			BusinessRead: originalRouting.BusinessRead,
			SystemRead:   originalRouting.SystemRead,
		})
	if mysqlRouting.Epoch != sqliteRouting.Epoch+1 {
		t.Fatalf("MySQL cutover epoch=%d, want %d", mysqlRouting.Epoch, sqliteRouting.Epoch+1)
	}
	if err := EnableMySQL(ctx, db, mysqlRouting.Epoch); err != nil {
		t.Fatalf("enable MySQL authority context: %v", err)
	}

	longText := "首<&>\u2028\u2029\n\t\"\\尾-" + strings.Repeat("完整字段🔐", 2_000)
	rawJSON := `{"说明":"` + strings.Repeat("历史数据🔐<&>\u2028", 1_000) + `","空":""}`
	primaryTransaction := beginMySQLJournalFixtureTx(t, ctx, db, mysqlRouting.Epoch)
	defer primaryTransaction.tx.Rollback()
	fixture.transactions = append(fixture.transactions, primaryTransaction.id)
	if _, err := primaryTransaction.tx.ExecContext(ctx, `INSERT INTO settings(`+"`key`"+`,value,updated_at_ms) VALUES(?,?,?)`,
		fixture.settingKey, longText, fixture.timestampMS); err != nil {
		t.Fatalf("insert journaled setting: %v", err)
	}
	if _, err := primaryTransaction.tx.ExecContext(ctx, `INSERT INTO model_prices(
		model,prompt_per_1m,completion_per_1m,cache_per_1m,cache_read_per_1m,
		cache_creation_per_1m,prompt_configured,completion_configured,
		cache_read_configured,cache_creation_configured,source,source_model_id,
		raw_json,updated_at_ms,synced_at_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		fixture.modelKey, 1.25, 2.5, 0.125, 0.0, 9.75, 1, 0, 1, 0,
		nil, "", rawJSON, fixture.timestampMS+1, nil); err != nil {
		t.Fatalf("insert journaled model price: %v", err)
	}
	if err := primaryTransaction.tx.Commit(); err != nil {
		t.Fatalf("commit multi-table MySQL authority transaction: %v", err)
	}

	primaryRecords := readMySQLJournalRecords(t, ctx, db, primaryTransaction.id)
	if len(primaryRecords) != 2 {
		t.Fatalf("multi-table Outbox records=%d, want 2", len(primaryRecords))
	}
	assertMySQLJournalEnvelope(t, primaryRecords, mysqlRouting.Epoch,
		[]string{"settings", "model_prices"}, []databasemigration.Operation{
			databasemigration.OperationInsert, databasemigration.OperationInsert,
		})
	assertTypedJournalObject(t, "settings primary key", primaryRecords[0].primaryKey,
		map[string]expectedMySQLJournalValue{"key": textJournalValue(fixture.settingKey)})
	assertTypedJournalObject(t, "settings payload", primaryRecords[0].payload.String,
		map[string]expectedMySQLJournalValue{
			"key": textJournalValue(fixture.settingKey), "value": textJournalValue(longText),
			"updated_at_ms": integerJournalValue(fixture.timestampMS),
		})
	assertTypedJournalObject(t, "model_prices primary key", primaryRecords[1].primaryKey,
		map[string]expectedMySQLJournalValue{"model": textJournalValue(fixture.modelKey)})
	assertTypedJournalObject(t, "model_prices payload", primaryRecords[1].payload.String,
		map[string]expectedMySQLJournalValue{
			"model":         textJournalValue(fixture.modelKey),
			"prompt_per_1m": realJournalValue("1.25"), "completion_per_1m": realJournalValue("2.5"),
			"cache_per_1m": realJournalValue("0.125"), "cache_read_per_1m": realJournalValue("0"),
			"cache_creation_per_1m": realJournalValue("9.75"),
			"prompt_configured":     integerJournalValue(1), "completion_configured": integerJournalValue(0),
			"cache_read_configured": integerJournalValue(1), "cache_creation_configured": integerJournalValue(0),
			"source": nullJournalValue(), "source_model_id": textJournalValue(""),
			"raw_json": textJournalValue(rawJSON), "updated_at_ms": integerJournalValue(fixture.timestampMS + 1),
			"synced_at_ms": nullJournalValue(),
		})

	// Bump the durable epoch without refreshing the in-process authority state.
	// The stale state must be rejected even after MySQL becomes primary again.
	backToSQLite := compareAndSwapMySQLIntegration(t, ctx, migrations, mysqlRouting,
		databasemigration.RoutingChange{WritePrimary: databasemigration.BackendSQLite,
			BusinessRead: originalRouting.BusinessRead, SystemRead: originalRouting.SystemRead})
	newMySQLRouting := compareAndSwapMySQLIntegration(t, ctx, migrations, backToSQLite,
		databasemigration.RoutingChange{WritePrimary: databasemigration.BackendMySQL,
			BusinessRead: originalRouting.BusinessRead, SystemRead: originalRouting.SystemRead})
	if _, err := Begin(ctx, db, nil); err == nil || !strings.Contains(err.Error(), "epoch fenced") {
		t.Fatalf("old MySQL authority epoch was not rejected: %v", err)
	}
	if err := EnableMySQL(ctx, db, newMySQLRouting.Epoch); err != nil {
		t.Fatalf("enable refreshed MySQL authority epoch: %v", err)
	}

	// A raw authoritative write has no application transaction context and must
	// be rejected by the trigger rather than silently escaping the Outbox.
	_, directErr := db.ExecContext(ctx, `INSERT INTO settings(`+"`key`"+`,value,updated_at_ms) VALUES(?,?,?)`,
		fixture.directKey, "must be fenced", fixture.timestampMS)
	assertMySQLJournalFenceError(t, directErr)
	assertBusinessRowCount(t, ctx, db, "settings", "`key`", fixture.directKey, 0)

	rollbackTransaction := beginMySQLJournalFixtureTx(t, ctx, db, newMySQLRouting.Epoch)
	defer rollbackTransaction.tx.Rollback()
	fixture.transactions = append(fixture.transactions, rollbackTransaction.id)
	counterBeforeRollback := readMySQLAuthorityCounter(t, ctx, db)
	if _, err := rollbackTransaction.tx.ExecContext(ctx, `INSERT INTO settings(`+"`key`"+`,value,updated_at_ms) VALUES(?,?,?)`,
		fixture.rollbackKey, "rollback", fixture.timestampMS); err != nil {
		t.Fatalf("insert rollback setting: %v", err)
	}
	if _, err := rollbackTransaction.tx.ExecContext(ctx, `INSERT INTO model_prices(
		model,prompt_per_1m,completion_per_1m,cache_per_1m,cache_read_per_1m,
		cache_creation_per_1m,prompt_configured,completion_configured,
		cache_read_configured,cache_creation_configured,source,source_model_id,
		raw_json,updated_at_ms,synced_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		fixture.rollbackModelKey, 1, 1, 1, 1, 1, 1, 1, 1, 1, nil, nil, nil,
		fixture.timestampMS, nil); err != nil {
		t.Fatalf("insert rollback model price: %v", err)
	}
	if err := rollbackTransaction.tx.Rollback(); err != nil {
		t.Fatalf("rollback MySQL authority transaction: %v", err)
	}
	assertBusinessRowCount(t, ctx, db, "settings", "`key`", fixture.rollbackKey, 0)
	assertBusinessRowCount(t, ctx, db, "model_prices", "model", fixture.rollbackModelKey, 0)
	assertTransactionOutboxCount(t, ctx, db, rollbackTransaction.id, 0)
	if counterAfterRollback := readMySQLAuthorityCounter(t, ctx, db); counterAfterRollback != counterBeforeRollback {
		t.Fatalf("rollback advanced MySQL authority counter from %d to %d",
			counterBeforeRollback, counterAfterRollback)
	}

	pkTransaction := beginMySQLJournalFixtureTx(t, ctx, db, newMySQLRouting.Epoch)
	defer pkTransaction.tx.Rollback()
	fixture.transactions = append(fixture.transactions, pkTransaction.id)
	if _, err := pkTransaction.tx.ExecContext(ctx, `UPDATE settings SET `+"`key`"+`=?,value=?,updated_at_ms=? WHERE `+"`key`"+`=?`,
		fixture.updatedSettingKey, longText+"-更新", fixture.timestampMS+2, fixture.settingKey); err != nil {
		t.Fatalf("update settings primary key: %v", err)
	}
	if err := pkTransaction.tx.Commit(); err != nil {
		t.Fatalf("commit primary-key update: %v", err)
	}
	pkRecords := readMySQLJournalRecords(t, ctx, db, pkTransaction.id)
	if len(pkRecords) != 2 {
		t.Fatalf("primary-key update Outbox records=%d, want delete+insert", len(pkRecords))
	}
	assertMySQLJournalEnvelope(t, pkRecords, newMySQLRouting.Epoch,
		[]string{"settings", "settings"}, []databasemigration.Operation{
			databasemigration.OperationDelete, databasemigration.OperationInsert,
		})
	assertTypedJournalObject(t, "old settings primary key", pkRecords[0].primaryKey,
		map[string]expectedMySQLJournalValue{"key": textJournalValue(fixture.settingKey)})
	assertTypedJournalObject(t, "new settings primary key", pkRecords[1].primaryKey,
		map[string]expectedMySQLJournalValue{"key": textJournalValue(fixture.updatedSettingKey)})
	assertBusinessRowCount(t, ctx, db, "settings", "`key`", fixture.settingKey, 0)
	assertBusinessRowCount(t, ctx, db, "settings", "`key`", fixture.updatedSettingKey, 1)

	t.Logf("MySQL %s journal integration contract passed", probe.Version)
}

type mysqlJournalFixture struct {
	token, noopKey, settingKey, updatedSettingKey, directKey, rollbackKey string
	modelKey, rollbackModelKey                                            string
	timestampMS                                                           int64
	transactions                                                          []string
}

func newMySQLJournalFixture() *mysqlJournalFixture {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", time.Now().UnixNano(), os.Getpid())))
	token := "__cpamp_mysql_journal_it_" + hex.EncodeToString(digest[:10])
	return &mysqlJournalFixture{
		token: token, noopKey: token + "_noop", settingKey: token + "_setting",
		updatedSettingKey: token + "_setting_updated", directKey: token + "_direct",
		rollbackKey: token + "_rollback", modelKey: token + "_model",
		rollbackModelKey: token + "_rollback_model", timestampMS: time.Now().UnixMilli(),
	}
}

type mysqlJournalFixtureTx struct {
	tx *Tx
	id string
}

func beginMySQLJournalFixtureTx(t *testing.T, ctx context.Context, db *sql.DB, epoch int64) mysqlJournalFixtureTx {
	t.Helper()
	tx, err := Begin(ctx, db, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatalf("begin MySQL authority transaction: %v", err)
	}
	var transactionID string
	var sourceEpoch int64
	var sequence int
	if err := tx.QueryRowContext(ctx, `SELECT @cpamp_transaction_id,@cpamp_source_epoch,@cpamp_sequence`).Scan(
		&transactionID, &sourceEpoch, &sequence); err != nil {
		_ = tx.Rollback()
		t.Fatalf("inspect MySQL authority transaction context: %v", err)
	}
	if transactionID == "" || sourceEpoch != epoch || sequence != 0 {
		_ = tx.Rollback()
		t.Fatalf("MySQL authority context=(%q,%d,%d), want transaction/%d/0",
			transactionID, sourceEpoch, sequence, epoch)
	}
	return mysqlJournalFixtureTx{tx: tx, id: transactionID}
}

type mysqlJournalRecord struct {
	outboxID, sourceEpoch, rowVersion, payloadBytes, createdAtMS, appliedAtMS int64
	mutationID, digest, transactionID, source, target, table, operation       string
	sequence, schemaVersion                                                   int
	primaryKey                                                                string
	payload                                                                   sql.NullString
}

func readMySQLJournalRecords(t *testing.T, ctx context.Context, db *sql.DB, transactionID string) []mysqlJournalRecord {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT outbox_id,mutation_id,mutation_digest,transaction_id,
		sequence_no,source_backend,target_backend,source_epoch,table_name,mutation_operation,
		primary_key_json,payload_json,schema_version,row_version,payload_bytes,created_at_ms,applied_at_ms
		FROM database_outbox WHERE transaction_id=? ORDER BY sequence_no`, transactionID)
	if err != nil {
		t.Fatalf("read MySQL journal transaction %s: %v", transactionID, err)
	}
	defer rows.Close()
	var records []mysqlJournalRecord
	for rows.Next() {
		var record mysqlJournalRecord
		if err := rows.Scan(&record.outboxID, &record.mutationID, &record.digest, &record.transactionID,
			&record.sequence, &record.source, &record.target, &record.sourceEpoch, &record.table,
			&record.operation, &record.primaryKey, &record.payload, &record.schemaVersion,
			&record.rowVersion, &record.payloadBytes, &record.createdAtMS, &record.appliedAtMS); err != nil {
			t.Fatalf("scan MySQL journal transaction %s: %v", transactionID, err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate MySQL journal transaction %s: %v", transactionID, err)
	}
	return records
}

func assertMySQLJournalEnvelope(
	t *testing.T,
	records []mysqlJournalRecord,
	epoch int64,
	tables []string,
	operations []databasemigration.Operation,
) {
	t.Helper()
	if len(records) != len(tables) || len(records) != len(operations) {
		t.Fatalf("journal assertion dimensions records=%d tables=%d operations=%d",
			len(records), len(tables), len(operations))
	}
	for index, record := range records {
		if record.sequence != index || record.transactionID != records[0].transactionID ||
			record.source != string(databasemigration.BackendMySQL) ||
			record.target != string(databasemigration.BackendSQLite) || record.sourceEpoch != epoch ||
			record.table != tables[index] || record.operation != string(operations[index]) ||
			record.schemaVersion != schema.Current().Version || record.createdAtMS <= 0 || record.appliedAtMS != 0 {
			t.Errorf("journal envelope[%d]=%+v", index, record)
		}
		expectedMutationID := sha256.Sum256([]byte(record.transactionID + ":" + strconv.Itoa(index)))
		if record.mutationID != hex.EncodeToString(expectedMutationID[:]) {
			t.Errorf("journal mutation ID[%d]=%s, want deterministic transaction sequence ID", index, record.mutationID)
		}
		payload := ""
		if record.payload.Valid {
			payload = record.payload.String
		}
		if record.payloadBytes != int64(len(record.primaryKey)+len(payload)) {
			t.Errorf("journal payload bytes[%d]=%d, want %d", index, record.payloadBytes,
				len(record.primaryKey)+len(payload))
		}
		mutation := databasemigration.Mutation{
			ID: record.mutationID, TransactionID: record.transactionID, Sequence: record.sequence,
			Source: databasemigration.Backend(record.source), Target: databasemigration.Backend(record.target),
			SourceEpoch: record.sourceEpoch, Table: record.table,
			Operation: databasemigration.Operation(record.operation), PrimaryKey: json.RawMessage(record.primaryKey),
			SchemaVersion: record.schemaVersion, RowVersion: record.rowVersion,
		}
		if record.payload.Valid {
			mutation.Payload = json.RawMessage(record.payload.String)
		}
		if expected := databasemigration.MutationDigest(mutation); record.digest != expected {
			t.Errorf("journal mutation digest[%d]=%s, want %s", index, record.digest, expected)
		}
		if record.rowVersion <= 0 {
			t.Errorf("journal row version[%d]=%d, want positive", index, record.rowVersion)
		}
		if index > 0 && record.rowVersion != records[index-1].rowVersion+1 {
			t.Errorf("journal row versions are not transaction-contiguous: %d then %d",
				records[index-1].rowVersion, record.rowVersion)
		}
	}
}

type expectedMySQLJournalValue struct {
	kind  string
	value *string
}

func textJournalValue(value string) expectedMySQLJournalValue {
	return expectedMySQLJournalValue{kind: "text", value: &value}
}

func integerJournalValue(value int64) expectedMySQLJournalValue {
	formatted := strconv.FormatInt(value, 10)
	return expectedMySQLJournalValue{kind: "integer", value: &formatted}
}

func realJournalValue(value string) expectedMySQLJournalValue {
	return expectedMySQLJournalValue{kind: "real", value: &value}
}

func nullJournalValue() expectedMySQLJournalValue {
	return expectedMySQLJournalValue{kind: "null"}
}

func assertTypedJournalObject(t *testing.T, label, encoded string, expected map[string]expectedMySQLJournalValue) {
	t.Helper()
	var values map[string]map[string]json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		t.Fatalf("%s is not typed JSON: %v", label, err)
	}
	if len(values) != len(expected) {
		t.Errorf("%s fields=%d, want %d", label, len(values), len(expected))
	}
	for column, want := range expected {
		actual, ok := values[column]
		if !ok {
			t.Errorf("%s omits field %s", label, column)
			continue
		}
		var kind string
		if err := json.Unmarshal(actual["type"], &kind); err != nil || kind != want.kind {
			t.Errorf("%s field %s type=%q err=%v, want %q", label, column, kind, err, want.kind)
		}
		if want.value == nil {
			if _, exists := actual["value"]; exists {
				t.Errorf("%s NULL field %s unexpectedly has a value", label, column)
			}
			continue
		}
		var value string
		if err := json.Unmarshal(actual["value"], &value); err != nil || value != *want.value {
			t.Errorf("%s field %s value length=%d err=%v, want length=%d",
				label, column, len(value), err, len(*want.value))
		}
	}
}

func assertMySQLJournalFenceError(t *testing.T, err error) {
	t.Helper()
	var mysqlErr *mysqldriver.MySQLError
	if err == nil || !errors.As(err, &mysqlErr) || mysqlErr.Number != 1644 ||
		!strings.Contains(mysqlErr.Message, "mysql journal write epoch fenced") {
		t.Fatalf("raw MySQL authority write was not fenced by trigger: %v", err)
	}
}

func assertBusinessRowCount(t *testing.T, ctx context.Context, db *sql.DB, table, column, value string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE "+column+"=?", value).Scan(&count); err != nil {
		t.Fatalf("count %s fixture row: %v", table, err)
	}
	if count != want {
		t.Fatalf("%s fixture rows=%d, want %d", table, count, want)
	}
}

func assertTransactionOutboxCount(t *testing.T, ctx context.Context, db *sql.DB, transactionID string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM database_outbox WHERE transaction_id=?`, transactionID).Scan(&count); err != nil {
		t.Fatalf("count transaction Outbox rows: %v", err)
	}
	if count != want {
		t.Fatalf("transaction %s Outbox rows=%d, want %d", transactionID, count, want)
	}
}

func readMySQLAuthorityCounter(t *testing.T, ctx context.Context, db *sql.DB) int64 {
	t.Helper()
	var value int64
	if err := db.QueryRowContext(ctx, `SELECT last_row_version
		FROM database_mysql_authority_counter WHERE id=1`).Scan(&value); err != nil {
		t.Fatalf("read MySQL authority counter: %v", err)
	}
	return value
}

func assertFixtureOutboxCount(t *testing.T, ctx context.Context, db *sql.DB, token string, want int) {
	t.Helper()
	var count int
	like := "%" + token + "%"
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM database_outbox
		WHERE primary_key_json LIKE ? OR payload_json LIKE ?`, like, like).Scan(&count); err != nil {
		t.Fatalf("count fixture Outbox rows: %v", err)
	}
	if count != want {
		t.Fatalf("fixture Outbox rows=%d, want %d", count, want)
	}
}

func routeMySQLIntegration(
	t *testing.T,
	ctx context.Context,
	repository *databasemigration.SQLRepository,
	change databasemigration.RoutingChange,
) databasemigration.RoutingState {
	t.Helper()
	current, err := repository.Routing(ctx)
	if err != nil {
		t.Fatalf("read MySQL integration routing: %v", err)
	}
	if current.WritePrimary == change.WritePrimary && current.BusinessRead == change.BusinessRead &&
		current.SystemRead == change.SystemRead {
		return current
	}
	return compareAndSwapMySQLIntegration(t, ctx, repository, current, change)
}

func compareAndSwapMySQLIntegration(
	t *testing.T,
	ctx context.Context,
	repository *databasemigration.SQLRepository,
	current databasemigration.RoutingState,
	change databasemigration.RoutingChange,
) databasemigration.RoutingState {
	t.Helper()
	next, err := repository.CompareAndSwapRouting(ctx, current.Generation, current.Epoch, change)
	if err != nil {
		t.Fatalf("compare-and-swap MySQL integration routing: %v", err)
	}
	return next
}

func cleanupMySQLJournalIntegration(
	t *testing.T,
	db *sql.DB,
	original databasemigration.RoutingState,
	fixture *mysqlJournalFixture,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	repository := databasemigration.NewSQLRepository(db, databasemigration.DialectMySQL)
	DisableMySQL(db)
	current, err := repository.Routing(ctx)
	if err != nil {
		t.Errorf("cleanup: read routing: %v", err)
		return
	}
	if current.WritePrimary != databasemigration.BackendSQLite {
		current, err = repository.CompareAndSwapRouting(ctx, current.Generation, current.Epoch,
			databasemigration.RoutingChange{WritePrimary: databasemigration.BackendSQLite,
				BusinessRead: original.BusinessRead, SystemRead: original.SystemRead})
		if err != nil {
			t.Errorf("cleanup: route writes to SQLite: %v", err)
			return
		}
	}
	settingKeys := []any{fixture.noopKey, fixture.settingKey, fixture.updatedSettingKey,
		fixture.directKey, fixture.rollbackKey}
	if _, err := db.ExecContext(ctx, `DELETE FROM settings WHERE `+"`key`"+` IN (?,?,?,?,?)`, settingKeys...); err != nil {
		t.Errorf("cleanup: delete settings fixtures: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM model_prices WHERE model IN (?,?)`,
		fixture.modelKey, fixture.rollbackModelKey); err != nil {
		t.Errorf("cleanup: delete model price fixtures: %v", err)
	}
	like := "%" + fixture.token + "%"
	if _, err := db.ExecContext(ctx, `DELETE FROM database_outbox
		WHERE primary_key_json LIKE ? OR payload_json LIKE ?`, like, like); err != nil {
		t.Errorf("cleanup: delete fixture Outbox rows: %v", err)
	}
	if original.WritePrimary == databasemigration.BackendMySQL {
		current, err = repository.Routing(ctx)
		if err == nil {
			current, err = repository.CompareAndSwapRouting(ctx, current.Generation, current.Epoch,
				databasemigration.RoutingChange{WritePrimary: original.WritePrimary,
					BusinessRead: original.BusinessRead, SystemRead: original.SystemRead})
		}
		if err != nil {
			t.Errorf("cleanup: restore original MySQL routing: %v", err)
			return
		}
		if err := EnableMySQL(ctx, db, current.Epoch); err != nil {
			t.Errorf("cleanup: restore MySQL authority state: %v", err)
		}
	}
}

type mysqlJournalIntegrationLock struct {
	conn *sql.Conn
	name string
}

func acquireMySQLJournalIntegrationLock(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	database string,
) mysqlJournalIntegrationLock {
	t.Helper()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve MySQL integration lock connection: %v", err)
	}
	digest := sha256.Sum256([]byte(database))
	lockName := "cpamp:outboxcontext:" + hex.EncodeToString(digest[:12])
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?,60)`, lockName).Scan(&acquired); err != nil ||
		!acquired.Valid || acquired.Int64 != 1 {
		_ = conn.Close()
		t.Fatalf("acquire MySQL journal integration lock: acquired=%v err=%v", acquired, err)
	}
	return mysqlJournalIntegrationLock{conn: conn, name: lockName}
}

func releaseMySQLJournalIntegrationLock(t *testing.T, lock mysqlJournalIntegrationLock) {
	t.Helper()
	if lock.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var released sql.NullInt64
	if err := lock.conn.QueryRowContext(ctx, `SELECT RELEASE_LOCK(?)`, lock.name).Scan(&released); err != nil ||
		!released.Valid || released.Int64 != 1 {
		t.Logf("release MySQL integration lock by closing session: %v", err)
	}
	_ = lock.conn.Close()
}

func mysqlJournalIntegrationConfig(t *testing.T) dbmysql.Config {
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
