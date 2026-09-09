package databasemanagement

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
)

var (
	ErrUnavailable        = errors.New("database management is unavailable")
	ErrGenerationConflict = errors.New("database routing generation conflict")
	ErrInvalidRequest     = errors.New("invalid database management request")
	ErrUnsafeOperation    = errors.New("database operation confirmation is required")
	ErrSQLiteSourceUnsafe = errors.New("SQLite source switch requires a stable SQLite-only topology")
)

type MetricValue struct {
	Value            float64 `json:"value,omitempty"`
	Available        bool    `json:"available"`
	PermissionDenied bool    `json:"permissionDenied,omitempty"`
	Error            string  `json:"error,omitempty"`
}

type MySQLPoolStatus struct {
	Open           int   `json:"open"`
	InUse          int   `json:"inUse"`
	Idle           int   `json:"idle"`
	WaitCount      int64 `json:"waitCount"`
	WaitDurationMS int64 `json:"waitDurationMs"`
	MaxOpen        int   `json:"maxOpen"`
}

type MySQLStatus struct {
	Configured            bool            `json:"configured"`
	Connected             bool            `json:"connected"`
	Available             bool            `json:"available"`
	MaskedAddress         string          `json:"maskedAddress,omitempty"`
	Database              string          `json:"database,omitempty"`
	Version               string          `json:"version,omitempty"`
	PingLatencyMS         int64           `json:"pingLatencyMs,omitempty"`
	UptimeSeconds         int64           `json:"uptimeSeconds,omitempty"`
	DatabaseBytes         int64           `json:"databaseBytes,omitempty"`
	TableBytes            int64           `json:"tableBytes,omitempty"`
	IndexBytes            int64           `json:"indexBytes,omitempty"`
	Pool                  MySQLPoolStatus `json:"pool"`
	Connections           MetricValue     `json:"connections"`
	MaxConnections        MetricValue     `json:"maxConnections"`
	QueriesPerSecond      MetricValue     `json:"queriesPerSecond"`
	TransactionsPerSecond MetricValue     `json:"transactionsPerSecond"`
	SlowQueries           MetricValue     `json:"slowQueries"`
	BufferPoolBytes       MetricValue     `json:"bufferPoolBytes"`
	LockWaits             MetricValue     `json:"lockWaits"`
	Deadlocks             MetricValue     `json:"deadlocks"`
	DataLockWaits         MetricValue     `json:"dataLockWaits"`
	MetadataLockWaits     MetricValue     `json:"metadataLockWaits"`
	LastError             string          `json:"lastError,omitempty"`
}

type SQLiteStatus struct {
	Connected       bool   `json:"connected"`
	Available       bool   `json:"available"`
	DatabaseBytes   int64  `json:"databaseBytes,omitempty"`
	WALBytes        int64  `json:"walBytes,omitempty"`
	SHMBytes        int64  `json:"shmBytes,omitempty"`
	TotalBytes      int64  `json:"totalBytes,omitempty"`
	EffectiveBytes  int64  `json:"effectiveBytes,omitempty"`
	ReusableBytes   int64  `json:"reusableBytes,omitempty"`
	PageCount       int64  `json:"pageCount,omitempty"`
	FreePageCount   int64  `json:"freePageCount,omitempty"`
	RetentionDays   int    `json:"retentionDays"`
	CleanupStatus   string `json:"cleanupStatus,omitempty"`
	RebuildStatus   string `json:"rebuildStatus,omitempty"`
	LastCleanupAtMS int64  `json:"lastCleanupAtMs,omitempty"`
	LastRebuildAtMS int64  `json:"lastRebuildAtMs,omitempty"`
	LastError       string `json:"lastError,omitempty"`
}

type BackendsStatus struct {
	SQLite SQLiteStatus `json:"sqlite"`
	MySQL  MySQLStatus  `json:"mysql"`
}

type Status struct {
	Generation        uint64                       `json:"generation"`
	DatabaseTopology  database.TopologyStatus      `json:"databaseTopology"`
	Databases         BackendsStatus               `json:"databases"`
	Replication       database.ReplicationStatus   `json:"replication"`
	DatabaseMigration database.MigrationStatus     `json:"databaseMigration"`
	CacheCoverage     database.CacheCoverageStatus `json:"cacheCoverage"`
}

