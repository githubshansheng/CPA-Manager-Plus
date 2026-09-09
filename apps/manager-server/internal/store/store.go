package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/accountaction"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/apikeyalias"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/codexinspection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/datamigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/deadletter"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/modelprice"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotacooldown"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaggregate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagemonitoring"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagepricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagerollup"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

type Setup = model.Setup
type ManagerConfig = model.ManagerConfig
type AdminCredential = model.AdminCredential
type BootstrapState = model.BootstrapState
type ManagerCPAConnectionConfig = model.ManagerCPAConnectionConfig
type ManagerCollectorConfig = model.ManagerCollectorConfig
type ManagerCodexInspectionConfig = model.ManagerCodexInspectionConfig
type ManagerCodexInspectionScheduleConfig = model.ManagerCodexInspectionScheduleConfig
type ManagerExternalUsageServiceConfig = model.ManagerExternalUsageServiceConfig
type ManagerCustomPageConfig = model.ManagerCustomPageConfig
type CodexInspectionRun = model.CodexInspectionRun
type CodexInspectionResult = model.CodexInspectionResult
type CodexInspectionLog = model.CodexInspectionLog
type CodexInspectionDisableOwnership = model.CodexInspectionDisableOwnership
type CodexInspectionLease = model.CodexInspectionLease
type InsertResult = model.InsertResult
type LegacyQuotaSnapshotBackfillResult = quotasnapshot.LegacyBackfillResult
type ModelPrice = model.ModelPrice
type ModelPriceContextTier = model.ModelPriceContextTier
type ModelPriceServiceTier = model.ModelPriceServiceTier
type ModelPriceSyncResult = model.ModelPriceSyncResult
type ModelUsageStat = model.ModelUsageStat
type ModelUsageSummary = model.ModelUsageSummary
type APIKeyAlias = model.APIKeyAlias
type QuotaCooldown = model.QuotaCooldown
type QuotaCooldownUpsert = model.QuotaCooldownUpsert
type AccountQuotaSnapshot = model.AccountQuotaSnapshot
type AccountQuotaObservationWrite = model.AccountQuotaObservationWrite
type AccountQuotaWindowState = model.AccountQuotaWindowState
type AccountActionCandidate = model.AccountActionCandidate
type AccountActionCandidateUpsert = model.AccountActionCandidateUpsert
type AutomationSettings = model.AutomationSettings
type DataMigrationState = datamigration.State
type DataMigrationBatchResult = datamigration.BatchResult

var DefaultCodexInspectionConfig = model.DefaultCodexInspectionConfig
var NormalizeCodexInspectionConfig = model.NormalizeCodexInspectionConfig

// Aggregation result types re-exported for service-layer consumers.
type Aggregate = usageevent.Aggregate
type ModelStat = usageevent.ModelStat
type RecentFailure = usageevent.RecentFailure
type AnalyticsFilter = usageevent.AnalyticsFilter
type TimelinePoint = usageevent.TimelinePoint
type LatencyPercentiles = usageevent.LatencyPercentiles
type LatencySummary = usageevent.LatencySummary
type HourlyPoint = usageevent.HourlyPoint
type FilterOptionValues = usageevent.FilterOptionValues
type FilterSelectorValues = usageevent.FilterSelectorValues
type HeatmapPoint = usageevent.HeatmapPoint
type ChannelModelStat = usageevent.ChannelModelStat
type FailureSourceStat = usageevent.FailureSourceStat
type AccountModelStat = usageevent.AccountModelStat
type CredentialModelStat = usageevent.CredentialModelStat
type CredentialTimelinePoint = usageevent.CredentialTimelinePoint
type APIKeyTimelinePoint = usageevent.APIKeyTimelinePoint
type APIKeyModelStat = usageevent.APIKeyModelStat
type TaskBucket = usageevent.TaskBucket
type EventPageItem = usageevent.EventPageItem
type EventsPage = usageevent.EventsPage
type HeaderSnapshot = usageevent.HeaderSnapshot
type AccountWindowUsageQuery = usageevent.AccountWindowUsageQuery
type AccountWindowModelStat = usageevent.AccountWindowModelStat
type LatestAccountRequestQuery = usageevent.LatestAccountRequestQuery
type LatestAccountRequest = usageevent.LatestAccountRequest
type UsageRollupCheckpoint = usagerollup.Checkpoint
type UsageRollupCatchUpResult = usagerollup.CatchUpResult
type AccountHistoryRollupRow = usagerollup.AccountHistoryRow
type DashboardHourlyRollupRow = usagerollup.DashboardHourlyRow
type UsageHourlyAggregateState = usageaggregate.State
type UsageHourlyAggregateCatchUpResult = usageaggregate.CatchUpResult
type UsageHourlyAggregateFilter = usageaggregate.Filter
type UsageHourlyAggregateRow = usageaggregate.Row
type UsagePricingState = usagepricing.State
type UsagePricingCatchUpResult = usagepricing.CatchUpResult
type UsagePricingHourlyFilter = usagepricing.HourlyFilter
type UsagePricingHourlyRow = usagepricing.HourlyRow
type UsagePricingAccountRow = usagepricing.AccountRow
type UsageMonitoringState = usagemonitoring.State
type UsageMonitoringCatchUpResult = usagemonitoring.CatchUpResult

type UsageHourlyPricingSnapshot struct {
	AggregateRows      []UsageHourlyAggregateRow
	AggregateState     UsageHourlyAggregateState
	AggregateAvailable bool
	PricingRows        []UsagePricingHourlyRow
	PricingState       UsagePricingState
	PricingAvailable   bool
	Prices             map[string]ModelPrice
}

type UsagePricingAccountSnapshot struct {
	Rows      []UsagePricingAccountRow
	State     UsagePricingState
	Available bool
	Prices    map[string]ModelPrice
}

type Store struct {
	db            *sql.DB
	backend       database.Backend
	modelPricesMu sync.RWMutex
	router        *RoutedStore
	provider      RoutedBackendProvider
	protectors    []*security.Protector
	endpointMu    sync.Mutex
	endpoints     map[database.BackendKind]cachedEndpoint
	endpointBuild func(database.Backend) (*Store, error)

	Settings         setting.Repository
	UsageEvents      usageevent.Repository
	DeadLetters      deadletter.Repository
	ModelPrices      modelprice.Repository
	APIKeyAliases    apikeyalias.Repository
	AccountActions   accountaction.Repository
	CodexInspections codexinspection.Repository
	DataMigrations   datamigration.Repository
	QuotaCooldowns   quotacooldown.Repository
	QuotaSnapshots   quotasnapshot.Repository
	UsageAggregates  usageaggregate.Repository
	UsagePricing     usagepricing.Repository
	UsageMonitoring  usagemonitoring.Repository
	UsageRollups     usagerollup.Repository
}

type cachedEndpoint struct {
	db    *sql.DB
	store *Store
}

func Open(path string, protector ...*security.Protector) (*Store, error) {
	db, err := sqliterepo.Open(path)
	if err != nil {
		return nil, err
	}
	return New(db, protector...), nil
}

func New(db *sql.DB, protector ...*security.Protector) *Store {
	return NewWithBackend(database.NewSQLBackend(database.BackendSQLite, db), protector...)
}

// NewWithBackend centralizes the database lifecycle behind the shared backend
// contract. Repositories with shared conformance suites select the backend
// dialect; the remaining business and derived repositories stay on SQLite
// until their own MySQL suites are enabled.
func NewWithBackend(backend database.Backend, protector ...*security.Protector) *Store {
	configured, err := NewWithBackendChecked(backend, protector...)
	if err != nil {
		panic(err)
	}
	return configured
}

