package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"testing"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
)

type fakeRouteProvider struct {
	snapshot RouteSnapshot
	err      error
}

func (p *fakeRouteProvider) RouteSnapshot(context.Context) (RouteSnapshot, error) {
	return p.snapshot, p.err
}

type fakeCoverageProvider struct {
	coverage database.CacheCoverageStatus
	err      error
	calls    int
}

func (p *fakeCoverageProvider) CacheCoverage(context.Context) (database.CacheCoverageStatus, error) {
	p.calls++
	return p.coverage, p.err
}

func TestRoutedStoreSQLiteOnlyPreservesLegacySelection(t *testing.T) {
	ctx := context.Background()
	routed := NewRoutedStore(nil, nil)
	snapshot, err := routed.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot != sqliteOnlyRoute() {
		t.Fatalf("Snapshot() = %+v, want SQLite-only route", snapshot)
	}

	sqliteWrites := 0
	mysqlWrites := 0
	err = routed.Write(ctx, snapshot, WriteOperations{
		SQLite: func(context.Context) error {
			sqliteWrites++
			return nil
		},
		MySQL: func(context.Context) error {
			mysqlWrites++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if sqliteWrites != 1 || mysqlWrites != 0 {
		t.Fatalf("write calls sqlite=%d mysql=%d, want 1/0", sqliteWrites, mysqlWrites)
	}

	assertSQLiteRead := func(name string, read func() (RoutedResult[string], error)) {
		t.Helper()
		result, readErr := read()
		if readErr != nil {
			t.Fatalf("%s error = %v", name, readErr)
		}
		if result.Value != "sqlite" || result.DataSource != database.BackendSQLite || result.Fallback {
			t.Fatalf("%s result = %+v, want direct SQLite", name, result)
		}
	}
	operations := ReadOperations[string]{
		SQLite: func(context.Context) (string, error) { return "sqlite", nil },
		MySQL: func(context.Context) (string, error) {
			t.Fatal("legacy SQLite-only read invoked MySQL")
			return "", nil
		},
	}
	assertSQLiteRead("BusinessRead", func() (RoutedResult[string], error) {
		return BusinessRead(ctx, routed, operations)
	})
	assertSQLiteRead("SystemRead", func() (RoutedResult[string], error) {
		return SystemRead(ctx, routed, operations)
	})
}

func TestBusinessReadUsesMySQLPrimary(t *testing.T) {
	routed := NewRoutedStore(&fakeRouteProvider{snapshot: mysqlBusinessRoute(3, 7)}, nil)
	sqliteCalls := 0
	result, err := BusinessRead(context.Background(), routed, ReadOperations[string]{
		SQLite: func(context.Context) (string, error) {
			sqliteCalls++
			return "sqlite", nil
		},
		MySQL: func(context.Context) (string, error) { return "mysql", nil },
	})
	if err != nil {
		t.Fatalf("BusinessRead() error = %v", err)
	}
	if result.Value != "mysql" || result.DataSource != database.BackendMySQL ||
		result.Completeness != DataComplete || result.Fallback || sqliteCalls != 0 {
		t.Fatalf("BusinessRead() = %+v, SQLite calls = %d", result, sqliteCalls)
	}
}

func TestBusinessReadFallsBackOnlyForAvailabilityAndReportsCoverage(t *testing.T) {
	coverage := &fakeCoverageProvider{coverage: database.CacheCoverageStatus{
		Complete: false,
		FromMS:   1700000000000,
		ToMS:     1701296000000,
	}}
	routed := NewRoutedStore(&fakeRouteProvider{snapshot: mysqlBusinessRoute(4, 9)}, coverage)
	result, err := BusinessRead(context.Background(), routed, ReadOperations[int]{
		MySQL: func(context.Context) (int, error) {
			return 0, BackendUnavailable(database.BackendMySQL, "query", errors.New("connection reset"))
		},
		SQLite: func(context.Context) (int, error) { return 42, nil },
	})
	if err != nil {
		t.Fatalf("BusinessRead() error = %v", err)
	}
	if result.Value != 42 || result.DataSource != database.BackendSQLite || !result.Fallback ||
		result.Completeness != DataPartial || result.CoverageFromMS != coverage.coverage.FromMS ||
		result.CoverageToMS != coverage.coverage.ToMS || coverage.calls != 1 {
		t.Fatalf("BusinessRead() = %+v, coverage calls = %d", result, coverage.calls)
	}
}

func TestBusinessReadRecordsSelectedSourceAfterRouting(t *testing.T) {
	ctx, recorder := database.WithReadCoverageRecorder(context.Background())
	routed := NewRoutedStore(&fakeRouteProvider{snapshot: mysqlBusinessRoute(4, 9)},
		&fakeCoverageProvider{coverage: database.CacheCoverageStatus{FromMS: 10, ToMS: 20}})
	_, err := BusinessRead(ctx, routed, ReadOperations[int]{
		MySQL: func(context.Context) (int, error) {
			return 0, BackendUnavailable(database.BackendMySQL, "query", errors.New("offline"))
		},
		SQLite: func(context.Context) (int, error) { return 1, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	got := recorder.Snapshot()
	if got.DataSource != database.BackendSQLite || got.Completeness != database.DataPartial ||
		got.FromMS != 10 || got.ToMS != 20 {
		t.Fatalf("recorded coverage = %#v", got)
	}
}

func TestBusinessReadDoesNotHideMySQLErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "syntax", err: &drivermysql.MySQLError{Number: 1064, Message: "syntax error"}},
		{name: "unknown table", err: &drivermysql.MySQLError{Number: 1146, Message: "table missing"}},
		{name: "unknown column", err: &drivermysql.MySQLError{Number: 1054, Message: "column missing"}},
		{name: "permission", err: &drivermysql.MySQLError{Number: 1142, Message: "permission denied"}},
		{name: "data conversion", err: &drivermysql.MySQLError{Number: 1265, Message: "data truncated"}},
		{name: "scan", err: errors.New("sql: Scan error on column index 0")},
		{name: "bare deadline", err: context.DeadlineExceeded},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			routed := NewRoutedStore(&fakeRouteProvider{snapshot: mysqlBusinessRoute(1, 1)}, nil)
			sqliteCalls := 0
			_, err := BusinessRead(context.Background(), routed, ReadOperations[int]{
				MySQL: func(context.Context) (int, error) { return 0, test.err },
				SQLite: func(context.Context) (int, error) {
					sqliteCalls++
					return 1, nil
				},
			})
			if err != test.err {
				t.Fatalf("BusinessRead() error = %v, want original %v", err, test.err)
			}
			if sqliteCalls != 0 {
				t.Fatalf("SQLite fallback calls = %d, want 0", sqliteCalls)
			}
		})
	}
}

func TestSystemReadUsesIndependentSQLiteRoute(t *testing.T) {
	routed := NewRoutedStore(&fakeRouteProvider{snapshot: mysqlBusinessRoute(5, 11)}, nil)
	mysqlCalls := 0
	result, err := SystemRead(context.Background(), routed, ReadOperations[string]{
		SQLite: func(context.Context) (string, error) { return "config", nil },
		MySQL: func(context.Context) (string, error) {
			mysqlCalls++
			return "wrong", nil
		},
	})
	if err != nil {
		t.Fatalf("SystemRead() error = %v", err)
	}
	if result.Value != "config" || result.DataSource != database.BackendSQLite || mysqlCalls != 0 {
		t.Fatalf("SystemRead() = %+v, MySQL calls = %d", result, mysqlCalls)
	}
}

func TestWriteRejectsStaleGenerationAndEpochBeforeBackendCall(t *testing.T) {
	tests := []struct {
		name    string
		current RouteSnapshot
	}{
		{name: "generation", current: sqliteRoute(8, 20)},
		{name: "epoch", current: sqliteRoute(7, 21)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &fakeRouteProvider{snapshot: sqliteRoute(7, 20)}
			routed := NewRoutedStore(provider, nil)
			expected, err := routed.Snapshot(context.Background())
			if err != nil {
				t.Fatalf("Snapshot() error = %v", err)
			}
			provider.snapshot = test.current
			sqliteCalls, mysqlCalls := 0, 0
			err = routed.Write(context.Background(), expected, WriteOperations{
				SQLite: func(context.Context) error { sqliteCalls++; return nil },
				MySQL:  func(context.Context) error { mysqlCalls++; return nil },
			})
			if !errors.Is(err, ErrStaleRouteSnapshot) {
				t.Fatalf("Write() error = %v, want ErrStaleRouteSnapshot", err)
			}
			if sqliteCalls != 0 || mysqlCalls != 0 {
				t.Fatalf("backend calls sqlite=%d mysql=%d, want 0/0", sqliteCalls, mysqlCalls)
			}
		})
	}
}

