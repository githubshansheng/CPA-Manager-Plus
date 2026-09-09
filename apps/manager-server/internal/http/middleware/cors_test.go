package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
)

func TestWriteCORSAllowsPatch(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/usage-service/account-processing-policy", nil)
	WriteCORS(config.Config{CORSOrigins: []string{"*"}}, rr, req)

	methods := rr.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(methods, "PATCH") {
		t.Fatalf("Access-Control-Allow-Methods = %q, want PATCH", methods)
	}
}

func TestWriteCORSExposesDatabaseCoverageHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://localhost/status", nil)
	rr := httptest.NewRecorder()

	WriteCORS(config.Config{CORSOrigins: []string{"*"}}, rr, req)

	got := rr.Header().Get("Access-Control-Expose-Headers")
	for _, name := range []string{
		"X-CPAMP-Data-Source",
		"X-CPAMP-Data-Completeness",
		"X-CPAMP-Coverage-From-Ms",
		"X-CPAMP-Coverage-To-Ms",
	} {
		if !strings.Contains(got, name) {
			t.Fatalf("Access-Control-Expose-Headers = %q, want %q", got, name)
		}
	}
}
