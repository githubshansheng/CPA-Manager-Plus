package databasemigration

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math"
	"slices"
	"strconv"
)

type CanonicalKind string

const (
	CanonicalNull   CanonicalKind = "null"
	CanonicalString CanonicalKind = "string"
	CanonicalBytes  CanonicalKind = "bytes"
	CanonicalInt    CanonicalKind = "int"
	CanonicalFloat  CanonicalKind = "float"
	CanonicalBool   CanonicalKind = "bool"
)

// CanonicalValue makes database normalization explicit. Validation readers
// must map the same logical manifest type to the same kind on both drivers;
// notably NULL and an empty string are different values.
type CanonicalValue struct {
	Kind  CanonicalKind
	Text  string
	Bytes []byte
}

func NullValue() CanonicalValue { return CanonicalValue{Kind: CanonicalNull} }
func StringValue(value string) CanonicalValue {
	return CanonicalValue{Kind: CanonicalString, Text: value}
}
func BytesValue(value []byte) CanonicalValue {
	return CanonicalValue{Kind: CanonicalBytes, Bytes: slices.Clone(value)}
}
func IntValue(value int64) CanonicalValue {
	return CanonicalValue{Kind: CanonicalInt, Text: strconv.FormatInt(value, 10)}
}
func FloatValue(value float64) CanonicalValue {
	return CanonicalValue{Kind: CanonicalFloat, Text: strconv.FormatUint(math.Float64bits(value), 16)}
}
func BoolValue(value bool) CanonicalValue {
	return CanonicalValue{Kind: CanonicalBool, Text: strconv.FormatBool(value)}
}

type CanonicalHasher struct {
	hash    hash.Hash
	columns []string
	rows    int64
}

func NewCanonicalHasher(columns []string) (*CanonicalHasher, error) {
	if len(columns) == 0 {
		return nil, errors.New("canonical hash requires columns")
	}
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if column == "" {
			return nil, errors.New("canonical hash contains an empty column")
		}
		if _, exists := seen[column]; exists {
			return nil, fmt.Errorf("canonical hash contains duplicate column %q", column)
		}
		seen[column] = struct{}{}
	}
	hasher := &CanonicalHasher{hash: sha256.New(), columns: slices.Clone(columns)}
	for _, column := range columns {
		hasher.write([]byte("column"), []byte(column))
	}
	return hasher, nil
}

// AddRow must be called in primary-key order. Length framing and kind tags
// avoid concatenation ambiguity and preserve NULL/empty and text/blob meaning.
func (h *CanonicalHasher) AddRow(values []CanonicalValue) error {
	if len(values) != len(h.columns) {
		return fmt.Errorf("canonical row has %d values, want %d", len(values), len(h.columns))
	}
	for index, value := range values {
		var bytesValue []byte
		switch value.Kind {
		case CanonicalNull:
			if value.Text != "" || len(value.Bytes) != 0 {
				return fmt.Errorf("column %q has a non-empty null value", h.columns[index])
			}
		case CanonicalString, CanonicalInt, CanonicalFloat, CanonicalBool:
			bytesValue = []byte(value.Text)
		case CanonicalBytes:
			bytesValue = value.Bytes
		default:
			return fmt.Errorf("column %q has unknown canonical kind %q", h.columns[index], value.Kind)
		}
		h.write([]byte(value.Kind), bytesValue)
	}
	h.rows++
	return nil
}

func (h *CanonicalHasher) Sum() (sha256Hex string, rows int64) {
	return hex.EncodeToString(h.hash.Sum(nil)), h.rows
}

func (h *CanonicalHasher) write(kind, value []byte) {
	var frame [8]byte
	binary.BigEndian.PutUint64(frame[:], uint64(len(kind)))
	h.hash.Write(frame[:])
	h.hash.Write(kind)
	binary.BigEndian.PutUint64(frame[:], uint64(len(value)))
	h.hash.Write(frame[:])
	h.hash.Write(value)
}

type ValidationSide string

const (
	ValidationSourceSide ValidationSide = "source"
	ValidationTargetSide ValidationSide = "target"
)

type TableSnapshot struct {
	SchemaHash       string
	Rows             int64
	MinKey           string
	MaxKey           string
	SHA256           string
	ForeignKeyErrors int64
}

type ValidationReader interface {
	TableSnapshot(ctx context.Context, side ValidationSide, table TableSpec) (TableSnapshot, error)
	AggregateSnapshot(ctx context.Context, side ValidationSide, frozenPriceHash string) (AggregateValidation, error)
	AppliedOutboxWatermark(ctx context.Context) (int64, error)
	DerivedDataReady(ctx context.Context) (bool, error)
}

