package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
)

const (
	DataComplete = database.DataComplete
	DataPartial  = database.DataPartial
)

var (
	// ErrBackendUnavailable is the only application-level error category that
	// permits a routed read to try its fallback backend.
	ErrBackendUnavailable   = errors.New("database backend unavailable")
	ErrBackendNotConfigured = errors.New("database backend is not configured")
	ErrInvalidRoute         = errors.New("invalid database route")
	ErrStaleRouteSnapshot   = errors.New("stale database route snapshot")
	// ErrMySQLWriteNotReady deliberately keeps failover closed until the MySQL
	// repository conformance suite and durable write fence are wired in.
	ErrMySQLWriteNotReady = errors.New("mysql write primary is not ready")
)

// RouteSnapshot is re-exported for store callers while the canonical routing
// contract lives in the database package and can be implemented by the
// database-management runtime without an import cycle.
type RouteSnapshot = database.RouteSnapshot

// RouteSnapshotProvider supplies the latest control-plane routing state.
// Implementations must return a coherent snapshot from one atomic read.
type RouteSnapshotProvider interface {
	RouteSnapshot(context.Context) (RouteSnapshot, error)
}

// CacheCoverageProvider reports the verified range retained by SQLite after a
// business read falls back from MySQL.
type CacheCoverageProvider interface {
	CacheCoverage(context.Context) (database.CacheCoverageStatus, error)
}

// BackendProvider pins a live backend handle for the duration of one routed
// repository call. The release callback must always be invoked; runtimes use
// it to prevent reconnects and atomic SQLite cache replacement from closing a
// handle underneath an in-flight query.
type BackendProvider interface {
	AcquireBackend(context.Context, database.BackendKind) (database.Backend, func(), error)
}

type RoutedBackendProvider interface {
	RouteSnapshotProvider
	CacheCoverageProvider
	BackendProvider
}

// RoutedStore contains routing policy only. Backend-specific repositories are
// intentionally supplied as operations, so this type does not imply that the
// existing SQLite SQL repositories already conform to MySQL.
type RoutedStore struct {
	routes          RouteSnapshotProvider
	coverage        CacheCoverageProvider
	mysqlWriteReady func() bool
}

func NewRoutedStore(routes RouteSnapshotProvider, coverage CacheCoverageProvider) *RoutedStore {
	return &RoutedStore{routes: routes, coverage: coverage}
}

func NewRoutedStoreWithMySQLWrites(
	routes RouteSnapshotProvider,
	coverage CacheCoverageProvider,
	ready func() bool,
) *RoutedStore {
	return &RoutedStore{routes: routes, coverage: coverage, mysqlWriteReady: ready}
}

// Snapshot returns the current route. A nil provider is the legacy
// SQLite-only mode and preserves the existing read/write choices.
func (r *RoutedStore) Snapshot(ctx context.Context) (RouteSnapshot, error) {
	if r == nil || r.routes == nil {
		return sqliteOnlyRoute(), nil
	}
	snapshot, err := r.routes.RouteSnapshot(ctx)
	if err != nil {
		return RouteSnapshot{}, err
	}
	if err := validateRoute(snapshot); err != nil {
		return RouteSnapshot{}, err
	}
	return snapshot, nil
}

type WriteOperation func(context.Context) error

type WriteOperations struct {
	SQLite WriteOperation
	MySQL  WriteOperation
}

// Write executes the current primary only after checking the caller's route
// token. This is an entry-point fence; the eventual MySQL repository must also
// assert the epoch inside its transaction before this readiness guard can be
// removed.
func (r *RoutedStore) Write(ctx context.Context, expected RouteSnapshot, operations WriteOperations) error {
	current, err := r.Snapshot(ctx)
	if err != nil {
		return err
	}
	if !sameRouteToken(expected, current) {
		return fmt.Errorf(
			"%w: expected generation=%d epoch=%d write=%s, current generation=%d epoch=%d write=%s",
			ErrStaleRouteSnapshot,
			expected.Generation,
			expected.Epoch,
			expected.WritePrimary,
			current.Generation,
			current.Epoch,
			current.WritePrimary,
		)
	}

	switch current.WritePrimary {
	case database.BackendSQLite:
		if operations.SQLite == nil {
			return backendNotConfigured(database.BackendSQLite)
		}
		return operations.SQLite(ctx)
	case database.BackendMySQL:
		if r.mysqlWriteReady == nil || !r.mysqlWriteReady() {
			return ErrMySQLWriteNotReady
		}
		if operations.MySQL == nil {
			return backendNotConfigured(database.BackendMySQL)
		}
		return operations.MySQL(ctx)
	default:
		return invalidBackend("write primary", current.WritePrimary)
	}
}