// NewWithBackendChecked constructs every repository for the selected dialect
// and validates the MySQL session contract before the Store can be installed
// behind a production route. The legacy constructor above remains convenient
// for the already-opened SQLite path while failing closed for an invalid MySQL
// session.
func NewWithBackendChecked(backend database.Backend, protector ...*security.Protector) (*Store, error) {
	if backend == nil || backend.DB() == nil {
		return nil, errors.New("store backend is required")
	}
	db := backend.DB()
	usageEvents, err := usageevent.NewForBackend(db, backend.Kind())
	if err != nil {
		return nil, err
	}
	usageAggregates := usageaggregate.NewForBackend(db, backend.Kind())
	usagePricingRepository := usagepricing.NewForBackend(db, backend.Kind())
	usageMonitoringRepository := usagemonitoring.NewForBackend(db, backend.Kind())
	usageRollups := usagerollup.NewForBackend(db, backend.Kind())
	if backend.Kind() == database.BackendMySQL {
		if usageAggregates, err = usageaggregate.NewMySQL(db); err != nil {
			return nil, err
		}
		if usagePricingRepository, err = usagepricing.NewMySQL(db); err != nil {
			return nil, err
		}
		if usageMonitoringRepository, err = usagemonitoring.NewMySQL(db); err != nil {
			return nil, err
		}
		if usageRollups, err = usagerollup.NewMySQL(db); err != nil {
			return nil, err
		}
	}
	return &Store{
		db:               db,
		backend:          backend,
		Settings:         setting.NewForBackend(db, backend.Kind(), protector...),
		UsageEvents:      usageEvents,
		DeadLetters:      deadletter.NewForBackend(db, backend.Kind()),
		ModelPrices:      modelprice.NewForBackend(db, backend.Kind()),
		APIKeyAliases:    apikeyalias.NewForBackend(db, backend.Kind()),
		AccountActions:   accountaction.NewForBackend(db, backend.Kind()),
		CodexInspections: codexinspection.NewForBackend(db, backend.Kind()),
		DataMigrations:   datamigration.New(db),
		QuotaCooldowns:   quotacooldown.NewForBackend(db, backend.Kind()),
		QuotaSnapshots:   quotasnapshot.NewForBackend(db, backend.Kind()),
		UsageAggregates:  usageAggregates,
		UsagePricing:     usagePricingRepository,
		UsageMonitoring:  usageMonitoringRepository,
		UsageRollups:     usageRollups,
	}, nil
}

func (s *Store) Backend() database.Backend {
	if s == nil {
		return nil
	}
	return s.backend
}

func (s *Store) Close() error {
	if s != nil && s.IsRouted() {
		return nil
	}
	if s == nil || s.backend == nil {
		return nil
	}
	return s.backend.Close()
}

func (s *Store) StartDerivedMaintenance(ctx context.Context) {
	if s == nil {
		return
	}
	if s.IsRouted() {
		endpoint, release, err := s.endpoint(ctx, database.BackendSQLite)
		if err != nil {
			return
		}
		sqliterepo.StartDerivedMaintenance(ctx, endpoint.db)
		release()
		return
	}
	sqliterepo.StartDerivedMaintenance(ctx, s.db)
}

func (s *Store) RunDerivedStartupMaintenance(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.IsRouted() {
		_, err := routedSQLiteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (struct{}, error) {
			return struct{}{}, sqliterepo.RunDerivedStartupMaintenance(callCtx, endpoint.db)
		})
		return err
	}
	return sqliterepo.RunDerivedStartupMaintenance(ctx, s.db)
}

func (s *Store) DerivedMaintenanceStatus(ctx context.Context) (sqliterepo.DerivedMaintenanceStatus, error) {
	if s == nil {
		return sqliterepo.DerivedMaintenanceStatus{Reasons: []string{}}, nil
	}
	if s.IsRouted() {
		return routedSQLiteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (sqliterepo.DerivedMaintenanceStatus, error) {
			return sqliterepo.ReadDerivedMaintenanceStatus(callCtx, endpoint.db)
		})
	}
	return sqliterepo.ReadDerivedMaintenanceStatus(ctx, s.db)
}

func (s *Store) BackfillLegacyQuotaSnapshotsBatch(ctx context.Context, maxGroupSize int) (LegacyQuotaSnapshotBackfillResult, error) {
	if s == nil {
		return LegacyQuotaSnapshotBackfillResult{Completed: true}, nil
	}
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (LegacyQuotaSnapshotBackfillResult, error) {
		return quotasnapshot.BackfillLegacySnapshotsBatchForBackend(
			callCtx,
			endpoint.db,
			endpoint.backend.Kind(),
			maxGroupSize,
		)
	})
}

func (s *Store) RecordLegacyQuotaSnapshotBackfillFailure(ctx context.Context, migrationErr error) error {
	if s == nil {
		return nil
	}
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return quotasnapshot.RecordLegacyBackfillFailure(callCtx, endpoint.db, migrationErr)
	})
}

func (s *Store) SaveSetup(ctx context.Context, setup Setup) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.Settings.SaveSetup(callCtx, setup)
	})
}

func (s *Store) LoadSetup(ctx context.Context) (Setup, bool, error) {
	type result struct {
		value Setup
		found bool
	}
	loaded, err := routedSystemValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, found, loadErr := endpoint.Settings.LoadSetup(callCtx)
		return result{value: value, found: found}, loadErr
	})
	return loaded.value, loaded.found, err
}

func (s *Store) SaveManagerConfig(ctx context.Context, cfg ManagerConfig) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.Settings.SaveManagerConfig(callCtx, cfg)
	})
}

func (s *Store) SaveManagerConfigAndSetup(ctx context.Context, cfg ManagerConfig, setup Setup) error {
	return s.Settings.SaveManagerConfigAndSetup(ctx, cfg, setup)
}

func (s *Store) NormalizeLegacyConnectionStorage(ctx context.Context, cfg ManagerConfig, managerPresent bool, setup Setup, setupPresent bool) error {
	return s.Settings.NormalizeLegacyConnectionStorage(ctx, cfg, managerPresent, setup, setupPresent)
}

func (s *Store) LoadManagerConfig(ctx context.Context) (ManagerConfig, bool, error) {
	type result struct {
		value ManagerConfig
		found bool
	}
	loaded, err := routedSystemValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, found, loadErr := endpoint.Settings.LoadManagerConfig(callCtx)
		return result{value: value, found: found}, loadErr
	})
	return loaded.value, loaded.found, err
}

func (s *Store) SaveAutomationSettings(ctx context.Context, settings AutomationSettings) (AutomationSettings, error) {
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (AutomationSettings, error) {
		return endpoint.Settings.SaveAutomationSettings(callCtx, settings)
	})
}

func (s *Store) LoadAutomationSettings(ctx context.Context) (AutomationSettings, bool, error) {
	type result struct {
		value AutomationSettings
		found bool
	}
	loaded, err := routedSystemValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, found, loadErr := endpoint.Settings.LoadAutomationSettings(callCtx)
		return result{value: value, found: found}, loadErr
	})
	return loaded.value, loaded.found, err
}

func (s *Store) SaveAdminCredential(ctx context.Context, credential AdminCredential) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.Settings.SaveAdminCredential(callCtx, credential)
	})
}

func (s *Store) LoadAdminCredential(ctx context.Context) (AdminCredential, bool, error) {
	type result struct {
		value AdminCredential
		found bool
	}
	loaded, err := routedSystemValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, found, loadErr := endpoint.Settings.LoadAdminCredential(callCtx)
		return result{value: value, found: found}, loadErr
	})
	return loaded.value, loaded.found, err
}

func (s *Store) SaveBootstrapState(ctx context.Context, state BootstrapState) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.Settings.SaveBootstrapState(callCtx, state)
	})
}

