package databasemigration

import (
	"context"
	"testing"
)

func TestCanonicalHashPreservesNullEmptyAndBinaryMeaning(t *testing.T) {
	columns := []string{"nullable", "raw_json", "fail_body"}
	hashRows := func(row []CanonicalValue) string {
		hasher, err := NewCanonicalHasher(columns)
		if err != nil {
			t.Fatal(err)
		}
		if err := hasher.AddRow(row); err != nil {
			t.Fatal(err)
		}
		digest, count := hasher.Sum()
		if count != 1 {
			t.Fatalf("row count = %d", count)
		}
		return digest
	}
	nullHash := hashRows([]CanonicalValue{NullValue(), StringValue(""), BytesValue(nil)})
	emptyHash := hashRows([]CanonicalValue{StringValue(""), StringValue(""), BytesValue(nil)})
	binaryHash := hashRows([]CanonicalValue{NullValue(), StringValue(""), BytesValue([]byte{})})
	if nullHash == emptyHash {
		t.Fatal("NULL and empty string hashes are equal")
	}
	// A zero-length blob is intentionally distinct from an empty string, but
	// BytesValue(nil) and BytesValue([]byte{}) have the same blob value.
	if nullHash != binaryHash {
		t.Fatal("nil and empty byte slices should represent the same zero-length blob")
	}
}

func TestValidationRequiresEveryFieldAndWatermarkToMatch(t *testing.T) {
	snapshot := TableSnapshot{SchemaHash: "schema", Rows: 10, MinKey: "1", MaxKey: "10", SHA256: "all-fields"}
	table := CompareTableValidation("usage_events", snapshot, snapshot)
	aggregate := AggregateValidation{InputTokens: 10, OutputTokens: 20, Successful: 1, Cost: 0.25}
	result := ValidationResult{FinalOutboxWatermark: 8, AppliedWatermark: 8,
		Tables: []TableValidation{table}, SourceAggregate: aggregate, TargetAggregate: aggregate,
		DerivedDataReady: true, ValidatedAtMS: 100}
	if !ValidationPassed(result) {
		t.Fatalf("matching validation did not pass: %#v", result)
	}
	token := ValidationToken(result)
	result.Tables[0].TargetSHA256 = "missing-field"
	result.Tables[0].Passed = false
	if ValidationPassed(result) {
		t.Fatal("field hash mismatch passed validation")
	}
	if token == ValidationToken(result) {
		t.Fatal("validation token did not bind the per-field hash result")
	}
}

func TestValidatorReportsTableAndAggregateProgress(t *testing.T) {
	manifest := StaticManifest{
		{Name: "first", Columns: []ColumnSpec{{Name: "id", LogicalType: "integer"}}, PrimaryKey: []string{"id"}},
		{Name: "second", Columns: []ColumnSpec{{Name: "id", LogicalType: "integer"}}, PrimaryKey: []string{"id"}},
		{Name: "derived", Columns: []ColumnSpec{{Name: "id", LogicalType: "integer"}}, PrimaryKey: []string{"id"}, Derived: true},
	}
	reader := progressValidationReader{}
	var updates []ValidationProgress
	validator := Validator{
		Manifest: manifest, Reader: reader, NowMS: func() int64 { return 42 },
		Progress: func(update ValidationProgress) { updates = append(updates, update) },
	}
	result, err := validator.Run(context.Background(), Migration{ID: "m", FrozenPriceHash: "prices"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Fatalf("validation unexpectedly failed: %#v", result)
	}
	if len(updates) < 12 {
		t.Fatalf("progress updates = %d, want table and aggregate updates", len(updates))
	}
	first := updates[0]
	if first.Stage != ValidationStagePreparing || first.TotalSteps != 8 || first.CompletedSteps != 0 {
		t.Fatalf("initial progress = %#v", first)
	}
	seenSource, seenTarget, seenAggregate, seenWatermark, seenDerived := false, false, false, false, false
	for _, update := range updates {
		switch {
		case update.Stage == ValidationStageTableSnapshot && update.Side == ValidationSourceSide:
			seenSource = true
		case update.Stage == ValidationStageTableSnapshot && update.Side == ValidationTargetSide:
			seenTarget = true
		case update.Stage == ValidationStageAggregates:
			seenAggregate = true
		case update.Stage == ValidationStageWatermark:
			seenWatermark = true
		case update.Stage == ValidationStageDerived:
			seenDerived = true
		}
	}
	if !seenSource || !seenTarget || !seenAggregate || !seenWatermark || !seenDerived {
		t.Fatalf("missing progress stages: %#v", updates)
	}
	last := updates[len(updates)-1]
	if last.Stage != ValidationStageCompleted || last.CompletedSteps != last.TotalSteps || last.TotalSteps != 8 {
		t.Fatalf("final progress = %#v", last)
	}
}

type progressValidationReader struct{}

func (progressValidationReader) TableSnapshot(context.Context, ValidationSide, TableSpec) (TableSnapshot, error) {
	return TableSnapshot{SchemaHash: "schema", Rows: 2, MinKey: "1", MaxKey: "2", SHA256: "hash"}, nil
}

func (progressValidationReader) AggregateSnapshot(context.Context, ValidationSide, string) (AggregateValidation, error) {
	return AggregateValidation{InputTokens: 1, Successful: 1}, nil
}

func (progressValidationReader) AppliedOutboxWatermark(context.Context) (int64, error) { return 7, nil }

func (progressValidationReader) DerivedDataReady(context.Context) (bool, error) { return true, nil }