type ReadOperation[T any] func(context.Context) (T, error)

type ReadOperations[T any] struct {
	SQLite ReadOperation[T]
	MySQL  ReadOperation[T]
}

type RoutedResult[T any] struct {
	Value          T
	DataSource     database.BackendKind
	Completeness   string
	CoverageFromMS int64
	CoverageToMS   int64
	Fallback       bool
}

// BusinessRead uses the configured business-read primary. Only a classified
// backend availability error permits trying the other backend; query, schema,
// permission, constraint, scan and data errors are returned unchanged.
func BusinessRead[T any](ctx context.Context, routed *RoutedStore, operations ReadOperations[T]) (RoutedResult[T], error) {
	snapshot, err := routed.Snapshot(ctx)
	if err != nil {
		return RoutedResult[T]{}, err
	}
	result, err := readWithPolicy(ctx, routed, snapshot.BusinessReadPrimary, true, operations)
	if err == nil && routed != nil && routed.routes != nil {
		database.RecordReadCoverage(ctx, database.ReadCoverage{
			DataSource: result.DataSource, Completeness: result.Completeness,
			FromMS: result.CoverageFromMS, ToMS: result.CoverageToMS,
		})
	}
	return result, err
}

// SystemRead follows the independent system/configuration route (SQLite by
// default). It never consults or mutates the business-read route.
func SystemRead[T any](ctx context.Context, routed *RoutedStore, operations ReadOperations[T]) (RoutedResult[T], error) {
	snapshot, err := routed.Snapshot(ctx)
	if err != nil {
		return RoutedResult[T]{}, err
	}
	return readWithPolicy(ctx, routed, snapshot.SystemReadPrimary, false, operations)
}

func readWithPolicy[T any](
	ctx context.Context,
	routed *RoutedStore,
	primary database.BackendKind,
	business bool,
	operations ReadOperations[T],
) (RoutedResult[T], error) {
	primaryOperation, fallbackOperation, fallbackSource, err := selectReadOperations(primary, operations)
	if err != nil {
		return RoutedResult[T]{}, err
	}
	if primaryOperation == nil {
		return RoutedResult[T]{}, backendNotConfigured(primary)
	}

	value, primaryErr := primaryOperation(ctx)
	if primaryErr == nil {
		return RoutedResult[T]{
			Value: value, DataSource: primary, Completeness: DataComplete,
		}, nil
	}
	if !IsBackendUnavailable(primaryErr) {
		return RoutedResult[T]{}, primaryErr
	}
	if fallbackOperation == nil {
		return RoutedResult[T]{}, &FallbackError{
			Primary: primary, PrimaryErr: primaryErr,
			Fallback: fallbackSource, FallbackErr: backendNotConfigured(fallbackSource),
		}
	}

	value, fallbackErr := fallbackOperation(ctx)
	if fallbackErr != nil {
		return RoutedResult[T]{}, &FallbackError{
			Primary: primary, PrimaryErr: primaryErr,
			Fallback: fallbackSource, FallbackErr: fallbackErr,
		}
	}
	result := RoutedResult[T]{
		Value: value, DataSource: fallbackSource, Completeness: DataComplete, Fallback: true,
	}
	if business && fallbackSource == database.BackendSQLite {
		result.Completeness = DataPartial
		if routed != nil && routed.coverage != nil {
			coverage, coverageErr := routed.coverage.CacheCoverage(ctx)
			if coverageErr == nil {
				result.CoverageFromMS = coverage.FromMS
				result.CoverageToMS = coverage.ToMS
				if coverage.Complete {
					result.Completeness = DataComplete
				}
			}
		}
	}
	return result, nil
}

func selectReadOperations[T any](
	primary database.BackendKind,
	operations ReadOperations[T],
) (ReadOperation[T], ReadOperation[T], database.BackendKind, error) {
	switch primary {
	case database.BackendSQLite:
		return operations.SQLite, operations.MySQL, database.BackendMySQL, nil
	case database.BackendMySQL:
		return operations.MySQL, operations.SQLite, database.BackendSQLite, nil
	default:
		return nil, nil, "", invalidBackend("read primary", primary)
	}
}

