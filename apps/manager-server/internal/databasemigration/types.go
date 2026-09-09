package databasemigration

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultBatchSize     = 1000
	DefaultRetentionDays = 15
	// DefaultCleanupEnabled keeps scheduled cache cleanup opt-in. Keeping the
	// default disabled avoids deleting historical SQLite rows until an
	// administrator has reviewed the retention window and enabled the policy.
	DefaultCleanupEnabled = false
	DefaultMaxBatchBytes  = int64(4 << 20)
	ReplicationWarnAfter  = 60 * time.Second
	ReplicationStallAfter = 120 * time.Second
)

type Dialect string

const (
	DialectSQLite Dialect = "sqlite"
	DialectMySQL  Dialect = "mysql"
)

type Backend string

const (
	BackendSQLite Backend = "sqlite"
	BackendMySQL  Backend = "mysql"
)

type MigrationPhase string

const (
	PhaseEnableDualWrite MigrationPhase = "enable_dual_write"
	PhaseCopyHistory     MigrationPhase = "copy_history"
	PhaseRebuildDerived  MigrationPhase = "rebuild_derived"
	PhaseValidate        MigrationPhase = "validate"
	PhaseReadyToCutover  MigrationPhase = "ready_to_cutover"
	PhaseCompleted       MigrationPhase = "completed"
)

type RunStatus string

const (
	StatusRunning   RunStatus = "running"
	StatusPaused    RunStatus = "paused"
	StatusCanceled  RunStatus = "canceled"
	StatusFailed    RunStatus = "failed"
	StatusSucceeded RunStatus = "succeeded"
)

var (
	ErrNotFound             = errors.New("database migration record not found")
	ErrGenerationConflict   = errors.New("database migration generation conflict")
	ErrEpochFenced          = errors.New("database write epoch has been fenced")
	ErrInvalidTransition    = errors.New("invalid database migration state transition")
	ErrInvalidMutationGroup = errors.New("invalid mutation group")
	ErrPartialDuplicate     = errors.New("only part of a transaction group exists in inbox")
	ErrCleanupUnsafe        = errors.New("sqlite cache cleanup safety gate rejected the operation")
	ErrIdempotencyConflict  = errors.New("idempotency key was already used for a different request")
	ErrRowVersionConflict   = errors.New("row version is already bound to different contents")
)

type ConflictError struct {
	Kind     error
	Expected int64
	Actual   int64
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%v: expected %d, actual %d", e.Kind, e.Expected, e.Actual)
}

func (e *ConflictError) Unwrap() error { return e.Kind }

type RoutingState struct {
	Generation   int64   `json:"generation"`
	WritePrimary Backend `json:"writePrimary"`
	BusinessRead Backend `json:"businessRead"`
	SystemRead   Backend `json:"systemRead"`
	Epoch        int64   `json:"epoch"`
	UpdatedAtMS  int64   `json:"updatedAtMs"`
}

type RoutingChange struct {
	WritePrimary Backend
	BusinessRead Backend
	SystemRead   Backend
}

type Migration struct {
	ID              string         `json:"id"`
	IdempotencyKey  string         `json:"idempotencyKey"`
	Generation      int64          `json:"generation"`
	Source          Backend        `json:"source"`
	Target          Backend        `json:"target"`
	Phase           MigrationPhase `json:"phase"`
	Status          RunStatus      `json:"status"`
	BatchSize       int            `json:"batchSize"`
	FrozenPriceHash string         `json:"frozenPriceHash,omitempty"`
	// FrozenPriceBook is persisted with the migration metadata but is never
	// returned by management APIs. Validation uses it to reproduce the exact
	// price basis captured when history migration started.
	FrozenPriceBook      json.RawMessage   `json:"-"`
	FinalOutboxWatermark int64             `json:"finalOutboxWatermark,omitempty"`
	Validation           *ValidationResult `json:"validation,omitempty"`
	ValidationToken      string            `json:"validationToken,omitempty"`
	CreatedAtMS          int64             `json:"createdAtMs"`
	UpdatedAtMS          int64             `json:"updatedAtMs"`
	FinishedAtMS         int64             `json:"finishedAtMs,omitempty"`
	LastError            string            `json:"lastError,omitempty"`
}

// MigrationEvent is an append-only audit record for one persisted migration
// state transition. Errors remain available after a later resume clears the
// task's current LastError field.
type MigrationEvent struct {
	ID             int64          `json:"id"`
	MigrationID    string         `json:"migrationId"`
	EventType      string         `json:"eventType"`
	PreviousPhase  MigrationPhase `json:"previousPhase,omitempty"`
	PreviousStatus RunStatus      `json:"previousStatus,omitempty"`
	Phase          MigrationPhase `json:"phase"`
	Status         RunStatus      `json:"status"`
	Generation     int64          `json:"generation"`
	Table          string         `json:"table,omitempty"`
	Error          string         `json:"error,omitempty"`
	CreatedAtMS    int64          `json:"createdAtMs"`
}

