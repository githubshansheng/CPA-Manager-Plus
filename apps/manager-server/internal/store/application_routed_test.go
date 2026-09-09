package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
)

type applicationRouteProvider struct {
	route     database.RouteSnapshot
	sqlite    database.Backend
	mysql     database.Backend
	failMySQL bool
	coverage  database.CacheCoverageStatus
}

func (p *applicationRouteProvider) RouteSnapshot(context.Context) (database.RouteSnapshot, error) {
	return p.route, nil
}

func (p *applicationRouteProvider) CacheCoverage(context.Context) (database.CacheCoverageStatus, error) {
	return p.coverage, nil
}

func (p *applicationRouteProvider) AcquireBackend(
	_ context.Context,
	kind database.BackendKind,
) (database.Backend, func(), error) {
	switch kind {
	case database.BackendSQLite:
		return p.sqlite, func() {}, nil
	case database.BackendMySQL:
		if p.failMySQL {
			return nil, func() {}, errors.New("mysql offline")
		}
		return p.mysql, func() {}, nil
	default:
		return nil, func() {}, errors.New("unsupported backend")
	}
}

func TestRoutedApplicationStoreSeparatesSystemBusinessAndWriteRoutes(t *testing.T) {
	sqliteStore, err := Open(filepath.Join(t.TempDir(), "sqlite.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()
	mysqlFixture, err := Open(filepath.Join(t.TempDir(), "mysql-fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mysqlFixture.Close()

	ctx := context.Background()
	if err := sqliteStore.SaveSetup(ctx, Setup{CPAUpstreamURL: "sqlite-system", ManagementKey: "sqlite-key"}); err != nil {
		t.Fatal(err)
	}
	if err := mysqlFixture.SaveSetup(ctx, Setup{CPAUpstreamURL: "mysql-system", ManagementKey: "mysql-key"}); err != nil {
		t.Fatal(err)
	}
	if err := sqliteStore.DeadLetters.Insert(ctx, "sqlite", "failure"); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if err := mysqlFixture.DeadLetters.Insert(ctx, "mysql", "failure"); err != nil {
			t.Fatal(err)
		}
	}

	provider := &applicationRouteProvider{
		route: database.RouteSnapshot{
			Generation: 4, Epoch: 9, WritePrimary: database.BackendSQLite,
			BusinessReadPrimary: database.BackendMySQL, SystemReadPrimary: database.BackendSQLite,
		},
		sqlite: sqliteStore.Backend(),
		mysql:  database.NewSQLBackend(database.BackendMySQL, mysqlFixture.Backend().DB()),
	}
	routed, err := newRoutedApplicationStore(ctx, provider, func(backend database.Backend) (*Store, error) {
		return New(backend.DB()), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	setup, found, err := routed.LoadSetup(ctx)
	if err != nil || !found || setup.CPAUpstreamURL != "sqlite-system" {
		t.Fatalf("system setup=%+v found=%v err=%v", setup, found, err)
	}
	_, deadLetters, err := routed.Counts(ctx)
	if err != nil || deadLetters != 2 {
		t.Fatalf("business counts deadLetters=%d err=%v", deadLetters, err)
	}

	provider.route.Generation++
	provider.route.Epoch++
	provider.route.WritePrimary = database.BackendMySQL
	if err := routed.SaveSetup(ctx, Setup{CPAUpstreamURL: "mysql-write", ManagementKey: "write-key"}); err != nil {
		t.Fatal(err)
	}
	written, found, err := mysqlFixture.LoadSetup(ctx)
	if err != nil || !found || written.CPAUpstreamURL != "mysql-write" {
		t.Fatalf("mysql write=%+v found=%v err=%v", written, found, err)
	}
	unchanged, found, err := sqliteStore.LoadSetup(ctx)
	if err != nil || !found || unchanged.CPAUpstreamURL != "sqlite-system" {
		t.Fatalf("sqlite setup changed=%+v found=%v err=%v", unchanged, found, err)
	}
}

func TestRoutedApplicationStoreAvailabilityFallbackReportsSQLiteCoverage(t *testing.T) {
	sqliteStore, err := Open(filepath.Join(t.TempDir(), "sqlite.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()
	if err := sqliteStore.DeadLetters.Insert(context.Background(), "sqlite", "failure"); err != nil {
		t.Fatal(err)
	}
	provider := &applicationRouteProvider{
		route: database.RouteSnapshot{
			Generation: 2, Epoch: 3, WritePrimary: database.BackendSQLite,
			BusinessReadPrimary: database.BackendMySQL, SystemReadPrimary: database.BackendSQLite,
		},
		sqlite: sqliteStore.Backend(), failMySQL: true,
		coverage: database.CacheCoverageStatus{FromMS: 100, ToMS: 200, Complete: false},
	}
	routed, err := newRoutedApplicationStore(context.Background(), provider,
		func(backend database.Backend) (*Store, error) { return New(backend.DB()), nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, recorder := database.WithReadCoverageRecorder(context.Background())
	_, deadLetters, err := routed.Counts(ctx)
	if err != nil || deadLetters != 1 {
		t.Fatalf("fallback counts deadLetters=%d err=%v", deadLetters, err)
	}
	coverage := recorder.Snapshot()
	if coverage.DataSource != database.BackendSQLite || coverage.Completeness != database.DataPartial ||
		coverage.FromMS != 100 || coverage.ToMS != 200 {
		t.Fatalf("fallback coverage=%+v", coverage)
	}
}

func TestRoutedCodexLegacyIdentityCatchUpAdvancesBothBackends(t *testing.T) {
	sqliteStore, err := Open(filepath.Join(t.TempDir(), "sqlite.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()
	mysqlFixture, err := Open(filepath.Join(t.TempDir(), "mysql-fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mysqlFixture.Close()

	for index, endpoint := range []*Store{sqliteStore, mysqlFixture} {
		if _, err := endpoint.db.Exec(`insert into usage_events (
			event_hash, timestamp_ms, timestamp, model, created_at_ms,
			provider, auth_provider_snapshot, auth_file_snapshot, source,
			auth_index, auth_account_id_snapshot, account_snapshot
		) values (?, 1, '1', 'gpt-test', 1, 'codex', 'codex',
			'codex-a.json', 'codex-a.json', 'auth-a', 'workspace-a', 'member@example.com')`,
			"routed-identity-"+string(rune('a'+index))); err != nil {
			t.Fatal(err)
		}
		if _, err := endpoint.db.Exec(`delete from usage_monitoring_rollup_state
			where rollup_name = 'codex_legacy_identity_v1'`); err != nil {
			t.Fatal(err)
		}
	}

	provider := &applicationRouteProvider{
		route: database.RouteSnapshot{
			Generation: 1, Epoch: 1, WritePrimary: database.BackendSQLite,
			BusinessReadPrimary: database.BackendSQLite, SystemReadPrimary: database.BackendSQLite,
		},
		sqlite: sqliteStore.Backend(),
		mysql:  database.NewSQLBackend(database.BackendMySQL, mysqlFixture.Backend().DB()),
	}
	routed, err := newRoutedApplicationStore(context.Background(), provider,
		func(backend database.Backend) (*Store, error) { return New(backend.DB()), nil })
	if err != nil {
		t.Fatal(err)
	}
	result, err := routed.CatchUpCodexLegacyIdentityEvidence(context.Background(), 1000, 1)
	if err != nil || result.Processed != 2 || result.Pending || result.CoverageEventID != 1 {
		t.Fatalf("routed identity catch-up = %+v err=%v", result, err)
	}
	for name, endpoint := range map[string]*Store{"sqlite": sqliteStore, "mysql": mysqlFixture} {
		state, err := endpoint.UsageMonitoring.State(context.Background(), usageevent.CodexLegacyIdentityRollupName)
		if err != nil || state.Status != "ready" || state.CoverageEventID != 1 {
			t.Fatalf("%s identity state = %+v err=%v", name, state, err)
		}
		var rows int
		if err := endpoint.db.QueryRow(`select count(*) from ` +
			usageevent.CodexLegacyIdentityEvidenceTable).Scan(&rows); err != nil || rows != 1 {
			t.Fatalf("%s identity evidence rows=%d err=%v", name, rows, err)
		}
	}
}