func TestWriteExpressesMySQLPrimaryButKeepsCapabilityClosed(t *testing.T) {
	snapshot := mysqlBusinessRoute(12, 30)
	snapshot.WritePrimary = database.BackendMySQL
	routed := NewRoutedStore(&fakeRouteProvider{snapshot: snapshot}, nil)
	sqliteCalls, mysqlCalls := 0, 0
	err := routed.Write(context.Background(), snapshot, WriteOperations{
		SQLite: func(context.Context) error { sqliteCalls++; return nil },
		MySQL:  func(context.Context) error { mysqlCalls++; return nil },
	})
	if !errors.Is(err, ErrMySQLWriteNotReady) {
		t.Fatalf("Write() error = %v, want ErrMySQLWriteNotReady", err)
	}
	if sqliteCalls != 0 || mysqlCalls != 0 {
		t.Fatalf("backend calls sqlite=%d mysql=%d, want 0/0", sqliteCalls, mysqlCalls)
	}
}

func TestWriteUsesMySQLOnlyWhenCapabilityGatePasses(t *testing.T) {
	snapshot := mysqlBusinessRoute(13, 31)
	snapshot.WritePrimary = database.BackendMySQL
	routed := NewRoutedStoreWithMySQLWrites(
		&fakeRouteProvider{snapshot: snapshot}, nil, func() bool { return true },
	)
	sqliteCalls, mysqlCalls := 0, 0
	err := routed.Write(context.Background(), snapshot, WriteOperations{
		SQLite: func(context.Context) error { sqliteCalls++; return nil },
		MySQL:  func(context.Context) error { mysqlCalls++; return nil },
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if sqliteCalls != 0 || mysqlCalls != 1 {
		t.Fatalf("backend calls sqlite=%d mysql=%d, want 0/1", sqliteCalls, mysqlCalls)
	}
}

func TestIsBackendUnavailableRecognizesTransportFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "bad connection", err: driver.ErrBadConn},
		{name: "wrapped bad connection", err: fmt.Errorf("query: %w", driver.ErrBadConn)},
		{name: "network operation", err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}},
		{name: "explicit classification", err: BackendUnavailable(database.BackendMySQL, "ping", context.DeadlineExceeded)},
		{name: "too many connections", err: &drivermysql.MySQLError{Number: 1040, Message: "too many connections"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !IsBackendUnavailable(test.err) {
				t.Fatalf("IsBackendUnavailable(%v) = false", test.err)
			}
		})
	}
}

func mysqlBusinessRoute(generation, epoch uint64) RouteSnapshot {
	return RouteSnapshot{
		Generation:          generation,
		Epoch:               epoch,
		WritePrimary:        database.BackendSQLite,
		BusinessReadPrimary: database.BackendMySQL,
		SystemReadPrimary:   database.BackendSQLite,
	}
}

func sqliteRoute(generation, epoch uint64) RouteSnapshot {
	route := sqliteOnlyRoute()
	route.Generation = generation
	route.Epoch = epoch
	return route
}