// ValidationProgress is an in-memory heartbeat emitted while final
// validation scans the source and target stores. It is deliberately separate
// from ValidationResult so an interrupted scan never looks like a successful
// durable validation.
type ValidationProgress struct {
	Stage          string
	Table          string
	Side           ValidationSide
	CompletedSteps int
	TotalSteps     int
	ProcessedRows  int64
	TotalRows      int64
}

type ValidationProgressFunc func(ValidationProgress)

const (
	ValidationStagePreparing     = "preparing"
	ValidationStageCatchUp       = "catch_up"
	ValidationStageTableSnapshot = "table_snapshot"
	ValidationStageAggregates    = "aggregates"
	ValidationStageWatermark     = "watermark"
	ValidationStageDerived       = "derived"
	ValidationStageCompleted     = "completed"
)

type Validator struct {
	Manifest Manifest
	Reader   ValidationReader
	NowMS    func() int64
	Progress ValidationProgressFunc
}

func (v Validator) Run(ctx context.Context, migration Migration, finalOutboxWatermark int64) (ValidationResult, error) {
	if err := ValidateManifest(v.Manifest); err != nil {
		return ValidationResult{}, err
	}
	if v.Reader == nil {
		return ValidationResult{}, errors.New("validation reader is required")
	}
	result := ValidationResult{MigrationID: migration.ID, FrozenPriceHash: migration.FrozenPriceHash,
		FinalOutboxWatermark: finalOutboxWatermark}
	tables := make([]TableSpec, 0)
	for _, table := range v.Manifest.Tables() {
		if table.Derived {
			continue
		}
		tables = append(tables, table)
	}
	totalSteps := len(tables)*2 + 4
	completedSteps := 0
	v.report(ValidationProgress{Stage: ValidationStagePreparing, CompletedSteps: 0, TotalSteps: totalSteps})
	for _, table := range tables {
		v.report(ValidationProgress{Stage: ValidationStageTableSnapshot, Table: table.Name,
			Side: ValidationSourceSide, CompletedSteps: completedSteps, TotalSteps: totalSteps})
		source, err := v.Reader.TableSnapshot(ctx, ValidationSourceSide, table)
		if err != nil {
			return ValidationResult{}, fmt.Errorf("validate source table %q: %w", table.Name, err)
		}
		completedSteps++
		v.report(ValidationProgress{Stage: ValidationStageTableSnapshot, Table: table.Name,
			Side: ValidationSourceSide, CompletedSteps: completedSteps, TotalSteps: totalSteps,
			ProcessedRows: source.Rows, TotalRows: source.Rows})
		v.report(ValidationProgress{Stage: ValidationStageTableSnapshot, Table: table.Name,
			Side: ValidationTargetSide, CompletedSteps: completedSteps, TotalSteps: totalSteps})
		target, err := v.Reader.TableSnapshot(ctx, ValidationTargetSide, table)
		if err != nil {
			return ValidationResult{}, fmt.Errorf("validate target table %q: %w", table.Name, err)
		}
		completedSteps++
		v.report(ValidationProgress{Stage: ValidationStageTableSnapshot, Table: table.Name,
			Side: ValidationTargetSide, CompletedSteps: completedSteps, TotalSteps: totalSteps,
			ProcessedRows: target.Rows, TotalRows: target.Rows})
		result.Tables = append(result.Tables, CompareTableValidation(table.Name, source, target))
	}
	v.report(ValidationProgress{Stage: ValidationStageAggregates, Side: ValidationSourceSide,
		CompletedSteps: completedSteps, TotalSteps: totalSteps})
	var err error
	result.SourceAggregate, err = v.Reader.AggregateSnapshot(ctx, ValidationSourceSide, migration.FrozenPriceHash)
	if err != nil {
		return ValidationResult{}, fmt.Errorf("validate source aggregates: %w", err)
	}
	completedSteps++
	v.report(ValidationProgress{Stage: ValidationStageAggregates, Side: ValidationSourceSide,
		CompletedSteps: completedSteps, TotalSteps: totalSteps})
	v.report(ValidationProgress{Stage: ValidationStageAggregates, Side: ValidationTargetSide,
		CompletedSteps: completedSteps, TotalSteps: totalSteps})
	result.TargetAggregate, err = v.Reader.AggregateSnapshot(ctx, ValidationTargetSide, migration.FrozenPriceHash)
	if err != nil {
		return ValidationResult{}, fmt.Errorf("validate target aggregates: %w", err)
	}
	completedSteps++
	v.report(ValidationProgress{Stage: ValidationStageAggregates, Side: ValidationTargetSide,
		CompletedSteps: completedSteps, TotalSteps: totalSteps})
	v.report(ValidationProgress{Stage: ValidationStageWatermark, CompletedSteps: completedSteps,
		TotalSteps: totalSteps})
	result.AppliedWatermark, err = v.Reader.AppliedOutboxWatermark(ctx)
	if err != nil {
		return ValidationResult{}, fmt.Errorf("validate outbox watermark: %w", err)
	}
	completedSteps++
	v.report(ValidationProgress{Stage: ValidationStageWatermark, CompletedSteps: completedSteps,
		TotalSteps: totalSteps})
	v.report(ValidationProgress{Stage: ValidationStageDerived, CompletedSteps: completedSteps,
		TotalSteps: totalSteps})
	result.DerivedDataReady, err = v.Reader.DerivedDataReady(ctx)
	if err != nil {
		return ValidationResult{}, fmt.Errorf("validate derived data: %w", err)
	}
	completedSteps++
	v.report(ValidationProgress{Stage: ValidationStageDerived, CompletedSteps: completedSteps,
		TotalSteps: totalSteps})
	if v.NowMS != nil {
		result.ValidatedAtMS = v.NowMS()
	}
	result.Passed = ValidationPassed(result)
	if result.Passed {
		result.Token = ValidationToken(result)
	}
	v.report(ValidationProgress{Stage: ValidationStageCompleted, CompletedSteps: totalSteps,
		TotalSteps: totalSteps})
	return result, nil
}