type CreateMigrationRequest struct {
	ID                 string
	IdempotencyKey     string
	ExpectedGeneration int64
	Source             Backend
	Target             Backend
	BatchSize          int
	FrozenPriceHash    string
	FrozenPriceBook    json.RawMessage
}

type TableProgress struct {
	MigrationID     string          `json:"migrationId"`
	Table           string          `json:"table"`
	Checkpoint      json.RawMessage `json:"checkpoint,omitempty"`
	SourceWatermark json.RawMessage `json:"sourceWatermark,omitempty"`
	RowsCopied      int64           `json:"rowsCopied"`
	BytesCopied     int64           `json:"bytesCopied"`
	Requests        int64           `json:"requests"`
	BatchSize       int             `json:"batchSize"`
	Completed       bool            `json:"completed"`
	UpdatedAtMS     int64           `json:"updatedAtMs"`
}

type Operation string

const (
	OperationInsert Operation = "insert"
	OperationUpdate Operation = "update"
	OperationDelete Operation = "delete"
)

type Mutation struct {
	ID            string          `json:"id"`
	TransactionID string          `json:"transactionId"`
	Sequence      int             `json:"sequence"`
	Source        Backend         `json:"source"`
	Target        Backend         `json:"target"`
	SourceEpoch   int64           `json:"sourceEpoch"`
	Table         string          `json:"table"`
	Operation     Operation       `json:"operation"`
	PrimaryKey    json.RawMessage `json:"primaryKey"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	SchemaVersion int             `json:"schemaVersion"`
	RowVersion    int64           `json:"rowVersion"`
	CreatedAtMS   int64           `json:"createdAtMs"`
	OutboxID      int64           `json:"outboxId,omitempty"`
}

type MutationGroup struct {
	TransactionID string     `json:"transactionId"`
	Source        Backend    `json:"source"`
	Target        Backend    `json:"target"`
	SourceEpoch   int64      `json:"sourceEpoch"`
	Watermark     int64      `json:"watermark,omitempty"`
	Mutations     []Mutation `json:"mutations"`
}

type ReplicationState struct {
	Direction            string  `json:"direction"`
	Source               Backend `json:"source"`
	Target               Backend `json:"target"`
	Epoch                int64   `json:"epoch"`
	SourceWatermark      int64   `json:"sourceWatermark"`
	TargetWatermark      int64   `json:"targetWatermark"`
	BacklogRows          int64   `json:"backlogRows"`
	BacklogBytes         int64   `json:"backlogBytes"`
	OldestBacklogAtMS    int64   `json:"oldestBacklogAtMs,omitempty"`
	ThroughputRowsPerSec float64 `json:"throughputRowsPerSec"`
	Retries              int64   `json:"retries"`
	HeartbeatAtMS        int64   `json:"heartbeatAtMs,omitempty"`
	LastProgressAtMS     int64   `json:"lastProgressAtMs,omitempty"`
	LastSuccessAtMS      int64   `json:"lastSuccessAtMs,omitempty"`
	LastError            string  `json:"lastError,omitempty"`
	Warning              bool    `json:"warning"`
	Stalled              bool    `json:"stalled"`
}

type ReplicationUpdate struct {
	Direction            string
	Source               Backend
	Target               Backend
	Epoch                int64
	SourceWatermark      int64
	TargetWatermark      int64
	BacklogRows          int64
	BacklogBytes         int64
	OldestBacklogAtMS    int64
	ThroughputRowsPerSec float64
	Retries              int64
	MadeProgress         bool
	Succeeded            bool
	LastError            string
}

type TableValidation struct {
	Table                  string `json:"table"`
	SchemaHashSource       string `json:"schemaHashSource"`
	SchemaHashTarget       string `json:"schemaHashTarget"`
	SourceRows             int64  `json:"sourceRows"`
	TargetRows             int64  `json:"targetRows"`
	SourceMinKey           string `json:"sourceMinKey,omitempty"`
	TargetMinKey           string `json:"targetMinKey,omitempty"`
	SourceMaxKey           string `json:"sourceMaxKey,omitempty"`
	TargetMaxKey           string `json:"targetMaxKey,omitempty"`
	SourceSHA256           string `json:"sourceSha256"`
	TargetSHA256           string `json:"targetSha256"`
	SourceForeignKeyErrors int64  `json:"sourceForeignKeyErrors"`
	TargetForeignKeyErrors int64  `json:"targetForeignKeyErrors"`
	Passed                 bool   `json:"passed"`
	Error                  string `json:"error,omitempty"`
}

type AggregateValidation struct {
	InputTokens     int64   `json:"inputTokens"`
	OutputTokens    int64   `json:"outputTokens"`
	CachedTokens    int64   `json:"cachedTokens"`
	ReasoningTokens int64   `json:"reasoningTokens"`
	Successful      int64   `json:"successful"`
	Failed          int64   `json:"failed"`
	Cost            float64 `json:"cost"`
}

type ValidationResult struct {
	MigrationID          string              `json:"migrationId"`
	FrozenPriceHash      string              `json:"frozenPriceHash"`
	FinalOutboxWatermark int64               `json:"finalOutboxWatermark"`
	AppliedWatermark     int64               `json:"appliedWatermark"`
	Tables               []TableValidation   `json:"tables"`
	SourceAggregate      AggregateValidation `json:"sourceAggregate"`
	TargetAggregate      AggregateValidation `json:"targetAggregate"`
	DerivedDataReady     bool                `json:"derivedDataReady"`
	Passed               bool                `json:"passed"`
	Token                string              `json:"token,omitempty"`
	ValidatedAtMS        int64               `json:"validatedAtMs"`
	Errors               []string            `json:"errors,omitempty"`
}

type CachePolicy struct {
	Generation    int64 `json:"generation"`
	Enabled       bool  `json:"enabled"`
	RetentionDays int   `json:"retentionDays"`
	BatchSize     int   `json:"batchSize"`
	UpdatedAtMS   int64 `json:"updatedAtMs"`
}

type CacheCoverage struct {
	EarliestAtMS int64 `json:"earliestAtMs,omitempty"`
	LatestAtMS   int64 `json:"latestAtMs,omitempty"`
	EarliestID   int64 `json:"earliestId,omitempty"`
	LatestID     int64 `json:"latestId,omitempty"`
	Watermark    int64 `json:"watermark"`
	Complete     bool  `json:"complete"`
	UpdatedAtMS  int64 `json:"updatedAtMs"`
}

type CleanupSafety struct {
	MySQLAvailable        bool
	MigrationComplete     bool
	ReplicationCaughtUp   bool
	SynchronizedWatermark int64
	ValidationPassed      bool
	ValidationToken       string
	ValidationAtMS        int64
	ValidationMaxAge      time.Duration
}

type TableCleanupPreview struct {
	Table         string `json:"table"`
	Rows          int64  `json:"rows"`
	FromMS        int64  `json:"fromMs,omitempty"`
	ToMS          int64  `json:"toMs,omitempty"`
	ProtectedRows int64  `json:"protectedRows"`
}

type CleanupPreview struct {
	CutoffMS      int64                 `json:"cutoffMs"`
	Tables        []TableCleanupPreview `json:"tables"`
	EstimatedRows int64                 `json:"estimatedRows"`
	CreatedAtMS   int64                 `json:"createdAtMs"`
}

type MaintenanceTaskKind string

const (
	TaskCleanup MaintenanceTaskKind = "cleanup"
	TaskRebuild MaintenanceTaskKind = "rebuild"
)

type MaintenanceTask struct {
	ID              string              `json:"id"`
	IdempotencyKey  string              `json:"idempotencyKey"`
	Generation      int64               `json:"generation"`
	Kind            MaintenanceTaskKind `json:"kind"`
	Status          RunStatus           `json:"status"`
	RetentionDays   int                 `json:"retentionDays"`
	ValidationToken string              `json:"validationToken,omitempty"`
	Preview         CleanupPreview      `json:"preview"`
	CurrentTable    string              `json:"currentTable,omitempty"`
	ProcessedRows   int64               `json:"processedRows"`
	CreatedAtMS     int64               `json:"createdAtMs"`
	UpdatedAtMS     int64               `json:"updatedAtMs"`
	FinishedAtMS    int64               `json:"finishedAtMs,omitempty"`
	LastError       string              `json:"lastError,omitempty"`
}

type OperationIdempotencyRecord struct {
	Key         string          `json:"key"`
	Operation   string          `json:"operation"`
	RequestHash string          `json:"requestHash"`
	Result      json.RawMessage `json:"result"`
	Generation  int64           `json:"generation"`
	CreatedAtMS int64           `json:"createdAtMs"`
	UpdatedAtMS int64           `json:"updatedAtMs"`
}

type RowVersionRecord struct {
	Table        string          `json:"table"`
	PrimaryKey   json.RawMessage `json:"primaryKey"`
	SourceEpoch  int64           `json:"sourceEpoch"`
	RowVersion   int64           `json:"rowVersion"`
	MutationID   string          `json:"mutationId"`
	MutationHash string          `json:"mutationHash"`
	UpdatedAtMS  int64           `json:"updatedAtMs"`
}

func validBackend(value Backend) bool {
	return value == BackendSQLite || value == BackendMySQL
}
