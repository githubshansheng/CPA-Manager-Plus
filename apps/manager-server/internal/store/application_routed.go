package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/outboxcontext"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

// NewRoutedApplicationStore creates the Store facade used by application
// services and workers. Legacy SQLite-only stores remain unchanged; the facade
// obtains a pinned backend for every call so reconnects and cache replacement
// cannot invalidate an in-flight repository operation.
func NewRoutedApplicationStore(
	ctx context.Context,
	provider RoutedBackendProvider,
	protector ...*security.Protector,
) (*Store, error) {
	return newRoutedApplicationStore(ctx, provider, func(backend database.Backend) (*Store, error) {
		return NewWithBackendChecked(backend, protector...)
	}, protector...)
}

func newRoutedApplicationStore(
	ctx context.Context,
	provider RoutedBackendProvider,
	builder func(database.Backend) (*Store, error),
	protector ...*security.Protector,
) (*Store, error) {
	if provider == nil {
		return nil, errors.New("routed application store requires a backend provider")
	}
	if builder == nil {
		return nil, errors.New("routed application store requires an endpoint builder")
	}
	result := &Store{
		provider:      provider,
		protectors:    append([]*security.Protector(nil), protector...),
		endpoints:     make(map[database.BackendKind]cachedEndpoint, 2),
		endpointBuild: builder,
	}
	// Manual failover is separately gated by the database-management runtime.
	// Once that durable gate changes the route, this store must actually honor
	// the selected MySQL operation instead of retaining the policy-only guard.
	result.router = NewRoutedStoreWithMySQLWrites(provider, provider, func() bool { return true })

	// SQLite is mandatory in the normal application mode. Eager construction
	// also preserves public repository fields for existing tests and extensions;
	// production code routes through Store methods and does not use these fields.
	sqlite, release, err := result.endpoint(ctx, database.BackendSQLite)
	if err != nil {
		return nil, err
	}
	defer release()
	result.installCompatibilityRepositories(sqlite)
	return result, nil
}

func (s *Store) IsRouted() bool { return s != nil && s.router != nil && s.provider != nil }

// PrepareBackend runs the checked repository constructors once for the live
// backend handle. It is used as the concrete conformance gate before exposing
// read cutover or write failover controls.
func (s *Store) PrepareBackend(ctx context.Context, kind database.BackendKind) error {
	if s == nil {
		return errors.New("store is nil")
	}
	if !s.IsRouted() {
		if s.backend != nil && s.backend.Kind() == kind {
			return nil
		}
		return backendNotConfigured(kind)
	}
	_, release, err := s.endpoint(ctx, kind)
	if release != nil {
		release()
	}
	return err
}

func (s *Store) endpoint(
	ctx context.Context,
	kind database.BackendKind,
) (*Store, func(), error) {
	if s == nil || s.provider == nil {
		return nil, func() {}, backendNotConfigured(kind)
	}
	backend, release, err := s.provider.AcquireBackend(ctx, kind)
	if err != nil {
		return nil, func() {}, BackendUnavailable(kind, "acquire", err)
	}
	if release == nil {
		release = func() {}
	}
	if backend == nil || backend.DB() == nil || backend.Kind() != kind {
		release()
		return nil, func() {}, fmt.Errorf("%w: provider returned %v for %s", ErrInvalidRoute, backend, kind)
	}

	s.endpointMu.Lock()
	cached := s.endpoints[kind]
	if cached.db == backend.DB() && cached.store != nil {
		s.endpointMu.Unlock()
		return cached.store, release, nil
	}
	configured, configureErr := s.endpointBuild(backend)
	if configureErr == nil {
		s.endpoints[kind] = cachedEndpoint{db: backend.DB(), store: configured}
	}
	s.endpointMu.Unlock()
	if configureErr != nil {
		release()
		if IsBackendUnavailable(configureErr) {
			return nil, func() {}, BackendUnavailable(kind, "prepare repositories", configureErr)
		}
		return nil, func() {}, configureErr
	}
	return configured, release, nil
}