func (s *Store) LoadBootstrapState(ctx context.Context) (BootstrapState, bool, error) {
	type result struct {
		value BootstrapState
		found bool
	}
	loaded, err := routedSystemValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, found, loadErr := endpoint.Settings.LoadBootstrapState(callCtx)
		return result{value: value, found: found}, loadErr
	})
	return loaded.value, loaded.found, err
}

func (s *Store) HasHistoricalData(ctx context.Context) (bool, error) {
	return routedSystemValue(ctx, s, func(callCtx context.Context, endpoint *Store) (bool, error) {
		return endpoint.Settings.HasHistoricalData(callCtx)
	})
}

func (s *Store) LoadModelPrices(ctx context.Context) (map[string]ModelPrice, error) {
	return routedSystemValue(ctx, s, func(callCtx context.Context, endpoint *Store) (map[string]ModelPrice, error) {
		return endpoint.ModelPrices.LoadAll(callCtx)
	})
}

func (s *Store) SaveModelPrices(ctx context.Context, prices map[string]ModelPrice) error {
	s.modelPricesMu.Lock()
	defer s.modelPricesMu.Unlock()
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.ModelPrices.ReplaceAll(callCtx, prices)
	})
}

func (s *Store) UpsertSyncedModelPrices(ctx context.Context, prices map[string]ModelPrice) (ModelPriceSyncResult, error) {
	s.modelPricesMu.Lock()
	defer s.modelPricesMu.Unlock()
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (ModelPriceSyncResult, error) {
		return endpoint.ModelPrices.UpsertSynced(callCtx, prices)
	})
}

// WithModelPriceSnapshot prevents model-price mutations while a service reads
// usage bands and applies the corresponding price book across multiple queries.
func (s *Store) WithModelPriceSnapshot(read func() error) error {
	s.modelPricesMu.RLock()
	defer s.modelPricesMu.RUnlock()
	return read()
}

func (s *Store) ModelUsageSummary(ctx context.Context, limit int) (ModelUsageSummary, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (ModelUsageSummary, error) {
		return endpoint.UsageEvents.ModelUsageSummary(callCtx, limit)
	})
}

func (s *Store) LoadAPIKeyAliases(ctx context.Context) ([]APIKeyAlias, error) {
	return routedSystemValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]APIKeyAlias, error) {
		return endpoint.APIKeyAliases.LoadAll(callCtx)
	})
}

func (s *Store) UpsertAPIKeyAliases(ctx context.Context, aliases []APIKeyAlias) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.APIKeyAliases.UpsertMany(callCtx, aliases, nil, false)
	})
}

func (s *Store) UpsertAPIKeyAliasesWithActiveHashes(ctx context.Context, aliases []APIKeyAlias, activeHashes []string, allowOrphanCleanup bool) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.APIKeyAliases.UpsertMany(callCtx, aliases, activeHashes, allowOrphanCleanup)
	})
}

func (s *Store) DeleteAPIKeyAlias(ctx context.Context, apiKeyHash string) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.APIKeyAliases.Delete(callCtx, apiKeyHash)
	})
}

func (s *Store) UpsertAccountActionCandidate(ctx context.Context, input AccountActionCandidateUpsert) (AccountActionCandidate, error) {
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (AccountActionCandidate, error) {
		return endpoint.AccountActions.Upsert(callCtx, input)
	})
}

func (s *Store) ListAccountActionCandidates(ctx context.Context, status string, limit int) ([]AccountActionCandidate, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]AccountActionCandidate, error) {
		return endpoint.AccountActions.List(callCtx, status, limit)
	})
}

func (s *Store) CountAccountActionCandidates(ctx context.Context, status string) (int64, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (int64, error) {
		return endpoint.AccountActions.Count(callCtx, status)
	})
}

func (s *Store) GetAccountActionCandidate(ctx context.Context, id int64) (AccountActionCandidate, bool, error) {
	type result struct {
		value AccountActionCandidate
		found bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, found, loadErr := endpoint.AccountActions.Get(callCtx, id)
		return result{value: value, found: found}, loadErr
	})
	return loaded.value, loaded.found, err
}

func (s *Store) UpdateAccountActionCandidateStatus(ctx context.Context, id int64, status string) (AccountActionCandidate, error) {
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (AccountActionCandidate, error) {
		return endpoint.AccountActions.UpdateStatus(callCtx, id, status)
	})
}

func (s *Store) UpdatePendingAccountActionCandidateStatus(ctx context.Context, id int64, status string) (AccountActionCandidate, error) {
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (AccountActionCandidate, error) {
		return endpoint.AccountActions.UpdatePendingStatus(callCtx, id, status)
	})
}

func (s *Store) RecordAccountActionCandidateFailure(ctx context.Context, id int64, reason string) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.AccountActions.RecordFailure(callCtx, id, reason)
	})
}

func (s *Store) MarkAccountActionCandidateAutoDisabled(ctx context.Context, id int64, disabledAtMS int64) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.AccountActions.MarkAutoDisabled(callCtx, id, disabledAtMS)
	})
}

func (s *Store) CreateCodexInspectionRun(ctx context.Context, run CodexInspectionRun) (CodexInspectionRun, error) {
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (CodexInspectionRun, error) {
		return endpoint.CodexInspections.CreateRun(callCtx, run)
	})
}

func (s *Store) UpdateCodexInspectionRun(ctx context.Context, run CodexInspectionRun) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.CodexInspections.UpdateRun(callCtx, run)
	})
}

func (s *Store) UpdateCodexInspectionRunProgress(ctx context.Context, run CodexInspectionRun, ownerID string) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.CodexInspections.UpdateRunProgress(callCtx, run, ownerID)
	})
}

func (s *Store) AcquireCodexInspectionRun(ctx context.Context, run CodexInspectionRun, ownerID string, leaseDuration time.Duration) (codexinspection.AcquireRunResult, error) {
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (codexinspection.AcquireRunResult, error) {
		return endpoint.CodexInspections.AcquireRun(callCtx, run, ownerID, leaseDuration)
	})
}

func (s *Store) HeartbeatCodexInspectionRun(ctx context.Context, runID int64, ownerID string, leaseDuration time.Duration) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.CodexInspections.HeartbeatRun(callCtx, runID, ownerID, leaseDuration)
	})
}

func (s *Store) MarkCodexInspectionRunCancelling(ctx context.Context, runID int64, ownerID string, reason string) (bool, error) {
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (bool, error) {
		return endpoint.CodexInspections.MarkRunCancelling(callCtx, runID, ownerID, reason)
	})
}

func (s *Store) FinalizeCodexInspectionRun(ctx context.Context, run CodexInspectionRun, ownerID string, finalLog *CodexInspectionLog) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.CodexInspections.FinalizeRun(callCtx, run, ownerID, finalLog)
	})
}

func (s *Store) ForceFinalizeCodexInspectionRun(ctx context.Context, run CodexInspectionRun, ownerID string, finalLog *CodexInspectionLog) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.CodexInspections.ForceFinalizeRun(callCtx, run, ownerID, finalLog)
	})
}

func (s *Store) GetActiveCodexInspectionLease(ctx context.Context, nowMS int64) (CodexInspectionLease, bool, error) {
	type result struct {
		value CodexInspectionLease
		found bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, found, loadErr := endpoint.CodexInspections.GetActiveLease(callCtx, nowMS)
		return result{value: value, found: found}, loadErr
	})
	return loaded.value, loaded.found, err
}

func (s *Store) RecoverStaleCodexInspectionRuns(ctx context.Context, nowMS int64, reason string) ([]CodexInspectionRun, error) {
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]CodexInspectionRun, error) {
		return endpoint.CodexInspections.RecoverStaleRuns(callCtx, nowMS, reason)
	})
}

