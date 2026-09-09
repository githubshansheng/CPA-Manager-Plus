package databasemanagement

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/control"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func TestReplicationCatchUpDoesNotReenterExclusiveBackgroundFence(t *testing.T) {
	protector, err := security.NewProtector([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	controlStore, err := control.NewStore(filepath.Join(t.TempDir(), "database-control.json.enc"), protector)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controlStore.Save(0, control.DefaultState()); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(RuntimeOptions{Control: controlStore})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		runtime.backgroundMu.Lock()
		defer runtime.backgroundMu.Unlock()
		runtime.replicateOnceWhileBackgroundPaused(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("validation replication catch-up recursively acquired backgroundMu")
	}
}

func TestDecodeJournalObjectPreservesNullTextBytesAndNumericBounds(t *testing.T) {
	input := json.RawMessage("{\"nullValue\":{\"type\":\"null\"},\"empty\":{\"type\":\"text\",\"value\":\"\"},\"unicode\":{\"type\":\"text\",\"value\":\"你好🌍\"},\"bytes\":{\"type\":\"bytes\",\"hex\":\"00ff\"},\"integer\":{\"type\":\"integer\",\"value\":\"9223372036854775807\"},\"real\":{\"type\":\"real\",\"value\":\"0.125\"}}")
	values, err := decodeJournalObject(input)
	if err != nil {
		t.Fatal(err)
	}
	if values["nullValue"] != nil || values["empty"] != "" || values["unicode"] != "你好🌍" {
		t.Fatalf("text/null values changed: %#v", values)
	}
	bytes, ok := values["bytes"].([]byte)
	if !ok || len(bytes) != 2 || bytes[0] != 0 || bytes[1] != 0xff {
		t.Fatalf("bytes changed: %#v", values["bytes"])
	}
	if values["integer"] != int64(math.MaxInt64) || values["real"] != 0.125 {
		t.Fatalf("numeric values changed: %#v", values)
	}
}

func TestApplySQLiteMutationPreservesEveryUsageFieldAndNull(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "reverse.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	table, ok := authoritativeTable("usage_events")
	if !ok {
		t.Fatal("usage_events is not authoritative")
	}
	longText := strings.Repeat("原始<&\u2028🌍", 2000)
	payload := make(map[string]any, len(table.Columns))
	for _, column := range table.Columns {
		if column.Nullable {
			payload[column.Name] = map[string]any{"type": "null"}
			continue
		}
		switch column.Kind {
		case schema.KindInteger:
			payload[column.Name] = map[string]any{"type": "integer", "value": "0"}
		case schema.KindReal:
			payload[column.Name] = map[string]any{"type": "real", "value": "0.125"}
		case schema.KindBlob:
			payload[column.Name] = map[string]any{"type": "bytes", "hex": "00ff"}
		default:
			payload[column.Name] = map[string]any{"type": "text", "value": ""}
		}
	}
	payload["id"] = map[string]any{"type": "integer", "value": "987654321"}
	payload["event_hash"] = map[string]any{"type": "text", "value": "reverse-full-field"}
	payload["timestamp_ms"] = map[string]any{"type": "integer", "value": "1700000000000"}
	payload["timestamp"] = map[string]any{"type": "text", "value": "2023-11-14T22:13:20Z"}
	payload["model"] = map[string]any{"type": "text", "value": "模型"}
	payload["raw_json"] = map[string]any{"type": "text", "value": longText}
	payload["fail_body"] = map[string]any{"type": "text", "value": ""}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	primaryKey, _ := json.Marshal(map[string]any{
		"id": map[string]any{"type": "integer", "value": "987654321"},
	})
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mutation := databasemigration.Mutation{Table: table.Name,
		Operation: databasemigration.OperationInsert, PrimaryKey: primaryKey, Payload: payloadJSON}
	if err := applySQLiteMutation(context.Background(), tx, mutation); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var raw, fail string
	var latency sql.NullInt64
	if err := db.QueryRow(`SELECT raw_json,fail_body,latency_ms FROM usage_events WHERE id=987654321`).Scan(
		&raw, &fail, &latency); err != nil {
		t.Fatal(err)
	}
	if raw != longText || fail != "" || latency.Valid {
		t.Fatalf("reverse full fields changed: raw=%d/%d fail=%q latency=%#v",
			len(raw), len(longText), fail, latency)
	}
}

func TestMigrationManifestContainsEveryAuthoritativeColumn(t *testing.T) {
	got := migrationManifest()
	if err := databasemigration.ValidateManifest(got); err != nil {
		t.Fatal(err)
	}
	specs := map[string]databasemigration.TableSpec{}
	for _, table := range got.Tables() {
		specs[table.Name] = table
	}
	for _, table := range schema.Current().AuthoritativeTables() {
		spec, exists := specs[table.Name]
		if !exists {
			t.Fatalf("authoritative table %s is missing", table.Name)
		}
		if len(spec.Columns) != len(table.Columns) {
			t.Fatalf("%s has %d migration columns, want %d", table.Name, len(spec.Columns), len(table.Columns))
		}
		for index, column := range table.Columns {
			wantNullable := column.Nullable && column.PrimaryKeyPosition == 0
			if spec.Columns[index].Name != column.Name ||
				spec.Columns[index].Nullable != wantNullable ||
				spec.Columns[index].LogicalType != string(column.Kind) {
				t.Fatalf("%s column %d differs: %#v vs %#v", table.Name, index, spec.Columns[index], column)
			}
		}
	}
}