func (s *Store) installCompatibilityRepositories(endpoint *Store) {
	if s == nil || endpoint == nil {
		return
	}
	s.db = endpoint.db
	s.backend = endpoint.backend
	s.Settings = endpoint.Settings
	s.UsageEvents = endpoint.UsageEvents
	s.DeadLetters = endpoint.DeadLetters
	s.ModelPrices = endpoint.ModelPrices
	s.APIKeyAliases = endpoint.APIKeyAliases
	s.AccountActions = endpoint.AccountActions
	s.CodexInspections = endpoint.CodexInspections
	s.DataMigrations = endpoint.DataMigrations
	s.QuotaCooldowns = endpoint.QuotaCooldowns
	s.QuotaSnapshots = endpoint.QuotaSnapshots
	s.UsageAggregates = endpoint.UsageAggregates
	s.UsagePricing = endpoint.UsagePricing
	s.UsageMonitoring = endpoint.UsageMonitoring
	s.UsageRollups = endpoint.UsageRollups
}

func routedWriteValue[T any](
	ctx context.Context,
	s *Store,
	operation func(context.Context, *Store) (T, error),
) (T, error) {
	var zero T
	if s == nil {
		return zero, errors.New("store is nil")
	}
	if !s.IsRouted() {
		return operation(ctx, s)
	}
	snapshot, err := s.router.Snapshot(ctx)
	if err != nil {
		return zero, err
	}
	var value T
	err = s.router.Write(ctx, snapshot, WriteOperations{
		SQLite: func(callCtx context.Context) error {
			value, err = callUnpinnedEndpoint(callCtx, s, database.BackendSQLite, operation)
			return err
		},
		MySQL: func(callCtx context.Context) error {
			value, err = callUnpinnedEndpoint(callCtx, s, database.BackendMySQL, operation)
			return err
		},
	})
	if err != nil {
		return zero, err
	}
	return value, nil
}

func routedWrite(
	ctx context.Context,
	s *Store,
	operation func(context.Context, *Store) error,
) error {
	_, err := routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (struct{}, error) {
		return struct{}{}, operation(callCtx, endpoint)
	})
	return err
}

func routedBusinessValue[T any](
	ctx context.Context,
	s *Store,
	operation func(context.Context, *Store) (T, error),
) (T, error) {
	var zero T
	if s == nil {
		return zero, errors.New("store is nil")
	}
	if !s.IsRouted() {
		return operation(ctx, s)
	}
	result, err := BusinessRead(ctx, s.router, ReadOperations[T]{
		SQLite: func(callCtx context.Context) (T, error) {
			return callEndpoint(callCtx, s, database.BackendSQLite, operation)
		},
		MySQL: func(callCtx context.Context) (T, error) {
			return callEndpoint(callCtx, s, database.BackendMySQL, operation)
		},
	})
	if err != nil {
		return zero, err
	}
	return result.Value, nil
}

func routedSystemValue[T any](
	ctx context.Context,
	s *Store,
	operation func(context.Context, *Store) (T, error),
) (T, error) {
	var zero T
	if s == nil {
		return zero, errors.New("store is nil")
	}
	if !s.IsRouted() {
		return operation(ctx, s)
	}
	result, err := SystemRead(ctx, s.router, ReadOperations[T]{
		SQLite: func(callCtx context.Context) (T, error) {
			return callEndpoint(callCtx, s, database.BackendSQLite, operation)
		},
		MySQL: func(callCtx context.Context) (T, error) {
			return callEndpoint(callCtx, s, database.BackendMySQL, operation)
		},
	})
	if err != nil {
		return zero, err
	}
	return result.Value, nil
}

func routedSQLiteValue[T any](
	ctx context.Context,
	s *Store,
	operation func(context.Context, *Store) (T, error),
) (T, error) {
	var zero T
	if s == nil {
		return zero, errors.New("store is nil")
	}
	if !s.IsRouted() {
		return operation(ctx, s)
	}
	return callEndpoint(ctx, s, database.BackendSQLite, operation)
}

