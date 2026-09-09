package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
)

func TestWithDatabaseCoverageMarksActualPartialFallback(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		database.RecordReadCoverage(r.Context(), database.ReadCoverage{
			DataSource:   database.BackendSQLite,
			Completeness: database.DataPartial,
			FromMS:       100,
			ToMS:         200,
		})
		w.WriteHeader(http.StatusOK)
	})
	handler := WithDatabaseCoverage(next)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v0/management/dashboard/summary", nil))

	if got := rr.Header().Get("X-CPAMP-Data-Source"); got != "sqlite" {
		t.Fatalf("X-CPAMP-Data-Source = %q", got)
	}
	if got := rr.Header().Get("X-CPAMP-Data-Completeness"); got != "partial" {
		t.Fatalf("X-CPAMP-Data-Completeness = %q", got)
	}
	if got := rr.Header().Get("X-CPAMP-Coverage-From-Ms"); got != "100" {
		t.Fatalf("X-CPAMP-Coverage-From-Ms = %q", got)
	}
}

func TestWithDatabaseCoverageDoesNotPredictSourceBeforeHandler(t *testing.T) {
	handler := WithDatabaseCoverage(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v0/management/dashboard/summary", nil))
	if got := rr.Header().Get("X-CPAMP-Data-Source"); got != "" {
		t.Fatalf("unexpected data source header %q", got)
	}
}

func TestWithDatabaseCoverageSkipsMutations(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		database.RecordReadCoverage(r.Context(), database.ReadCoverage{DataSource: database.BackendSQLite})
		w.WriteHeader(http.StatusNoContent)
	})
	handler := WithDatabaseCoverage(next)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", nil))
	if got := rr.Header().Get("X-CPAMP-Data-Source"); got != "" {
		t.Fatalf("unexpected data source header %q", got)
	}
}
