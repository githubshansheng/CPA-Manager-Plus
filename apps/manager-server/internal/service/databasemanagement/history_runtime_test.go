package databasemanagement

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	_ "modernc.org/sqlite"
)

func TestSQLiteHistorySourceBoundsBytesWithoutSkippingRows(t *testing.T) {
	db, err := sql.Open("sqlite", "file:history-byte-limit?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE api_key_aliases (
		api_key_hash TEXT PRIMARY KEY, alias TEXT NOT NULL, updated_at_ms INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("界", 200)
	if _, err := db.Exec(`INSERT INTO api_key_aliases VALUES
		('key-1', ?, 1), ('key-2', ?, 2), ('key-3', ?, 3)`, large, large, large); err != nil {
		t.Fatal(err)
	}
	table := findMigrationTable(t, "api_key_aliases")
	source := sqliteHistorySource{db: db, maxBatchBytes: 1}
	watermark, err := source.CaptureWatermark(context.Background(), table)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint json.RawMessage
	var keys []string
	for batchNumber := 0; batchNumber < 4; batchNumber++ {
		batch, err := source.ReadBatch(context.Background(), table, checkpoint, watermark, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch.Rows) != 1 {
			t.Fatalf("batch %d rows=%d, want one oversized row", batchNumber, len(batch.Rows))
		}
		keys = append(keys, batch.Rows[0].Values[0].(string))
		checkpoint = batch.NextCheckpoint
		if batch.Done {
			break
		}
	}
	if strings.Join(keys, ",") != "key-1,key-2,key-3" {
		t.Fatalf("copied keys=%v", keys)
	}
}

func TestDecodeHistoryTableWatermarkSupportsLegacyAndRejectsNull(t *testing.T) {
	current, err := decodeHistoryTableWatermark(json.RawMessage(`{"maxRowId":9,"rows":7}`))
	if err != nil || current.MaxRowID != 9 || current.Rows != 7 {
		t.Fatalf("current watermark=%#v err=%v", current, err)
	}
	legacy, err := decodeHistoryTableWatermark(json.RawMessage(`9`))
	if err != nil || legacy.MaxRowID != 9 || legacy.Rows != 0 {
		t.Fatalf("legacy watermark=%#v err=%v", legacy, err)
	}
	if _, err := decodeHistoryTableWatermark(json.RawMessage(`null`)); err == nil {
		t.Fatal("null watermark was accepted")
	}
}

func findMigrationTable(t *testing.T, name string) databasemigration.TableSpec {
	t.Helper()
	for _, table := range migrationManifest().Tables() {
		if table.Name == name {
			return table
		}
	}
	t.Fatalf("migration table %s not found", name)
	return databasemigration.TableSpec{}
}
