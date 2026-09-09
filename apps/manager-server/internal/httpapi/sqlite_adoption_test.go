package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

func TestSQLiteAdoptionEndpointsRequireAdminAndReturnStructuredErrors(t *testing.T) {
	dataDir := t.TempDir()
	handler := newTestHandlerWithConfig(t, config.Config{
		DataDir: dataDir,
		DBPath:  filepath.Join(dataDir, "usage.sqlite"),
		Queue:   "usage", PopSide: "right", CORSOrigins: []string{"*"},
	})

	unauthorized := httptest.NewRequest(http.MethodPost, "/setup/sqlite-source/preflight", bytes.NewBufferString(`{}`))
	unauthorized.Header.Set("Content-Type", "application/json")
	unauthorizedResult := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedResult, unauthorized)
	if unauthorizedResult.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorizedResult.Code, unauthorizedResult.Body.String())
	}

	preflight := httptest.NewRequest(http.MethodPost, "/setup/sqlite-source/preflight", bytes.NewBufferString(`{}`))
	preflight.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
	preflight.Header.Set("Content-Type", "application/json")
	preflightResult := httptest.NewRecorder()
	handler.ServeHTTP(preflightResult, preflight)
	assertSQLiteAdoptionError(t, preflightResult, http.StatusBadRequest, "sqlite_source_path_required")

	adopt := httptest.NewRequest(http.MethodPost, "/setup/sqlite-source/adopt", bytes.NewBufferString(
		`{"sourcePath":"`+filepath.ToSlash(filepath.Join(t.TempDir(), "legacy.sqlite"))+`"}`,
	))
	adopt.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
	adopt.Header.Set("Content-Type", "application/json")
	adoptResult := httptest.NewRecorder()
	handler.ServeHTTP(adoptResult, adopt)
	assertSQLiteAdoptionError(t, adoptResult, http.StatusBadRequest, "sqlite_source_confirmation_required")
}

func assertSQLiteAdoptionError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Error   string         `json:"error"`
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != code || payload.Error == "" || len(payload.Details) == 0 {
		t.Fatalf("payload=%#v", payload)
	}
}