func (s *Store) GetLatestCodexInspectionRunByTriggerType(ctx context.Context, triggerType string) (CodexInspectionRun, bool, error) {
	type result struct {
		value CodexInspectionRun
		found bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, found, loadErr := endpoint.CodexInspections.GetLatestRunByTriggerType(callCtx, triggerType)
		return result{value: value, found: found}, loadErr
	})
	return loaded.value, loaded.found, err
}

func (s *Store) InsertCodexInspectionResult(ctx context.Context, result CodexInspectionResult) (CodexInspectionResult, error) {
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (CodexInspectionResult, error) {
		return endpoint.CodexInspections.InsertResult(callCtx, result)
	})
}

func (s *Store) InsertCodexInspectionLog(ctx context.Context, entry CodexInspectionLog) (CodexInspectionLog, error) {
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (CodexInspectionLog, error) {
		return endpoint.CodexInspections.InsertLog(callCtx, entry)
	})
}

func (s *Store) ListCodexInspectionRuns(ctx context.Context, limit int) ([]CodexInspectionRun, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]CodexInspectionRun, error) {
		return endpoint.CodexInspections.ListRuns(callCtx, limit)
	})
}

func (s *Store) GetCodexInspectionRun(ctx context.Context, id int64) (CodexInspectionRun, bool, error) {
	type result struct {
		value CodexInspectionRun
		found bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, found, loadErr := endpoint.CodexInspections.GetRun(callCtx, id)
		return result{value: value, found: found}, loadErr
	})
	return loaded.value, loaded.found, err
}

func (s *Store) GetLatestCodexInspectionRunByTrigger(ctx context.Context, triggerType, triggerKey string) (CodexInspectionRun, bool, error) {
	type result struct {
		value CodexInspectionRun
		found bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, found, loadErr := endpoint.CodexInspections.GetLatestRunByTrigger(callCtx, triggerType, triggerKey)
		return result{value: value, found: found}, loadErr
	})
	return loaded.value, loaded.found, err
}

func (s *Store) ListCodexInspectionResults(ctx context.Context, runID int64) ([]CodexInspectionResult, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]CodexInspectionResult, error) {
		return endpoint.CodexInspections.ListResults(callCtx, runID)
	})
}

func (s *Store) ListCodexInspectionLogs(ctx context.Context, runID int64) ([]CodexInspectionLog, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]CodexInspectionLog, error) {
		return endpoint.CodexInspections.ListLogs(callCtx, runID)
	})
}

func (s *Store) ListCodexInspectionDisableOwnership(ctx context.Context) ([]CodexInspectionDisableOwnership, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]CodexInspectionDisableOwnership, error) {
		return endpoint.CodexInspections.ListDisableOwnership(callCtx)
	})
}

func (s *Store) UpsertCodexInspectionDisableOwnership(ctx context.Context, item CodexInspectionDisableOwnership) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.CodexInspections.UpsertDisableOwnership(callCtx, item)
	})
}

func (s *Store) UpsertCodexInspectionDisableOwnerships(ctx context.Context, items []CodexInspectionDisableOwnership) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.CodexInspections.UpsertDisableOwnerships(callCtx, items)
	})
}

func (s *Store) DeleteCodexInspectionDisableOwnership(ctx context.Context, target model.CodexInspectionDisableOwnershipTarget) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.CodexInspections.DeleteDisableOwnership(callCtx, target)
	})
}

func (s *Store) RevokeCodexInspectionDisableOwnership(ctx context.Context, targets []model.CodexInspectionDisableOwnershipTarget, clearAll bool) ([]CodexInspectionDisableOwnership, error) {
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]CodexInspectionDisableOwnership, error) {
		return endpoint.CodexInspections.RevokeDisableOwnership(callCtx, targets, clearAll)
	})
}

func (s *Store) RestoreCodexInspectionDisableOwnership(ctx context.Context, items []CodexInspectionDisableOwnership) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.CodexInspections.RestoreDisableOwnership(callCtx, items)
	})
}

func (s *Store) InsertEvents(ctx context.Context, events []usage.Event) (InsertResult, error) {
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (InsertResult, error) {
		return endpoint.UsageEvents.InsertBatch(callCtx, events)
	})
}

func (s *Store) ExistingUsageEventHashes(ctx context.Context, hashes []string) (map[string]struct{}, error) {
	return s.UsageEvents.ExistingEventHashes(ctx, hashes)
}

func (s *Store) UsageCacheAccountingMigrationState(ctx context.Context) (DataMigrationState, error) {
	type result struct {
		state DataMigrationState
		found bool
	}
	loaded, err := routedSQLiteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		state, found, loadErr := endpoint.DataMigrations.UsageCacheAccountingState(callCtx)
		return result{state: state, found: found}, loadErr
	})
	if err != nil {
		return DataMigrationState{}, err
	}
	if loaded.found {
		return loaded.state, nil
	}
	return DataMigrationState{
		Name:   datamigration.UsageCacheAccountingMigrationName,
		Status: datamigration.StatusDiscovering,
	}, nil
}

func (s *Store) DiscoverUsageCacheAccounting(ctx context.Context) (DataMigrationState, error) {
	return routedSQLiteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (DataMigrationState, error) {
		return endpoint.DataMigrations.DiscoverUsageCacheAccounting(callCtx)
	})
}

func (s *Store) RunUsageCacheAccountingBatch(ctx context.Context, batchSize int) (DataMigrationBatchResult, error) {
	return routedSQLiteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (DataMigrationBatchResult, error) {
		return endpoint.DataMigrations.RunUsageCacheAccountingBatch(callCtx, batchSize)
	})
}

func (s *Store) RecordUsageCacheAccountingFailure(ctx context.Context, migrationErr error) error {
	_, err := routedSQLiteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (struct{}, error) {
		return struct{}{}, endpoint.DataMigrations.RecordUsageCacheAccountingFailure(callCtx, migrationErr)
	})
	return err
}

func (s *Store) UsageCacheAccountingMigrationReady(ctx context.Context) (bool, error) {
	state, err := s.UsageCacheAccountingMigrationState(ctx)
	if err != nil {
		return false, err
	}
	return state.Status == datamigration.StatusCompleted, nil
}

func (s *Store) CatchUpUsageHourlyAggregate(ctx context.Context, limit int, nowMS int64) (UsageHourlyAggregateCatchUpResult, error) {
	ready, err := s.UsageCacheAccountingMigrationReady(ctx)
	if err != nil {
		return UsageHourlyAggregateCatchUpResult{}, err
	}
	if !ready {
		return UsageHourlyAggregateCatchUpResult{Pending: true}, nil
	}
	return routedDerivedValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsageHourlyAggregateCatchUpResult, error) {
		return endpoint.UsageAggregates.CatchUp(callCtx, limit, nowMS)
	}, func(current *UsageHourlyAggregateCatchUpResult, other UsageHourlyAggregateCatchUpResult) {
		current.Processed += other.Processed
		current.LastEventID = max(current.LastEventID, other.LastEventID)
		current.CoverageEventID = max(current.CoverageEventID, other.CoverageEventID)
		current.TargetEventID = max(current.TargetEventID, other.TargetEventID)
		current.Pending = current.Pending || other.Pending
		current.Rebuilt = current.Rebuilt || other.Rebuilt
	})
}

func (s *Store) RecordUsageHourlyAggregateFailure(ctx context.Context, aggregateErr error, nowMS int64) error {
	_, err := routedDerivedValue(ctx, s, func(callCtx context.Context, endpoint *Store) (struct{}, error) {
		return struct{}{}, endpoint.UsageAggregates.RecordFailure(callCtx, aggregateErr, nowMS)
	}, nil)
	return err
}

