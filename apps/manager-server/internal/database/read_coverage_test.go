package database

import (
	"context"
	"testing"
)

func TestReadCoverageRecorderKeepsConservativePartialIntersection(t *testing.T) {
	ctx, recorder := WithReadCoverageRecorder(context.Background())
	RecordReadCoverage(ctx, ReadCoverage{
		DataSource: BackendMySQL, Completeness: DataComplete,
	})
	RecordReadCoverage(ctx, ReadCoverage{
		DataSource: BackendSQLite, Completeness: DataPartial, FromMS: 100, ToMS: 500,
	})
	RecordReadCoverage(ctx, ReadCoverage{
		DataSource: BackendSQLite, Completeness: DataPartial, FromMS: 200, ToMS: 400,
	})
	got := recorder.Snapshot()
	if got.DataSource != BackendSQLite || got.Completeness != DataPartial ||
		got.FromMS != 200 || got.ToMS != 400 {
		t.Fatalf("coverage = %#v", got)
	}
}