// routedDerivedValue advances independently-owned derived state on every live
// backend. Availability loss on a non-primary backend is tolerated so SQLite
// ingestion keeps moving while MySQL is offline; SQL/schema/data errors remain
// visible. The caller-provided merge combines worker scheduling flags such as
// Pending and ContinueSoon.
func routedDerivedValue[T any](
	ctx context.Context,
	s *Store,
	operation func(context.Context, *Store) (T, error),
	merge func(*T, T),
) (T, error) {
	var zero T
	if s == nil {
		return zero, errors.New("store is nil")
	}
	if !s.IsRouted() {
		release, err := outboxcontext.AcquireWriteOperation(ctx, s.db)
		if err != nil {
			return zero, err
		}
		defer release()
		return operation(ctx, s)
	}
	snapshot, err := s.router.Snapshot(ctx)
	if err != nil {
		return zero, err
	}
	primary := snapshot.BusinessReadPrimary
	order := []database.BackendKind{primary}
	if primary == database.BackendSQLite {
		order = append(order, database.BackendMySQL)
	} else {
		order = append(order, database.BackendSQLite)
	}
	var result T
	haveResult := false
	var primaryErr error
	for _, kind := range order {
		value, callErr := callDerivedEndpoint(ctx, s, kind, operation)
		if callErr != nil {
			if kind == primary {
				primaryErr = callErr
				continue
			}
			if IsBackendUnavailable(callErr) || errors.Is(callErr, ErrBackendNotConfigured) {
				continue
			}
			return zero, callErr
		}
		if !haveResult {
			result = value
			haveResult = true
		} else if merge != nil {
			merge(&result, value)
		}
	}
	if primaryErr != nil {
		return zero, primaryErr
	}
	if !haveResult {
		return zero, backendNotConfigured(primary)
	}
	return result, nil
}

func callDerivedEndpoint[T any](
	ctx context.Context,
	s *Store,
	kind database.BackendKind,
	operation func(context.Context, *Store) (T, error),
) (T, error) {
	var zero T
	for {
		endpoint, releaseBackend, err := s.endpoint(ctx, kind)
		if err != nil {
			return zero, err
		}
		db := endpoint.db
		releaseBackend()
		releaseOperation, err := outboxcontext.AcquireWriteOperation(ctx, db)
		if err != nil {
			return zero, err
		}
		current, releaseCurrent, err := s.endpoint(ctx, kind)
		if err != nil {
			releaseOperation()
			return zero, err
		}
		if current.db != db {
			releaseCurrent()
			releaseOperation()
			continue
		}
		value, operationErr := operation(ctx, current)
		releaseCurrent()
		releaseOperation()
		return value, operationErr
	}
}

func routedBusinessStream(
	ctx context.Context,
	s *Store,
	writer io.Writer,
	operation func(context.Context, *Store, io.Writer) error,
) error {
	if s == nil {
		return errors.New("store is nil")
	}
	if !s.IsRouted() {
		return operation(ctx, s, writer)
	}
	path, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (string, error) {
		file, createErr := os.CreateTemp("", "cpamp-routed-export-*.tmp")
		if createErr != nil {
			return "", createErr
		}
		path := file.Name()
		operationErr := operation(callCtx, endpoint, file)
		closeErr := file.Close()
		if operationErr != nil || closeErr != nil {
			_ = os.Remove(path)
			return "", errors.Join(operationErr, closeErr)
		}
		return path, nil
	})
	if err != nil {
		return err
	}
	defer os.Remove(path)
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(writer, file)
	return errors.Join(copyErr, file.Close())
}

func callEndpoint[T any](
	ctx context.Context,
	s *Store,
	kind database.BackendKind,
	operation func(context.Context, *Store) (T, error),
) (T, error) {
	var zero T
	endpoint, release, err := s.endpoint(ctx, kind)
	if err != nil {
		return zero, err
	}
	defer release()
	return operation(ctx, endpoint)
}

func callUnpinnedEndpoint[T any](
	ctx context.Context,
	s *Store,
	kind database.BackendKind,
	operation func(context.Context, *Store) (T, error),
) (T, error) {
	var zero T
	endpoint, release, err := s.endpoint(ctx, kind)
	if err != nil {
		return zero, err
	}
	release()
	return operation(ctx, endpoint)
}