func (s *Store) UsageHourlyAggregateState(ctx context.Context) (UsageHourlyAggregateState, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsageHourlyAggregateState, error) {
		return endpoint.UsageAggregates.State(callCtx)
	})
}

func (s *Store) UsageHourlyAggregateRows(ctx context.Context, filter UsageHourlyAggregateFilter) ([]UsageHourlyAggregateRow, UsageHourlyAggregateState, bool, error) {
	type result struct {
		rows      []UsageHourlyAggregateRow
		state     UsageHourlyAggregateState
		available bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		rows, state, available, loadErr := endpoint.UsageAggregates.LoadRows(callCtx, filter)
		return result{rows: rows, state: state, available: available}, loadErr
	})
	return loaded.rows, loaded.state, loaded.available, err
}

func (s *Store) CatchUpUsagePricing(ctx context.Context, limit int, nowMS int64) (UsagePricingCatchUpResult, error) {
	ready, err := s.UsageCacheAccountingMigrationReady(ctx)
	if err != nil {
		return UsagePricingCatchUpResult{}, err
	}
	if !ready {
		return UsagePricingCatchUpResult{Pending: true}, nil
	}
	return routedDerivedValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsagePricingCatchUpResult, error) {
		return endpoint.UsagePricing.CatchUp(callCtx, limit, nowMS)
	}, func(current *UsagePricingCatchUpResult, other UsagePricingCatchUpResult) {
		current.Processed += other.Processed
		current.LastEventID = max(current.LastEventID, other.LastEventID)
		current.CoverageEventID = max(current.CoverageEventID, other.CoverageEventID)
		current.TargetEventID = max(current.TargetEventID, other.TargetEventID)
		current.Pending = current.Pending || other.Pending
		current.Rebuilt = current.Rebuilt || other.Rebuilt
		current.ContinueSoon = current.ContinueSoon || other.ContinueSoon
	})
}

func (s *Store) RecordUsagePricingFailure(ctx context.Context, rollupErr error, nowMS int64) error {
	_, err := routedDerivedValue(ctx, s, func(callCtx context.Context, endpoint *Store) (struct{}, error) {
		return struct{}{}, endpoint.UsagePricing.RecordFailure(callCtx, rollupErr, nowMS)
	}, nil)
	return err
}

func (s *Store) CatchUpUsageMonitoringStats(ctx context.Context, limit int, nowMS int64) (UsageMonitoringCatchUpResult, error) {
	ready, err := s.UsageCacheAccountingMigrationReady(ctx)
	if err != nil {
		return UsageMonitoringCatchUpResult{}, err
	}
	if !ready {
		return UsageMonitoringCatchUpResult{Pending: true}, nil
	}
	return routedDerivedValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsageMonitoringCatchUpResult, error) {
		return endpoint.UsageMonitoring.CatchUpStats(callCtx, limit, nowMS)
	}, mergeUsageMonitoringCatchUp)
}

func (s *Store) CatchUpUsageMonitoringProjection(ctx context.Context, limit int, nowMS int64) (UsageMonitoringCatchUpResult, error) {
	ready, err := s.UsageCacheAccountingMigrationReady(ctx)
	if err != nil {
		return UsageMonitoringCatchUpResult{}, err
	}
	if !ready {
		return UsageMonitoringCatchUpResult{Pending: true}, nil
	}
	return routedDerivedValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsageMonitoringCatchUpResult, error) {
		return endpoint.UsageMonitoring.CatchUpProjection(callCtx, limit, nowMS)
	}, mergeUsageMonitoringCatchUp)
}

func (s *Store) CatchUpUsageMonitoringMetadata(ctx context.Context, limit int, nowMS int64) (UsageMonitoringCatchUpResult, error) {
	return routedDerivedValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsageMonitoringCatchUpResult, error) {
		return endpoint.UsageMonitoring.CatchUpMetadata(callCtx, limit, nowMS)
	}, mergeUsageMonitoringCatchUp)
}

func (s *Store) CatchUpCodexLegacyIdentityEvidence(ctx context.Context, limit int, nowMS int64) (UsageMonitoringCatchUpResult, error) {
	return routedDerivedValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsageMonitoringCatchUpResult, error) {
		return endpoint.UsageMonitoring.CatchUpCodexLegacyIdentityEvidence(callCtx, limit, nowMS)
	}, mergeUsageMonitoringCatchUp)
}

func (s *Store) RecordUsageMonitoringFailure(ctx context.Context, rollupName string, rollupErr error, nowMS int64) error {
	_, err := routedDerivedValue(ctx, s, func(callCtx context.Context, endpoint *Store) (struct{}, error) {
		return struct{}{}, endpoint.UsageMonitoring.RecordFailure(callCtx, rollupName, rollupErr, nowMS)
	}, nil)
	return err
}

func (s *Store) UsageMonitoringState(ctx context.Context, rollupName string) (UsageMonitoringState, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsageMonitoringState, error) {
		return endpoint.UsageMonitoring.State(callCtx, rollupName)
	})
}

func (s *Store) UsageMonitoringAggregate(ctx context.Context, filter AnalyticsFilter) (Aggregate, UsageMonitoringState, bool, error) {
	type result struct {
		value     Aggregate
		state     UsageMonitoringState
		available bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, state, available, loadErr := endpoint.UsageMonitoring.LoadAggregate(callCtx, filter)
		return result{value: value, state: state, available: available}, loadErr
	})
	return loaded.value, loaded.state, loaded.available, err
}

func (s *Store) UsageMonitoringModelStats(ctx context.Context, filter AnalyticsFilter) ([]ModelStat, UsageMonitoringState, bool, error) {
	type result struct {
		value     []ModelStat
		state     UsageMonitoringState
		available bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, state, available, loadErr := endpoint.UsageMonitoring.LoadModelStats(callCtx, filter)
		return result{value: value, state: state, available: available}, loadErr
	})
	return loaded.value, loaded.state, loaded.available, err
}

func (s *Store) UsageMonitoringAccountStats(ctx context.Context, filter AnalyticsFilter) ([]AccountModelStat, UsageMonitoringState, bool, error) {
	type result struct {
		value     []AccountModelStat
		state     UsageMonitoringState
		available bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, state, available, loadErr := endpoint.UsageMonitoring.LoadAccountStats(callCtx, filter)
		return result{value: value, state: state, available: available}, loadErr
	})
	return loaded.value, loaded.state, loaded.available, err
}

func (s *Store) UsageMonitoringAccountWindowStats(ctx context.Context, windows []AccountWindowUsageQuery) ([]AccountWindowModelStat, UsageMonitoringState, bool, error) {
	type result struct {
		value     []AccountWindowModelStat
		state     UsageMonitoringState
		available bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, state, available, loadErr := endpoint.UsageMonitoring.LoadAccountWindowStats(callCtx, windows)
		return result{value: value, state: state, available: available}, loadErr
	})
	return loaded.value, loaded.state, loaded.available, err
}

func (s *Store) UsageMonitoringAPIKeyStats(ctx context.Context, filter AnalyticsFilter) ([]APIKeyModelStat, UsageMonitoringState, bool, error) {
	type result struct {
		value     []APIKeyModelStat
		state     UsageMonitoringState
		available bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, state, available, loadErr := endpoint.UsageMonitoring.LoadAPIKeyStats(callCtx, filter)
		return result{value: value, state: state, available: available}, loadErr
	})
	return loaded.value, loaded.state, loaded.available, err
}

