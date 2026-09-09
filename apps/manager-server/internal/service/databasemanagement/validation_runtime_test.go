package databasemanagement

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/databasemigration"
	_ "modernc.org/sqlite"
)

func TestSQLValidationSnapshotsPreserveNullEmptyUnicodeBytesAndFloatBits(t *testing.T) {
	source := openValidationFixture(t, "source.sqlite")
	target := openValidationFixture(t, "target.sqlite")
	for _, db := range []*sql.DB{source, target} {
		if _, err := db.Exec(`CREATE TABLE usage_events (
			id INTEGER PRIMARY KEY, nullable_text TEXT, empty_text TEXT NOT NULL,
			unicode_text TEXT NOT NULL, bytes_value BLOB NOT NULL, real_value REAL NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO usage_events VALUES(1,NULL,'','你好🌍',?,?)`,
			[]byte{0, 0xff, 1}, math.Copysign(0, -1)); err != nil {
			t.Fatal(err)
		}
	}
	spec := databasemigration.TableSpec{Name: "usage_events", PrimaryKey: []string{"id"},
		Columns: []databasemigration.ColumnSpec{
			{Name: "id", LogicalType: "integer"},
			{Name: "nullable_text", LogicalType: "text", Nullable: true},
			{Name: "empty_text", LogicalType: "text"},
			{Name: "unicode_text", LogicalType: "text"},
			{Name: "bytes_value", LogicalType: "blob"},
			{Name: "real_value", LogicalType: "real"},
		}}
	reader := &sqlValidationReader{source: source, target: target}
	left, err := reader.TableSnapshot(context.Background(), databasemigration.ValidationSourceSide, spec)
	if err != nil {
		t.Fatal(err)
	}
	right, err := reader.TableSnapshot(context.Background(), databasemigration.ValidationTargetSide, spec)
	if err != nil {
		t.Fatal(err)
	}
	comparison := databasemigration.CompareTableValidation(spec.Name, left, right)
	if !comparison.Passed || left.Rows != 1 || left.SHA256 == "" || left.MinKey == "" {
		t.Fatalf("comparison=%#v source=%#v target=%#v", comparison, left, right)
	}
	if _, err := target.Exec(`UPDATE usage_events SET nullable_text='' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	right, err = reader.TableSnapshot(context.Background(), databasemigration.ValidationTargetSide, spec)
	if err != nil {
		t.Fatal(err)
	}
	if databasemigration.CompareTableValidation(spec.Name, left, right).Passed {
		t.Fatal("NULL to empty-string change was not detected")
	}
}

func TestCanonicalDatabaseValuePreservesFloatBits(t *testing.T) {
	spec := databasemigration.ColumnSpec{Name: "value", LogicalType: "real"}
	negativeZero, err := canonicalDatabaseValue(spec, math.Copysign(0, -1))
	if err != nil {
		t.Fatal(err)
	}
	positiveZero, err := canonicalDatabaseValue(spec, float64(0))
	if err != nil {
		t.Fatal(err)
	}
	if negativeZero.Kind == positiveZero.Kind && negativeZero.Text == positiveZero.Text {
		t.Fatal("canonical float normalization discarded the sign bit")
	}
}

func TestSQLValidationSnapshotEmptyTableHasStableEmptyRange(t *testing.T) {
	db := openValidationFixture(t, "empty.sqlite")
	if _, err := db.Exec(`CREATE TABLE empty_rows (id INTEGER PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	spec := databasemigration.TableSpec{Name: "empty_rows", PrimaryKey: []string{"id"},
		Columns: []databasemigration.ColumnSpec{{Name: "id", LogicalType: "integer"},
			{Name: "value", LogicalType: "text", Nullable: true}}}
	reader := &sqlValidationReader{source: db, target: db}
	snapshot, err := reader.TableSnapshot(context.Background(), databasemigration.ValidationSourceSide, spec)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Rows != 0 || snapshot.MinKey != "" || snapshot.MaxKey != "" || snapshot.SHA256 == "" {
		t.Fatalf("empty snapshot = %#v", snapshot)
	}
}

func TestMySQLForeignKeyAntiJoinIgnoresNullAndCountsOrphans(t *testing.T) {
	target := openValidationFixture(t, "foreign-keys.sqlite")
	if _, err := target.Exec(`CREATE TABLE codex_inspection_runs (id INTEGER PRIMARY KEY);
		CREATE TABLE codex_inspection_logs (run_id INTEGER);
		INSERT INTO codex_inspection_runs(id) VALUES(1);
		INSERT INTO codex_inspection_logs(run_id) VALUES(1),(NULL),(999)`); err != nil {
		t.Fatal(err)
	}
	reader := &sqlValidationReader{target: target}
	count, err := reader.foreignKeyErrors(context.Background(), databasemigration.ValidationTargetSide,
		"codex_inspection_logs")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("foreign-key violations = %d, want 1", count)
	}
}

func TestDerivedDataReadyRequiresExplicitConformanceGate(t *testing.T) {
	ready, err := (&sqlValidationReader{}).DerivedDataReady(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Fatal("derived data was accepted without the MySQL semantic conformance gate")
	}
}

func openValidationFixture(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
