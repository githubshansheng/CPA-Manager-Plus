package database

import (
	"context"
	"database/sql"
)

// BackendKind identifies a storage engine without exposing driver details.
type BackendKind string

const (
	BackendSQLite BackendKind = "sqlite"
	BackendMySQL  BackendKind = "mysql"
)

// Backend is the minimal dialect-neutral lifecycle contract used by routing
// and migration orchestration. Repository behavior remains a separate layer.
type Backend interface {
	Kind() BackendKind
	DB() *sql.DB
	Ping(context.Context) error
	Stats() sql.DBStats
	Close() error
}

type SQLBackend struct {
	kind BackendKind
	db   *sql.DB
}

func NewSQLBackend(kind BackendKind, db *sql.DB) *SQLBackend {
	return &SQLBackend{kind: kind, db: db}
}

func (b *SQLBackend) Kind() BackendKind              { return b.kind }
func (b *SQLBackend) DB() *sql.DB                    { return b.db }
func (b *SQLBackend) Ping(ctx context.Context) error { return b.db.PingContext(ctx) }
func (b *SQLBackend) Stats() sql.DBStats             { return b.db.Stats() }
func (b *SQLBackend) Close() error                   { return b.db.Close() }

// RouteSnapshot is the immutable control-plane token used by one application
// operation. Generation protects configuration changes while Epoch fences a
// former write primary after a manual failover.
type RouteSnapshot struct {
	Generation          uint64
	Epoch               uint64
	WritePrimary        BackendKind
	BusinessReadPrimary BackendKind
	SystemReadPrimary   BackendKind
}

type TopologyStatus struct {
	Generation          uint64      `json:"generation"`
	WritePrimary        BackendKind `json:"writePrimary"`
	BusinessReadPrimary BackendKind `json:"businessReadPrimary"`
	SystemReadPrimary   BackendKind `json:"systemReadPrimary"`
	FallbackSource      BackendKind `json:"fallbackSource,omitempty"`
	FallbackActive      bool        `json:"fallbackActive,omitempty"`
	ReadOnly            bool        `json:"readOnly,omitempty"`
	FailoverState       string      `json:"failoverState,omitempty"`
	ReadCutoverReady    bool        `json:"readCutoverReady"`
	WriteFailoverReady  bool        `json:"writeFailoverReady"`
}

type BackendStatus struct {
	Kind          BackendKind `json:"kind"`
	Configured    bool        `json:"configured"`
	Connected     bool        `json:"connected"`
	Writable      bool        `json:"writable"`
	Version       string      `json:"version,omitempty"`
	LatencyMS     int64       `json:"latencyMs,omitempty"`
	LastCheckedMS int64       `json:"lastCheckedMs,omitempty"`
	Error         string      `json:"error,omitempty"`
}

type ReplicationStatus struct {
	Enabled            bool        `json:"enabled"`
	State              string      `json:"state,omitempty"`
	Direction          string      `json:"direction,omitempty"`
	Epoch              uint64      `json:"epoch"`
	Source             BackendKind `json:"source,omitempty"`
	Target             BackendKind `json:"target,omitempty"`
	SourceWatermark    int64       `json:"sourceWatermark"`
	TargetWatermark    int64       `json:"targetWatermark"`
	PendingMutations   int64       `json:"pendingMutations"`
	PendingBytes       int64       `json:"pendingBytes"`
	OldestPendingAtMS  int64       `json:"oldestPendingAtMs,omitempty"`
	MutationsPerSecond float64     `json:"mutationsPerSecond"`
	RetryCount         int64       `json:"retryCount"`
	LastProgressAtMS   int64       `json:"lastProgressAtMs,omitempty"`
	LastSuccessAtMS    int64       `json:"lastSuccessAtMs,omitempty"`
	HeartbeatAtMS      int64       `json:"heartbeatAtMs,omitempty"`
	Stalled            bool        `json:"stalled"`
	Error              string      `json:"error,omitempty"`
}

type MigrationTableStatus struct {
	Name           string  `json:"name"`
	Stage          string  `json:"stage,omitempty"`
	CopiedRows     int64   `json:"copiedRows"`
	TotalRows      int64   `json:"totalRows"`
	CopiedBytes    int64   `json:"copiedBytes"`
	Requests       int64   `json:"requests"`
	LastPrimaryKey string  `json:"lastPrimaryKey,omitempty"`
	Progress       float64 `json:"progress"`
	Completed      bool    `json:"completed"`
	Active         bool    `json:"active"`
	UpdatedAtMS    int64   `json:"updatedAtMs,omitempty"`
	Checksum       string  `json:"checksum,omitempty"`
	Error          string  `json:"error,omitempty"`
}

