package databasemanagement

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
	_ "modernc.org/sqlite"
)

func TestSQLiteSourcePreflightAcceptsLosslessValuesAndPreservesNull(t *testing.T) {
	db := openSourcePreflightDB(t, `CREATE TABLE fixture (
		id INTEGER, optional_text TEXT, label TEXT, ratio, enabled INTEGER, payload BLOB
	)`)
	if _, err := db.Exec(`INSERT INTO fixture VALUES (1,NULL,'界',1.5,1,x'00ff')`); err != nil {
		t.Fatal(err)
	}
	table := sourcePreflightFixtureTable()
	report, err := preflightSQLiteSource(context.Background(), db, []schema.Table{table}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if report.Tables != 1 || report.Rows != 1 || report.LargestRowByte <= 0 {
		t.Fatalf("preflight report = %#v", report)
	}
}

func TestSQLiteSourcePreflightRejectsValuesMySQLCannotPreserve(t *testing.T) {
	tests := []struct {
		name       string
		insert     string
		packetSize int64
		want       string
	}{
		{name: "null primary key", insert: `INSERT INTO fixture VALUES (NULL,NULL,'ok',1,1,x'')`, packetSize: 1 << 20, want: "NULL is not representable"},
		{name: "invalid utf8", insert: `INSERT INTO fixture VALUES (1,NULL,CAST(x'80' AS TEXT),1,1,x'')`, packetSize: 1 << 20, want: "invalid UTF-8"},
		{name: "text stored as blob", insert: `INSERT INTO fixture VALUES (1,NULL,CAST('ok' AS BLOB),1,1,x'')`, packetSize: 1 << 20, want: "want text"},
		{name: "bounded text", insert: `INSERT INTO fixture VALUES (1,NULL,'123456789',1,1,x'')`, packetSize: 1 << 20, want: "permits 8"},
		{name: "tinyint overflow", insert: `INSERT INTO fixture VALUES (1,NULL,'ok',1,128,x'')`, packetSize: 1 << 20, want: "outside signed TINYINT"},
		{name: "double precision loss", insert: `INSERT INTO fixture VALUES (1,NULL,'ok',9007199254740993,1,x'')`, packetSize: 1 << 20, want: "cannot be represented exactly"},
		{name: "non finite double", insert: `INSERT INTO fixture VALUES (1,NULL,'ok',1e999,1,x'')`, packetSize: 1 << 20, want: "non-finite"},
		{name: "packet overflow", insert: `INSERT INTO fixture VALUES (1,NULL,'ok',1,1,zeroblob(1024))`, packetSize: 512, want: "max_allowed_packet"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openSourcePreflightDB(t, `CREATE TABLE fixture (
				id, optional_text, label, ratio, enabled, payload
			)`)
			if _, err := db.Exec(test.insert); err != nil {
				t.Fatal(err)
			}
			_, err := preflightSQLiteSource(context.Background(), db,
				[]schema.Table{sourcePreflightFixtureTable()}, test.packetSize)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("preflight error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestMySQLTargetValueBoundsRejectLossyASCIIChar(t *testing.T) {
	column := schema.Column{Kind: schema.KindText, MySQLType: "CHAR(64) CHARACTER SET ascii COLLATE ascii_bin"}
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "hash ", want: "trailing spaces"},
		{value: "哈希", want: "non-ASCII"},
	} {
		if err := validateSQLiteMySQLValue(column, "text", test.value); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("validate %q error = %v, want %q", test.value, err, test.want)
		}
	}
}

func TestMySQLExecutePacketBytesIncludesLengthPrefixes(t *testing.T) {
	small, err := mysqlExecutePacketBytes([]any{int64(1), "x", nil})
	if err != nil {
		t.Fatal(err)
	}
	large, err := mysqlExecutePacketBytes([]any{int64(1), strings.Repeat("x", 1<<16), nil})
	if err != nil {
		t.Fatal(err)
	}
	if large-small < 1<<16 {
		t.Fatalf("packet sizes small=%d large=%d", small, large)
	}
}

func sourcePreflightFixtureTable() schema.Table {
	return schema.Table{
		Name: "fixture", Class: schema.ClassAuthoritative,
		Columns: []schema.Column{
			{Name: "id", Kind: schema.KindInteger, MySQLType: "BIGINT", PrimaryKeyPosition: 1},
			{Name: "optional_text", Kind: schema.KindText, MySQLType: "LONGTEXT", Nullable: true},
			{Name: "label", Kind: schema.KindText, MySQLType: "VARCHAR(8)"},
			{Name: "ratio", Kind: schema.KindReal, MySQLType: "DOUBLE"},
			{Name: "enabled", Kind: schema.KindInteger, MySQLType: "TINYINT"},
			{Name: "payload", Kind: schema.KindBlob, MySQLType: "LONGBLOB"},
		},
	}
}

func openSourcePreflightDB(t *testing.T, ddl string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	return db
}