func (s *Store) UsageMonitoringFilterOptions(ctx context.Context, filter AnalyticsFilter) (FilterOptionValues, UsageMonitoringState, bool, error) {
	type result struct {
		value     FilterOptionValues
		state     UsageMonitoringState
		available bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, state, available, loadErr := endpoint.UsageMonitoring.LoadFilterOptions(callCtx, filter)
		return result{value: value, state: state, available: available}, loadErr
	})
	return loaded.value, loaded.state, loaded.available, err
}

func (s *Store) UsageMonitoringFilterSelectors(ctx context.Context, filter AnalyticsFilter) (FilterSelectorValues, UsageMonitoringState, bool, error) {
	type result struct {
		value     FilterSelectorValues
		state     UsageMonitoringState
		available bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, state, available, loadErr := endpoint.UsageMonitoring.LoadFilterSelectors(callCtx, filter)
		return result{value: value, state: state, available: available}, loadErr
	})
	return loaded.value, loaded.state, loaded.available, err
}

func (s *Store) UsageMonitoringEventsCount(ctx context.Context, filter AnalyticsFilter) (int64, UsageMonitoringState, bool, error) {
	type result struct {
		value     int64
		state     UsageMonitoringState
		available bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, state, available, loadErr := endpoint.UsageMonitoring.LoadEventsCount(callCtx, filter)
		return result{value: value, state: state, available: available}, loadErr
	})
	return loaded.value, loaded.state, loaded.available, err
}

func (s *Store) UsageMonitoringEventsPage(ctx context.Context, filter AnalyticsFilter, beforeMS, beforeID int64, limit int) (EventsPage, UsageMonitoringState, bool, error) {
	type result struct {
		value     EventsPage
		state     UsageMonitoringState
		available bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, state, available, loadErr := endpoint.UsageMonitoring.LoadEventsPage(callCtx, filter, beforeMS, beforeID, limit)
		return result{value: value, state: state, available: available}, loadErr
	})
	return loaded.value, loaded.state, loaded.available, err
}

func (s *Store) UsageMonitoringHeaderSnapshots(ctx context.Context, sinceMS int64, limit int) ([]HeaderSnapshot, UsageMonitoringState, bool, error) {
	type result struct {
		value     []HeaderSnapshot
		state     UsageMonitoringState
		available bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		value, state, available, loadErr := endpoint.UsageMonitoring.LoadHeaderSnapshots(callCtx, sinceMS, limit)
		return result{value: value, state: state, available: available}, loadErr
	})
	return loaded.value, loaded.state, loaded.available, err
}

func (s *Store) UsagePricingState(ctx context.Context) (UsagePricingState, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsagePricingState, error) {
		return endpoint.UsagePricing.State(callCtx)
	})
}

func (s *Store) UsagePricingHourlyRows(ctx context.Context, filter UsagePricingHourlyFilter) ([]UsagePricingHourlyRow, UsagePricingState, bool, error) {
	type result struct {
		rows      []UsagePricingHourlyRow
		state     UsagePricingState
		available bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		rows, state, available, loadErr := endpoint.UsagePricing.LoadHourlyRows(callCtx, filter)
		return result{rows: rows, state: state, available: available}, loadErr
	})
	return loaded.rows, loaded.state, loaded.available, err
}

func (s *Store) LoadUsageHourlyPricingSnapshot(
	ctx context.Context,
	aggregateFilter UsageHourlyAggregateFilter,
	pricingFilter UsagePricingHourlyFilter,
) (UsageHourlyPricingSnapshot, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsageHourlyPricingSnapshot, error) {
		return endpoint.loadUsageHourlyPricingSnapshot(callCtx, aggregateFilter, pricingFilter)
	})
}

func (s *Store) loadUsageHourlyPricingSnapshot(
	ctx context.Context,
	aggregateFilter UsageHourlyAggregateFilter,
	pricingFilter UsagePricingHourlyFilter,
) (UsageHourlyPricingSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return UsageHourlyPricingSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()

	aggregateRows, aggregateState, aggregateAvailable, err := s.UsageAggregates.LoadRowsTx(ctx, tx, aggregateFilter)
	if err != nil {
		return UsageHourlyPricingSnapshot{}, err
	}
	pricingRows, pricingState, pricingAvailable, err := s.UsagePricing.LoadHourlyRowsTx(ctx, tx, pricingFilter)
	if err != nil {
		return UsageHourlyPricingSnapshot{}, err
	}
	prices, err := s.ModelPrices.LoadAllTx(ctx, tx)
	if err != nil {
		return UsageHourlyPricingSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return UsageHourlyPricingSnapshot{}, err
	}
	return UsageHourlyPricingSnapshot{
		AggregateRows:      aggregateRows,
		AggregateState:     aggregateState,
		AggregateAvailable: aggregateAvailable,
		PricingRows:        pricingRows,
		PricingState:       pricingState,
		PricingAvailable:   pricingAvailable,
		Prices:             prices,
	}, nil
}

func (s *Store) UsagePricingAccountRows(ctx context.Context, accountKeys []string) ([]UsagePricingAccountRow, UsagePricingState, bool, error) {
	type result struct {
		rows      []UsagePricingAccountRow
		state     UsagePricingState
		available bool
	}
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		rows, state, available, loadErr := endpoint.UsagePricing.LoadAccountRows(callCtx, accountKeys)
		return result{rows: rows, state: state, available: available}, loadErr
	})
	return loaded.rows, loaded.state, loaded.available, err
}

func (s *Store) LoadUsagePricingAccountSnapshot(ctx context.Context, accountKeys []string) (UsagePricingAccountSnapshot, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsagePricingAccountSnapshot, error) {
		return endpoint.loadUsagePricingAccountSnapshot(callCtx, accountKeys)
	})
}

func (s *Store) loadUsagePricingAccountSnapshot(ctx context.Context, accountKeys []string) (UsagePricingAccountSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return UsagePricingAccountSnapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, state, available, err := s.UsagePricing.LoadAccountRowsTx(ctx, tx, accountKeys)
	if err != nil {
		return UsagePricingAccountSnapshot{}, err
	}
	prices, err := s.ModelPrices.LoadAllTx(ctx, tx)
	if err != nil {
		return UsagePricingAccountSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return UsagePricingAccountSnapshot{}, err
	}
	return UsagePricingAccountSnapshot{
		Rows:      rows,
		State:     state,
		Available: available,
		Prices:    prices,
	}, nil
}

func (s *Store) CatchUpAccountHistoryRollups(ctx context.Context, limit int, nowMS int64) (UsageRollupCatchUpResult, error) {
	ready, err := s.UsageCacheAccountingMigrationReady(ctx)
	if err != nil {
		return UsageRollupCatchUpResult{}, err
	}
	if !ready {
		return UsageRollupCatchUpResult{Pending: true}, nil
	}
	return routedDerivedValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsageRollupCatchUpResult, error) {
		return endpoint.UsageRollups.CatchUpAccountHistory(callCtx, limit, nowMS)
	}, mergeUsageRollupCatchUp)
}

func (s *Store) CatchUpDashboardHourlyRollups(ctx context.Context, limit int, nowMS int64) (UsageRollupCatchUpResult, error) {
	ready, err := s.UsageCacheAccountingMigrationReady(ctx)
	if err != nil {
		return UsageRollupCatchUpResult{}, err
	}
	if !ready {
		return UsageRollupCatchUpResult{Pending: true}, nil
	}
	return routedDerivedValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsageRollupCatchUpResult, error) {
		return endpoint.UsageRollups.CatchUpDashboardHourly(callCtx, limit, nowMS)
	}, mergeUsageRollupCatchUp)
}

func (s *Store) AccountHistoryRollupCheckpoint(ctx context.Context) (UsageRollupCheckpoint, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsageRollupCheckpoint, error) {
		return endpoint.UsageRollups.Checkpoint(callCtx, usagerollup.AccountHistoryCheckpointName)
	})
}