type MutationControl struct {
	ExpectedGeneration uint64 `json:"expectedGeneration"`
	IdempotencyKey     string `json:"idempotencyKey"`
}

func (c MutationControl) Validate() error {
	if c.ExpectedGeneration == 0 {
		return errors.Join(ErrInvalidRequest, errors.New("expectedGeneration is required"))
	}
	if strings.TrimSpace(c.IdempotencyKey) == "" {
		return errors.Join(ErrInvalidRequest, errors.New("idempotencyKey is required"))
	}
	if len(c.IdempotencyKey) > 200 {
		return errors.Join(ErrInvalidRequest, errors.New("idempotencyKey exceeds 200 characters"))
	}
	return nil
}

type MySQLConnectionInput struct {
	Address            string `json:"address"`
	Database           string `json:"database"`
	Username           string `json:"username"`
	Password           string `json:"password,omitempty"`
	TLSMode            string `json:"tlsMode"`
	CACertificate      string `json:"caCertificate,omitempty"`
	ConfirmInsecureTLS bool   `json:"confirmInsecureTls,omitempty"`
}

func (i MySQLConnectionInput) Validate() error {
	address := strings.TrimSpace(i.Address)
	if address == "" || strings.TrimSpace(i.Database) == "" || strings.TrimSpace(i.Username) == "" {
		return errors.Join(ErrInvalidRequest, errors.New("address, database, and username are required"))
	}
	if strings.Contains(address, "://") || strings.ContainsAny(address, "\x00\r\n") {
		return errors.Join(ErrInvalidRequest, errors.New("address must be a TCP host or host:port"))
	}
	if host, port, err := net.SplitHostPort(address); err == nil {
		parsed, parseErr := strconv.Atoi(port)
		if strings.TrimSpace(host) == "" || parseErr != nil || parsed < 1 || parsed > 65535 {
			return errors.Join(ErrInvalidRequest, errors.New("address contains an invalid host or port"))
		}
	} else if strings.Count(address, ":") > 1 && !strings.HasPrefix(address, "[") {
		return errors.Join(ErrInvalidRequest, errors.New("IPv6 addresses must use [host]:port form"))
	}
	switch strings.ToLower(strings.TrimSpace(i.TLSMode)) {
	case "verify_identity":
	case "disabled":
		if !i.ConfirmInsecureTLS {
			return errors.Join(ErrUnsafeOperation, errors.New("confirmInsecureTls must be true when TLS is disabled"))
		}
	default:
		return errors.Join(ErrInvalidRequest, errors.New("tlsMode must be verify_identity or disabled"))
	}
	return nil
}

type MySQLConfigMutation struct {
	MySQLConnectionInput
	MutationControl
}

// MySQLSchemaReinitializeMutation is an explicit destructive confirmation for
// replacing every table, view, and trigger in the configured MySQL schema with
// the current canonical manifest. ConfirmDatabase binds the confirmation to
// the exact configured schema; it is intentionally not inferred from Target.
type MySQLSchemaReinitializeMutation struct {
	MutationControl
	Target          database.BackendKind `json:"target"`
	ConfirmDatabase string               `json:"confirmDatabase"`
	ConfirmDrop     bool                 `json:"confirmDrop"`
}

func (m MySQLSchemaReinitializeMutation) Validate() error {
	if err := m.MutationControl.Validate(); err != nil {
		return err
	}
	if m.Target != database.BackendMySQL || !m.ConfirmDrop || strings.TrimSpace(m.ConfirmDatabase) == "" {
		return errors.Join(ErrUnsafeOperation,
			errors.New("target=mysql, confirmDatabase, and confirmDrop=true are required"))
	}
	if len(m.ConfirmDatabase) > 64 {
		return errors.Join(ErrInvalidRequest, errors.New("confirmDatabase exceeds 64 characters"))
	}
	return nil
}