// BackendUnavailableError explicitly classifies a failure discovered while
// connecting to, pinging or communicating with a backend.
type BackendUnavailableError struct {
	Backend   database.BackendKind
	Operation string
	Err       error
}

func (e *BackendUnavailableError) Error() string {
	if e == nil {
		return ErrBackendUnavailable.Error()
	}
	if e.Err == nil {
		return fmt.Sprintf("%s %s: %v", e.Backend, e.Operation, ErrBackendUnavailable)
	}
	return fmt.Sprintf("%s %s: %v", e.Backend, e.Operation, e.Err)
}

func (e *BackendUnavailableError) Unwrap() error {
	if e == nil || e.Err == nil {
		return ErrBackendUnavailable
	}
	return e.Err
}

func (e *BackendUnavailableError) Is(target error) bool {
	return target == ErrBackendUnavailable
}

func BackendUnavailable(backend database.BackendKind, operation string, err error) error {
	return &BackendUnavailableError{Backend: backend, Operation: operation, Err: err}
}

// IsBackendUnavailable is deliberately conservative. In particular, a bare
// context deadline is not enough to hide a slow/invalid SQL statement behind
// fallback data; it must be part of an explicit network operation or be
// classified by the backend adapter.
func IsBackendUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrBackendUnavailable) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr *net.OpError
	if errors.As(err, &networkErr) {
		return true
	}
	if isConnectionSyscall(err) {
		return true
	}
	var mysqlErr *drivermysql.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1040, // ER_CON_COUNT_ERROR
			1053, // ER_SERVER_SHUTDOWN
			2002, // CR_CONNECTION_ERROR
			2003, // CR_CONN_HOST_ERROR
			2006, // CR_SERVER_GONE_ERROR
			2013: // CR_SERVER_LOST
			return true
		}
	}
	return false
}

func isConnectionSyscall(err error) bool {
	var syscallErr *os.SyscallError
	if !errors.As(err, &syscallErr) {
		return false
	}
	return errors.Is(syscallErr, syscall.ECONNREFUSED) ||
		errors.Is(syscallErr, syscall.ECONNRESET) ||
		errors.Is(syscallErr, syscall.ENETUNREACH) ||
		errors.Is(syscallErr, syscall.EHOSTUNREACH) ||
		errors.Is(syscallErr, syscall.ETIMEDOUT) ||
		errors.Is(syscallErr, syscall.EPIPE)
}

type FallbackError struct {
	Primary     database.BackendKind
	PrimaryErr  error
	Fallback    database.BackendKind
	FallbackErr error
}

func (e *FallbackError) Error() string {
	if e == nil {
		return "database fallback failed"
	}
	return fmt.Sprintf(
		"%s unavailable (%v); %s fallback failed (%v)",
		e.Primary,
		e.PrimaryErr,
		e.Fallback,
		e.FallbackErr,
	)
}

func (e *FallbackError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return []error{e.PrimaryErr, e.FallbackErr}
}

func sqliteOnlyRoute() RouteSnapshot {
	return RouteSnapshot{
		WritePrimary:        database.BackendSQLite,
		BusinessReadPrimary: database.BackendSQLite,
		SystemReadPrimary:   database.BackendSQLite,
	}
}

func validateRoute(snapshot RouteSnapshot) error {
	if err := validateBackend("write primary", snapshot.WritePrimary); err != nil {
		return err
	}
	if err := validateBackend("business read primary", snapshot.BusinessReadPrimary); err != nil {
		return err
	}
	return validateBackend("system read primary", snapshot.SystemReadPrimary)
}

func validateBackend(field string, kind database.BackendKind) error {
	if kind == database.BackendSQLite || kind == database.BackendMySQL {
		return nil
	}
	return invalidBackend(field, kind)
}

func invalidBackend(field string, kind database.BackendKind) error {
	return fmt.Errorf("%w: %s %q", ErrInvalidRoute, field, kind)
}

func backendNotConfigured(kind database.BackendKind) error {
	return fmt.Errorf("%w: %s", ErrBackendNotConfigured, kind)
}

func sameRouteToken(expected, current RouteSnapshot) bool {
	return expected.Generation == current.Generation &&
		expected.Epoch == current.Epoch &&
		expected.WritePrimary == current.WritePrimary
}