func (s *Store) DashboardHourlyRollupCheckpoint(ctx context.Context) (UsageRollupCheckpoint, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (UsageRollupCheckpoint, error) {
		return endpoint.UsageRollups.Checkpoint(callCtx, usagerollup.DashboardHourlyCheckpointName)
	})
}

func (s *Store) LatestUsageEventID(ctx context.Context) (int64, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (int64, error) {
		return endpoint.UsageRollups.LatestEventID(callCtx)
	})
}

func (s *Store) AccountHistoryRollupRows(ctx context.Context, accountKeys []string) ([]AccountHistoryRollupRow, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]AccountHistoryRollupRow, error) {
		return endpoint.UsageRollups.AccountHistoryRows(callCtx, accountKeys)
	})
}

func (s *Store) RecentAccountRequests(ctx context.Context, targets []LatestAccountRequestQuery, limit int) ([]LatestAccountRequest, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]LatestAccountRequest, error) {
		return endpoint.UsageEvents.RecentAccountRequests(callCtx, targets, limit)
	})
}

func (s *Store) DashboardHourlyRollupRows(ctx context.Context, fromMS, toMS int64) ([]DashboardHourlyRollupRow, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]DashboardHourlyRollupRow, error) {
		return endpoint.UsageRollups.DashboardHourlyRows(callCtx, fromMS, toMS)
	})
}

func (s *Store) DashboardHourlyRollupModelRows(ctx context.Context, fromMS, toMS int64) ([]DashboardHourlyRollupRow, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]DashboardHourlyRollupRow, error) {
		return endpoint.UsageRollups.DashboardHourlyModelRows(callCtx, fromMS, toMS)
	})
}

func (s *Store) DashboardDailyRollupRows(ctx context.Context, fromMS, toMS int64) ([]DashboardHourlyRollupRow, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]DashboardHourlyRollupRow, error) {
		return endpoint.UsageRollups.DashboardDailyRows(callCtx, fromMS, toMS)
	})
}

func AccountHistoryKey(accountSnapshot, authLabelSnapshot, source, authIndex string) string {
	return usagerollup.AccountKey(accountSnapshot, authLabelSnapshot, source, authIndex)
}

func mergeUsageMonitoringCatchUp(current *UsageMonitoringCatchUpResult, other UsageMonitoringCatchUpResult) {
	current.Processed += other.Processed
	current.LastEventID = max(current.LastEventID, other.LastEventID)
	current.CoverageEventID = max(current.CoverageEventID, other.CoverageEventID)
	current.TargetEventID = max(current.TargetEventID, other.TargetEventID)
	current.Pending = current.Pending || other.Pending
	current.Rebuilt = current.Rebuilt || other.Rebuilt
	current.ContinueSoon = current.ContinueSoon || other.ContinueSoon
}

func mergeUsageRollupCatchUp(current *UsageRollupCatchUpResult, other UsageRollupCatchUpResult) {
	current.Processed += other.Processed
	current.LastEventID = max(current.LastEventID, other.LastEventID)
	current.Pending = current.Pending || other.Pending
	current.Rebuilt = current.Rebuilt || other.Rebuilt
	current.RebuildTargetEventID = max(current.RebuildTargetEventID, other.RebuildTargetEventID)
}

func (s *Store) UpsertQuotaCooldown(ctx context.Context, cooldown QuotaCooldownUpsert) (QuotaCooldown, error) {
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (QuotaCooldown, error) {
		return endpoint.QuotaCooldowns.UpsertActive(callCtx, cooldown)
	})
}

func (s *Store) ListDueQuotaCooldowns(ctx context.Context, nowMS int64, limit int) ([]QuotaCooldown, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]QuotaCooldown, error) {
		return endpoint.QuotaCooldowns.ListDue(callCtx, nowMS, limit)
	})
}

func (s *Store) ListActiveQuotaCooldowns(ctx context.Context) ([]QuotaCooldown, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]QuotaCooldown, error) {
		return endpoint.QuotaCooldowns.ListActive(callCtx)
	})
}

func (s *Store) MarkQuotaCooldownRecovered(ctx context.Context, id int64, recoveredAtMS int64) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.QuotaCooldowns.MarkRecovered(callCtx, id, recoveredAtMS)
	})
}

func (s *Store) MarkQuotaCooldownSkipped(ctx context.Context, id int64, reason string) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.QuotaCooldowns.MarkSkipped(callCtx, id, reason)
	})
}

func (s *Store) RecordQuotaCooldownFailure(ctx context.Context, id int64, reason string) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.QuotaCooldowns.RecordFailure(callCtx, id, reason)
	})
}

func (s *Store) InsertQuotaObservationWrites(ctx context.Context, writes []AccountQuotaObservationWrite) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.QuotaSnapshots.InsertObservationWrites(callCtx, writes)
	})
}

func (s *Store) ListQuotaSnapshotCandidates(ctx context.Context, accountKey, provider string, limit int) ([]AccountQuotaSnapshot, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]AccountQuotaSnapshot, error) {
		return endpoint.QuotaSnapshots.ListCandidates(callCtx, accountKey, provider, limit)
	})
}

func (s *Store) ListQuotaWindowStates(ctx context.Context, accountKey, provider string) ([]AccountQuotaWindowState, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]AccountQuotaWindowState, error) {
		return endpoint.QuotaSnapshots.ListWindowStates(callCtx, accountKey, provider)
	})
}

func (s *Store) AddDeadLetter(ctx context.Context, payload string, parseErr error) error {
	return routedWrite(ctx, s, func(callCtx context.Context, endpoint *Store) error {
		return endpoint.DeadLetters.Insert(callCtx, payload, parseErr.Error())
	})
}

func (s *Store) RecentEvents(ctx context.Context, limit int) ([]usage.Event, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]usage.Event, error) {
		return endpoint.UsageEvents.ListRecent(callCtx, limit)
	})
}

func (s *Store) BackfillUsageResponseMetadata(ctx context.Context, batchLimit int) (int, error) {
	return routedWriteValue(ctx, s, func(callCtx context.Context, endpoint *Store) (int, error) {
		return endpoint.UsageEvents.BackfillResponseMetadata(callCtx, batchLimit)
	})
}

func (s *Store) Counts(ctx context.Context) (events int64, deadLetters int64, err error) {
	type result struct{ events, deadLetters int64 }
	loaded, err := routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (result, error) {
		events, countErr := endpoint.UsageEvents.Count(callCtx)
		if countErr != nil {
			return result{}, countErr
		}
		deadLetters, countErr := endpoint.DeadLetters.Count(callCtx)
		return result{events: events, deadLetters: deadLetters}, countErr
	})
	return loaded.events, loaded.deadLetters, err
}

func (s *Store) ExportJSONL(ctx context.Context) ([]byte, error) {
	var output bytes.Buffer
	if err := s.WriteExportJSONL(ctx, &output, 0); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (s *Store) WriteCompatibleUsage(ctx context.Context, writer io.Writer, limit int) error {
	return routedBusinessStream(ctx, s, writer, func(callCtx context.Context, endpoint *Store, output io.Writer) error {
		return endpoint.UsageEvents.WriteCompatibleUsage(callCtx, output, limit)
	})
}

func (s *Store) WriteExportJSONL(ctx context.Context, writer io.Writer, limit int) error {
	return routedBusinessStream(ctx, s, writer, func(callCtx context.Context, endpoint *Store, output io.Writer) error {
		return endpoint.UsageEvents.WriteExportJSONL(callCtx, output, limit)
	})
}

// AggregateBetween computes summary metrics over [fromMs, toMs).
func (s *Store) AggregateBetween(ctx context.Context, fromMs, toMs int64) (Aggregate, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (Aggregate, error) {
		return endpoint.UsageEvents.AggregateBetween(callCtx, fromMs, toMs)
	})
}

