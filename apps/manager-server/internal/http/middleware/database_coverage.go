package middleware

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database"
)

// WithDatabaseCoverage emits metadata recorded by the routed reads that
// actually contributed to this response. This keeps legacy SQLite-only
// responses byte-compatible and cannot falsely claim a fallback before the
// handler has run.
func WithDatabaseCoverage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isBusinessRead(r) {
			next.ServeHTTP(w, r)
			return
		}
		ctx, recorder := database.WithReadCoverageRecorder(r.Context())
		wrapped := &coverageResponseWriter{ResponseWriter: w, recorder: recorder}
		next.ServeHTTP(wrapped, r.WithContext(ctx))
		if !wrapped.wroteHeader {
			wrapped.inject()
		}
	})
}

type coverageResponseWriter struct {
	http.ResponseWriter
	recorder    *database.ReadCoverageRecorder
	wroteHeader bool
}

func (w *coverageResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.inject()
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *coverageResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *coverageResponseWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *coverageResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (w *coverageResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *coverageResponseWriter) inject() {
	coverage := w.recorder.Snapshot()
	if coverage.DataSource != "" {
		w.Header().Set("X-CPAMP-Data-Source", string(coverage.DataSource))
	}
	if completeness := strings.TrimSpace(coverage.Completeness); completeness != "" {
		w.Header().Set("X-CPAMP-Data-Completeness", completeness)
	}
	if coverage.FromMS > 0 {
		w.Header().Set("X-CPAMP-Coverage-From-Ms", strconv.FormatInt(coverage.FromMS, 10))
	}
	if coverage.ToMS > 0 {
		w.Header().Set("X-CPAMP-Coverage-To-Ms", strconv.FormatInt(coverage.ToMS, 10))
	}
}

func isBusinessRead(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	path := strings.TrimRight(r.URL.Path, "/")
	return path == "/status" ||
		path == "/v0/management/usage" ||
		strings.HasPrefix(path, "/v0/management/usage/") ||
		strings.HasPrefix(path, "/v0/management/dashboard/") ||
		strings.HasPrefix(path, "/v0/management/monitoring/") ||
		path == "/v0/management/quota-snapshots" ||
		path == "/v0/management/quota-snapshots/query"
}
