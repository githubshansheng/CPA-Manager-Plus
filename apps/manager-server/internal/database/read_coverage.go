package database

import (
	"context"
	"sync"
)

const (
	DataComplete = "complete"
	DataPartial  = "partial"
)

// ReadCoverage describes the backend and verified range that contributed to
// one HTTP response. When a response performs several reads, partial SQLite
// coverage dominates a complete MySQL read and retained ranges are
// conservatively intersected.
type ReadCoverage struct {
	DataSource   BackendKind
	Completeness string
	FromMS       int64
	ToMS         int64
}

type ReadCoverageRecorder struct {
	mu       sync.Mutex
	coverage ReadCoverage
}

type readCoverageContextKey struct{}

func WithReadCoverageRecorder(ctx context.Context) (context.Context, *ReadCoverageRecorder) {
	recorder := &ReadCoverageRecorder{}
	return context.WithValue(ctx, readCoverageContextKey{}, recorder), recorder
}

func RecordReadCoverage(ctx context.Context, coverage ReadCoverage) {
	if ctx == nil || coverage.DataSource == "" {
		return
	}
	recorder, _ := ctx.Value(readCoverageContextKey{}).(*ReadCoverageRecorder)
	if recorder == nil {
		return
	}
	recorder.Record(coverage)
}

func (r *ReadCoverageRecorder) Record(next ReadCoverage) {
	if r == nil || next.DataSource == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.coverage.DataSource == "" {
		r.coverage = next
		return
	}
	if r.coverage.DataSource != next.DataSource &&
		(r.coverage.DataSource == BackendSQLite || next.DataSource == BackendSQLite) {
		r.coverage.DataSource = BackendSQLite
	}
	if next.Completeness == DataPartial {
		r.coverage.Completeness = DataPartial
		if next.FromMS > r.coverage.FromMS {
			r.coverage.FromMS = next.FromMS
		}
		if next.ToMS > 0 && (r.coverage.ToMS == 0 || next.ToMS < r.coverage.ToMS) {
			r.coverage.ToMS = next.ToMS
		}
	} else if r.coverage.Completeness == "" {
		r.coverage.Completeness = next.Completeness
	}
}

func (r *ReadCoverageRecorder) Snapshot() ReadCoverage {
	if r == nil {
		return ReadCoverage{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.coverage
}