// TopModelsBetween returns the most active models ordered by call count.
func (s *Store) TopModelsBetween(ctx context.Context, fromMs, toMs int64, limit int) ([]ModelStat, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]ModelStat, error) {
		return endpoint.UsageEvents.TopModelsBetween(callCtx, fromMs, toMs, limit)
	})
}

// ModelStatsBetween returns per-model totals for all models in a window.
func (s *Store) ModelStatsBetween(ctx context.Context, fromMs, toMs int64) ([]ModelStat, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]ModelStat, error) {
		return endpoint.UsageEvents.ModelStatsBetween(callCtx, fromMs, toMs)
	})
}

// RecentFailuresBetween returns the most recent failed events in window.
func (s *Store) RecentFailuresBetween(ctx context.Context, fromMs, toMs int64, limit int) ([]RecentFailure, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]RecentFailure, error) {
		return endpoint.UsageEvents.RecentFailuresBetween(callCtx, fromMs, toMs, limit)
	})
}

func (s *Store) HourlyTimelineBetween(ctx context.Context, fromMs, toMs int64) ([]TimelinePoint, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]TimelinePoint, error) {
		return endpoint.UsageEvents.HourlyTimelineBetween(callCtx, fromMs, toMs)
	})
}

func (s *Store) BucketTimelineBetween(ctx context.Context, fromMs, toMs int64, bucketMs int64) ([]TimelinePoint, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]TimelinePoint, error) {
		return endpoint.UsageEvents.BucketTimelineBetween(callCtx, fromMs, toMs, bucketMs)
	})
}

func (s *Store) AggregateWithFilter(ctx context.Context, filter AnalyticsFilter) (Aggregate, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (Aggregate, error) {
		return endpoint.UsageEvents.AggregateWithFilter(callCtx, filter)
	})
}

func (s *Store) ModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter, limit int) ([]ModelStat, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]ModelStat, error) {
		return endpoint.UsageEvents.ModelStatsWithFilter(callCtx, filter, limit)
	})
}

func (s *Store) TimelineWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]TimelinePoint, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]TimelinePoint, error) {
		return endpoint.UsageEvents.TimelineWithFilter(callCtx, filter, granularity, location)
	})
}

func (s *Store) LatencyPercentilesWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]LatencyPercentiles, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]LatencyPercentiles, error) {
		return endpoint.UsageEvents.LatencyPercentilesWithFilter(callCtx, filter, granularity, location)
	})
}

func (s *Store) LatencySummaryWithFilter(ctx context.Context, filter AnalyticsFilter) (LatencySummary, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (LatencySummary, error) {
		return endpoint.UsageEvents.LatencySummaryWithFilter(callCtx, filter)
	})
}

func (s *Store) HourlyDistributionWithFilter(ctx context.Context, filter AnalyticsFilter, location *time.Location) ([]HourlyPoint, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]HourlyPoint, error) {
		return endpoint.UsageEvents.HourlyDistributionWithFilter(callCtx, filter, location)
	})
}

func (s *Store) FilterOptionValuesWithFilter(ctx context.Context, filter AnalyticsFilter) (FilterOptionValues, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (FilterOptionValues, error) {
		return endpoint.UsageEvents.FilterOptionValuesWithFilter(callCtx, filter)
	})
}

func (s *Store) FilterSelectorValuesWithFilter(ctx context.Context, filter AnalyticsFilter) (FilterSelectorValues, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (FilterSelectorValues, error) {
		return endpoint.UsageEvents.FilterSelectorValuesWithFilter(callCtx, filter)
	})
}

func (s *Store) HeatmapWithFilter(ctx context.Context, filter AnalyticsFilter, location *time.Location) ([]HeatmapPoint, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]HeatmapPoint, error) {
		return endpoint.UsageEvents.HeatmapWithFilter(callCtx, filter, location)
	})
}

func (s *Store) ChannelModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]ChannelModelStat, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]ChannelModelStat, error) {
		return endpoint.UsageEvents.ChannelModelStatsWithFilter(callCtx, filter)
	})
}

func (s *Store) FailureSourcesWithFilter(ctx context.Context, filter AnalyticsFilter) ([]FailureSourceStat, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]FailureSourceStat, error) {
		return endpoint.UsageEvents.FailureSourcesWithFilter(callCtx, filter)
	})
}

func (s *Store) AccountModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]AccountModelStat, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]AccountModelStat, error) {
		return endpoint.UsageEvents.AccountModelStatsWithFilter(callCtx, filter)
	})
}

func (s *Store) AccountWindowModelStats(ctx context.Context, windows []AccountWindowUsageQuery) ([]AccountWindowModelStat, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]AccountWindowModelStat, error) {
		return endpoint.UsageEvents.AccountWindowModelStats(callCtx, windows)
	})
}

func (s *Store) CredentialModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]CredentialModelStat, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]CredentialModelStat, error) {
		return endpoint.UsageEvents.CredentialModelStatsWithFilter(callCtx, filter)
	})
}

func (s *Store) CredentialTimelineWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]CredentialTimelinePoint, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]CredentialTimelinePoint, error) {
		return endpoint.UsageEvents.CredentialTimelineWithFilter(callCtx, filter, granularity, location)
	})
}

func (s *Store) APIKeyTimelineWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]APIKeyTimelinePoint, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]APIKeyTimelinePoint, error) {
		return endpoint.UsageEvents.APIKeyTimelineWithFilter(callCtx, filter, granularity, location)
	})
}

func (s *Store) APIKeyModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]APIKeyModelStat, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]APIKeyModelStat, error) {
		return endpoint.UsageEvents.APIKeyModelStatsWithFilter(callCtx, filter)
	})
}

func (s *Store) TaskBucketsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]TaskBucket, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]TaskBucket, error) {
		return endpoint.UsageEvents.TaskBucketsWithFilter(callCtx, filter)
	})
}

func (s *Store) RecentFailuresWithFilter(ctx context.Context, filter AnalyticsFilter, limit int) ([]RecentFailure, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]RecentFailure, error) {
		return endpoint.UsageEvents.RecentFailuresWithFilter(callCtx, filter, limit)
	})
}

func (s *Store) EventsPageWithFilter(ctx context.Context, filter AnalyticsFilter, beforeMS int64, beforeID int64, limit int) (EventsPage, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (EventsPage, error) {
		return endpoint.UsageEvents.EventsPageWithFilter(callCtx, filter, beforeMS, beforeID, limit)
	})
}

func (s *Store) EventsCountWithFilter(ctx context.Context, filter AnalyticsFilter) (int64, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (int64, error) {
		return endpoint.UsageEvents.EventsCountWithFilter(callCtx, filter)
	})
}

func (s *Store) LatestHeaderSnapshots(ctx context.Context, sinceMS int64, limit int) ([]HeaderSnapshot, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]HeaderSnapshot, error) {
		return endpoint.UsageEvents.LatestHeaderSnapshots(callCtx, sinceMS, limit)
	})
}

func (s *Store) ActiveDaysWithFilter(ctx context.Context, filter AnalyticsFilter, location *time.Location) (int64, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) (int64, error) {
		return endpoint.UsageEvents.ActiveDaysWithFilter(callCtx, filter, location)
	})
}

func (s *Store) ZeroTokenModelsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]string, error) {
	return routedBusinessValue(ctx, s, func(callCtx context.Context, endpoint *Store) ([]string, error) {
		return endpoint.UsageEvents.ZeroTokenModelsWithFilter(callCtx, filter)
	})
}
