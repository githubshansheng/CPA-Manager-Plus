package databasemigration

import (
	"context"
	"strings"
	"testing"
)

func TestSQLiteJournalInstallerRequiresCompleteManifestAndGroupProvider(t *testing.T) {
	repository, db := openTestRepository(t)
	_ = repository
	ctx := context.Background()
	if _, err := db.Exec(`CREATE TABLE authority_fixture (
		id INTEGER PRIMARY KEY,
		raw_json TEXT NOT NULL,
		fail_body TEXT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	omitted := StaticManifest{{Name: "authority_fixture", Columns: []ColumnSpec{
		{Name: "id", LogicalType: "integer"}, {Name: "raw_json", LogicalType: "text"},
	}, PrimaryKey: []string{"id"}}}
	if err := ValidateSQLiteManifestParity(ctx, db, omitted); err == nil || !strings.Contains(err.Error(), "omit fields") {
		t.Fatalf("omitted-field parity error = %v", err)
	}
	complete := journalFixtureManifest()
	installer := SQLiteJournalInstaller{DB: db, Manifest: complete, SchemaVersion: 1}
	if err := installer.Install(ctx); err == nil || !strings.Contains(err.Error(), "GroupID") {
		t.Fatalf("missing provider error = %v", err)
	}
	installer.Provider = fixtureJournalProvider{contract: "test-v1"}
	if err := installer.Install(ctx); err != nil {
		t.Fatalf("install complete journal: %v", err)
	}
	if err := installer.Install(ctx); err != nil {
		t.Fatalf("idempotent journal install: %v", err)
	}
	for _, name := range JournalTriggerNames("authority_fixture") {
		var triggerSQL string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`, name).Scan(&triggerSQL); err != nil {
			t.Fatalf("read trigger %q: %v", name, err)
		}
		if !strings.Contains(triggerSQL, `"id"`) || !strings.Contains(triggerSQL, `"raw_json"`) ||
			!strings.Contains(triggerSQL, `"fail_body"`) {
			t.Fatalf("trigger %q omits a manifest field:\n%s", name, triggerSQL)
		}
	}
	if _, err := db.Exec(`INSERT INTO authority_fixture(id, raw_json, fail_body)
		VALUES (1, '{"all":true}', NULL)`); err != nil {
		t.Fatalf("authoritative insert with journal: %v", err)
	}
	groups, err := repository.PendingOutbox(ctx, 1)
	if err != nil || len(groups) != 1 || len(groups[0].Mutations) != 1 {
		t.Fatalf("journal outbox = %#v, err=%v", groups, err)
	}
	payload := string(groups[0].Mutations[0].Payload)
	if !strings.Contains(payload, `"raw_json"`) || !strings.Contains(payload, `"fail_body"`) ||
		!strings.Contains(payload, `"type":"null"`) {
		t.Fatalf("journal payload did not preserve all fields and NULL: %s", payload)
	}
}

func TestSQLiteJournalContractChangeRequiresCoordinatedReinstall(t *testing.T) {
	_, db := openTestRepository(t)
	ctx := context.Background()
	if _, err := db.Exec(`CREATE TABLE authority_fixture (
		id INTEGER PRIMARY KEY,
		raw_json TEXT NOT NULL,
		fail_body TEXT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	first := SQLiteJournalInstaller{DB: db, Manifest: journalFixtureManifest(),
		Provider: fixtureJournalProvider{contract: "test-v1"}, SchemaVersion: 1}
	if err := first.Install(ctx); err != nil {
		t.Fatal(err)
	}
	first.Provider = fixtureJournalProvider{contract: "test-v2"}
	if err := first.Install(ctx); err == nil || !strings.Contains(err.Error(), "coordinated reinstall") {
		t.Fatalf("changed provider error = %v", err)
	}
}

func journalFixtureManifest() StaticManifest {
	return StaticManifest{{Name: "authority_fixture", Columns: []ColumnSpec{
		{Name: "id", LogicalType: "integer"},
		{Name: "raw_json", LogicalType: "text"},
		{Name: "fail_body", LogicalType: "text", Nullable: true},
	}, PrimaryKey: []string{"id"}, VersionColumn: "id"}}
}

type fixtureJournalProvider struct{ contract string }

func (provider fixtureJournalProvider) ContractID() string { return provider.contract }
func (fixtureJournalProvider) TransactionIDExpression() string {
	return `'tx-fixture'`
}
func (fixtureJournalProvider) MutationIDExpression() string { return `'mutation-fixture'` }
func (fixtureJournalProvider) SequenceExpression() string   { return `0` }
func (fixtureJournalProvider) EpochExpression() string      { return `1` }
func (fixtureJournalProvider) RowVersionExpression(string, Operation, string) string {
	return `1`
}
func (fixtureJournalProvider) NowMSExpression() string { return `1` }
func (fixtureJournalProvider) DigestExpression(string) string {
	mutation := Mutation{
		ID: "mutation-fixture", TransactionID: "tx-fixture", Sequence: 0,
		Source: BackendSQLite, Target: BackendMySQL, SourceEpoch: 1,
		Table: "authority_fixture", Operation: OperationInsert,
		PrimaryKey:    []byte(`{"id":{"type":"integer","value":"1"}}`),
		Payload:       []byte(`{"id":{"type":"integer","value":"1"},"raw_json":{"type":"text","value":"{\"all\":true}"},"fail_body":{"type":"null"}}`),
		SchemaVersion: 1, RowVersion: 1,
	}
	return quoteSQLiteString(MutationDigest(mutation))
}

var _ SQLiteJournalSQLProvider = fixtureJournalProvider{}