type MySQLTestResult struct {
	Success   bool     `json:"success"`
	Version   string   `json:"version,omitempty"`
	LatencyMS int64    `json:"latencyMs,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type MigrationMutation struct {
	MutationControl
	MigrationID string `json:"migrationId,omitempty"`
}

// DangerousOperationConfirmation binds an administrator confirmation to the
// exact backend, migration, and successful validation result displayed by the
// UI.  It is deliberately separate from MutationControl: generation CAS and
// idempotency prevent stale/repeated writes, while these fields prevent a
// confirmation obtained for one destructive operation from being replayed
// against a different target or migration.
type DangerousOperationConfirmation struct {
	Target          database.BackendKind `json:"target"`
	MigrationID     string               `json:"migrationId"`
	ValidationToken string               `json:"validationToken"`
}

func (c DangerousOperationConfirmation) Validate(allowedTargets ...database.BackendKind) error {
	targetAllowed := false
	for _, allowed := range allowedTargets {
		if c.Target == allowed {
			targetAllowed = true
			break
		}
	}
	if !targetAllowed || strings.TrimSpace(c.MigrationID) == "" ||
		strings.TrimSpace(c.ValidationToken) == "" {
		return errors.Join(ErrUnsafeOperation,
			errors.New("target, migrationId, and validationToken confirmation are required"))
	}
	if len(c.MigrationID) > 200 || len(c.ValidationToken) > 512 {
		return errors.Join(ErrInvalidRequest,
			errors.New("migrationId or validationToken exceeds the allowed length"))
	}
	return nil
}

type CutoverMutation struct {
	MutationControl
	DangerousOperationConfirmation
}

type FailoverMutation struct {
	MutationControl
	DangerousOperationConfirmation
}

type CachePolicyMutation struct {
	MutationControl
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retentionDays"`
}

type CacheRebuildMutation struct {
	MutationControl
	DangerousOperationConfirmation
	RetentionDays int `json:"retentionDays"`
}

type CacheCleanupMutation struct {
	MutationControl
	DangerousOperationConfirmation
}

type CacheCleanupPreview struct {
	Eligible       bool   `json:"eligible"`
	EstimatedRows  int64  `json:"estimatedRows,omitempty"`
	EstimatedBytes int64  `json:"estimatedBytes,omitempty"`
	CoverageFromMS int64  `json:"coverageFromMs,omitempty"`
	CoverageToMS   int64  `json:"coverageToMs,omitempty"`
	BlockedReason  string `json:"blockedReason,omitempty"`
}

type ResponseCoverage struct {
	DataSource   database.BackendKind
	Completeness string
	FromMS       int64
	ToMS         int64
}

// CoverageProvider is optional. Routed read implementations expose it when a
// request was served by a fallback backend and clients need range metadata.
type CoverageProvider interface {
	ResponseCoverage(context.Context, string) ResponseCoverage
}

// Manager is the sole management-plane entry point for database topology
// mutations. Implementations must persist generation and idempotency state
// before reporting a mutation as successful.
type Manager interface {
	Status(context.Context) (Status, error)
	MigrationHistory(context.Context, int) (MigrationHistoryResponse, error)
	TestMySQL(context.Context, MySQLConnectionInput) (MySQLTestResult, error)
	SaveMySQLConfig(context.Context, MySQLConfigMutation) (Status, error)
	ReinitializeMySQLSchema(context.Context, MySQLSchemaReinitializeMutation) (Status, error)
	EnableReplication(context.Context, MutationControl) (Status, error)
	StartMigration(context.Context, MutationControl) (Status, error)
	UpdateMigration(context.Context, string, string, MutationControl) (Status, error)
	Cutover(context.Context, CutoverMutation) (Status, error)
	Failover(context.Context, FailoverMutation) (Status, error)
	UpdateCachePolicy(context.Context, CachePolicyMutation) (Status, error)
	PreviewCacheCleanup(context.Context, MutationControl) (CacheCleanupPreview, error)
	CleanupCache(context.Context, CacheCleanupMutation) (Status, error)
	RebuildCache(context.Context, CacheRebuildMutation) (Status, error)
}