type MigrationStatus struct {
	ID                  string                       `json:"id,omitempty"`
	Phase               string                       `json:"phase,omitempty"`
	Status              string                       `json:"status,omitempty"`
	State               string                       `json:"state,omitempty"`
	Source              BackendKind                  `json:"source,omitempty"`
	Target              BackendKind                  `json:"target,omitempty"`
	StartedAtMS         int64                        `json:"startedAtMs,omitempty"`
	UpdatedAtMS         int64                        `json:"updatedAtMs,omitempty"`
	FinishedAtMS        int64                        `json:"finishedAtMs,omitempty"`
	RowsPerSecond       float64                      `json:"rowsPerSecond"`
	ETASeconds          int64                        `json:"etaSeconds,omitempty"`
	CopiedRows          int64                        `json:"copiedRows"`
	TotalRows           int64                        `json:"totalRows"`
	CopiedBytes         int64                        `json:"copiedBytes"`
	Requests            int64                        `json:"requests"`
	ProgressPercent     float64                      `json:"progressPercent"`
	CompletedSteps      int                          `json:"completedSteps"`
	TotalSteps          int                          `json:"totalSteps"`
	CurrentTable        string                       `json:"currentTable,omitempty"`
	CurrentTableActive  bool                         `json:"currentTableActive"`
	CurrentTableSinceMS int64                        `json:"currentTableSinceMs,omitempty"`
	InputTokens         int64                        `json:"inputTokens,omitempty"`
	OutputTokens        int64                        `json:"outputTokens,omitempty"`
	CachedTokens        int64                        `json:"cachedTokens,omitempty"`
	ReasoningTokens     int64                        `json:"reasoningTokens,omitempty"`
	Cost                float64                      `json:"cost,omitempty"`
	ValidationValid     bool                         `json:"validationValid"`
	ValidationProgress  *MigrationValidationProgress `json:"validationProgress,omitempty"`
	ValidationError     string                       `json:"validationError,omitempty"`
	ValidationToken     string                       `json:"validationToken,omitempty"`
	PriceManifestSHA256 string                       `json:"priceManifestSha256,omitempty"`
	Tables              []MigrationTableStatus       `json:"tables,omitempty"`
	DerivedTables       []MigrationTableStatus       `json:"derivedTables,omitempty"`
	Error               string                       `json:"error,omitempty"`
}

// MigrationValidationProgress describes the live, non-authoritative work
// performed by final validation. It is intentionally kept separate from the
// history/derived copy checkpoints: validation scans do not mutate those
// checkpoints and can be safely retried after a process restart.
type MigrationValidationProgress struct {
	Running             bool    `json:"running"`
	Stage               string  `json:"stage,omitempty"`
	CurrentTable        string  `json:"currentTable,omitempty"`
	CurrentTableSinceMS int64   `json:"currentTableSinceMs,omitempty"`
	Side                string  `json:"side,omitempty"`
	CompletedSteps      int     `json:"completedSteps"`
	TotalSteps          int     `json:"totalSteps"`
	ProcessedRows       int64   `json:"processedRows"`
	TotalRows           int64   `json:"totalRows"`
	ProgressPercent     float64 `json:"progressPercent"`
	StartedAtMS         int64   `json:"startedAtMs,omitempty"`
	UpdatedAtMS         int64   `json:"updatedAtMs,omitempty"`
}

type CacheCoverageStatus struct {
	RetentionDays     int    `json:"retentionDays"`
	CleanupEnabled    bool   `json:"cleanupEnabled"`
	Complete          bool   `json:"complete"`
	FromMS            int64  `json:"fromMs,omitempty"`
	ToMS              int64  `json:"toMs,omitempty"`
	MinimumEventID    int64  `json:"minimumEventId,omitempty"`
	MaximumEventID    int64  `json:"maximumEventId,omitempty"`
	SyncedWatermark   int64  `json:"syncedWatermark,omitempty"`
	LastValidatedAtMS int64  `json:"lastValidatedAtMs,omitempty"`
	CleanupPaused     bool   `json:"cleanupPaused"`
	PauseReason       string `json:"pauseReason,omitempty"`
}