func (v Validator) report(progress ValidationProgress) {
	if v.Progress != nil {
		v.Progress(progress)
	}
}

func CompareTableValidation(table string, source, target TableSnapshot) TableValidation {
	result := TableValidation{Table: table, SchemaHashSource: source.SchemaHash,
		SchemaHashTarget: target.SchemaHash, SourceRows: source.Rows, TargetRows: target.Rows,
		SourceMinKey: source.MinKey, TargetMinKey: target.MinKey, SourceMaxKey: source.MaxKey,
		TargetMaxKey: target.MaxKey, SourceSHA256: source.SHA256, TargetSHA256: target.SHA256,
		SourceForeignKeyErrors: source.ForeignKeyErrors, TargetForeignKeyErrors: target.ForeignKeyErrors}
	result.Passed = result.SchemaHashSource != "" && result.SchemaHashSource == result.SchemaHashTarget &&
		result.SourceRows == result.TargetRows && result.SourceMinKey == result.TargetMinKey &&
		result.SourceMaxKey == result.TargetMaxKey && result.SourceSHA256 != "" &&
		result.SourceSHA256 == result.TargetSHA256 && result.SourceForeignKeyErrors == 0 &&
		result.TargetForeignKeyErrors == 0
	if !result.Passed {
		result.Error = "schema, row count, primary-key range, foreign keys, or field hash differs"
	}
	return result
}

func ValidationPassed(result ValidationResult) bool {
	if !result.DerivedDataReady || result.FinalOutboxWatermark != result.AppliedWatermark ||
		len(result.Tables) == 0 || len(result.Errors) != 0 {
		return false
	}
	for _, table := range result.Tables {
		if !table.Passed || table.SchemaHashSource == "" || table.SchemaHashSource != table.SchemaHashTarget ||
			table.SourceRows != table.TargetRows || table.SourceMinKey != table.TargetMinKey ||
			table.SourceMaxKey != table.TargetMaxKey || table.SourceSHA256 == "" ||
			table.SourceSHA256 != table.TargetSHA256 || table.SourceForeignKeyErrors != 0 ||
			table.TargetForeignKeyErrors != 0 {
			return false
		}
	}
	return result.SourceAggregate == result.TargetAggregate
}

func ValidationToken(result ValidationResult) string {
	copy := result
	copy.Token = ""
	copy.Passed = ValidationPassed(copy)
	copy.Tables = slices.Clone(copy.Tables)
	slices.SortFunc(copy.Tables, func(left, right TableValidation) int {
		if left.Table < right.Table {
			return -1
		}
		if left.Table > right.Table {
			return 1
		}
		return 0
	})
	encoded, err := json.Marshal(copy)
	if err != nil {
		panic(fmt.Sprintf("validation result cannot be encoded: %v", err))
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func ManifestSchemaHash(table TableSpec) string {
	encoded, err := json.Marshal(table)
	if err != nil {
		panic(fmt.Sprintf("table manifest cannot be encoded: %v", err))
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
