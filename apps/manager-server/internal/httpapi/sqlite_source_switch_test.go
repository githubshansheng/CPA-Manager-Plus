package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	databasemanagementsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/databasemanagement"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

type sqliteSwitchTestManager struct {
	databasemanagementsvc.Manager
}

type sqliteSwitchTestGuard struct {
	err        error
	generation uint64
	calls      int
	releases   int
}

func (g *sqliteSwitchTestGuard) AcquireSQLiteSourceSwitch(
	_ context.Context,
	generation uint64,
) (func(), error) {
	g.calls++
	g.generation = generation
	if g.err != nil {
		return nil, g.err
	}
	return func() { g.releases++ }, nil
}

type sqliteSwitchRestartRequester struct {
	accepted bool
	calls    int
}

func (r *sqliteSwitchRestartRequester) RequestRestart() bool {
	r.calls++
	if r.accepted {
		return false
	}
	r.accepted = true
	return true
}

func newSQLiteSwitchTestServer(t *testing.T) (*Server, config.Config) {
	t.Helper()
	dataDir := t.TempDir()
	cfg := config.Config{
		DataDir: dataDir,
		DBPath:  filepath.Join(dataDir, "usage.sqlite"),
		Queue:   "usage", PopSide: "right", CORSOrigins: []string{"*"},
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open current store: %v", err)
	}
	testutil.EnsureAdminCredential(t, db)
	if err := db.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: "http://cpa.example.test",
		ManagementKey:  "management-key",
		Queue:          "usage",
		PopSide:        "right",
	}); err != nil {
		_ = db.Close()
		t.Fatalf("save current setup: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	manager := collector.NewManager(cfg, db)
	server := New(cfg, db, manager)
	server.AppContext().DatabaseManagement = &sqliteSwitchTestManager{}
	return server, cfg
}

func createSQLiteSwitchSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open source store: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close source store: %v", err)
	}
	return path
}

func TestSQLiteSourceSwitchRouteRequiresAuthentication(t *testing.T) {
	server, _ := newSQLiteSwitchTestServer(t)
	server.AppContext().SQLiteSourceSwitchGuard = &sqliteSwitchTestGuard{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v0/management/databases/sqlite-source/preflight",
		bytes.NewBufferString(`{"expectedGeneration":7,"idempotencyKey":"preflight-7"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()

	server.Handler().ServeHTTP(result, request)

	if result.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
	}
}

func TestSQLiteSourceSwitchRouteReturnsTopologyAndGenerationConflicts(t *testing.T) {
	for _, test := range []struct {
		name     string
		guardErr error
		code     string
	}{
		{
			name:     "unsafe topology",
			guardErr: errors.Join(databasemanagementsvc.ErrSQLiteSourceUnsafe, errors.New("mysql is configured")),
			code:     "sqlite_source_topology_unsafe",
		},
		{
			name:     "stale generation",
			guardErr: databasemanagementsvc.ErrGenerationConflict,
			code:     "database_generation_conflict",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newSQLiteSwitchTestServer(t)
			server.AppContext().SQLiteSourceSwitchGuard = &sqliteSwitchTestGuard{err: test.guardErr}
			request := httptest.NewRequest(
				http.MethodPost,
				"/v0/management/databases/sqlite-source/preflight",
				bytes.NewBufferString(`{
					"sourcePath":"/not-inspected.sqlite",
					"expectedGeneration":7,
					"idempotencyKey":"preflight-7"
				}`),
			)
			request.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
			request.Header.Set("Content-Type", "application/json")
			result := httptest.NewRecorder()

			server.Handler().ServeHTTP(result, request)

			if result.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s", result.Code, result.Body.String())
			}
			var payload struct {
				Code    string         `json:"code"`
				Details map[string]any `json:"details"`
			}
			if err := json.Unmarshal(result.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Code != test.code {
				t.Fatalf("payload=%#v body=%s", payload, result.Body.String())
			}
			if test.code == "sqlite_source_topology_unsafe" && len(payload.Details) == 0 {
				t.Fatalf("topology conflict omitted details: %s", result.Body.String())
			}
		})
	}
}

func TestSQLiteSourceSwitchRouteRequiresStopConfirmationAndPersistsPendingSource(t *testing.T) {
	server, cfg := newSQLiteSwitchTestServer(t)
	guard := &sqliteSwitchTestGuard{}
	server.AppContext().SQLiteSourceSwitchGuard = guard
	sourcePath := createSQLiteSwitchSource(t)

	unconfirmed := httptest.NewRequest(
		http.MethodPost,
		"/v0/management/databases/sqlite-source/switch",
		bytes.NewBufferString(`{
			"sourcePath":"`+filepath.ToSlash(sourcePath)+`",
			"expectedGeneration":7,
			"idempotencyKey":"switch-7"
		}`),
	)
	unconfirmed.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
	unconfirmed.Header.Set("Content-Type", "application/json")
	unconfirmedResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(unconfirmedResult, unconfirmed)
	assertSQLiteAdoptionError(
		t,
		unconfirmedResult,
		http.StatusBadRequest,
		"sqlite_source_confirmation_required",
	)

	confirmed := httptest.NewRequest(
		http.MethodPost,
		"/v0/management/databases/sqlite-source/switch",
		bytes.NewBufferString(`{
			"sourcePath":"`+filepath.ToSlash(sourcePath)+`",
			"confirmSourceStopped":true,
			"expectedGeneration":7,
			"idempotencyKey":"switch-7-confirmed"
		}`),
	)
	confirmed.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
	confirmed.Header.Set("Content-Type", "application/json")
	confirmedResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(confirmedResult, confirmed)
	if confirmedResult.Code != http.StatusOK {
		t.Fatalf("confirmed status=%d body=%s", confirmedResult.Code, confirmedResult.Body.String())
	}
	var response struct {
		OK              bool   `json:"ok"`
		SourcePath      string `json:"sourcePath"`
		RestartRequired bool   `json:"restartRequired"`
		SelectionState  string `json:"selectionState"`
	}
	if err := json.Unmarshal(confirmedResult.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || !response.RestartRequired || response.SelectionState != config.SQLiteSourceStatePending {
		t.Fatalf("response=%#v", response)
	}
	selection, ok, err := config.LoadSQLiteSourceSelection(cfg.DataDir)
	if err != nil || !ok || selection.DatabasePath != response.SourcePath ||
		selection.Operation != config.SQLiteSourceOperationSwitch {
		t.Fatalf("selection=%#v ok=%v error=%v", selection, ok, err)
	}
	if guard.calls != 2 || guard.releases != 2 || guard.generation != 7 {
		t.Fatalf("guard calls=%d releases=%d generation=%d", guard.calls, guard.releases, guard.generation)
	}
}

func TestSystemRestartRouteIsAuthenticatedAndReachable(t *testing.T) {
	server, _ := newSQLiteSwitchTestServer(t)
	restart := &sqliteSwitchRestartRequester{}
	server.AppContext().RestartRequester = restart

	unauthorized := httptest.NewRequest(http.MethodPost, "/v0/management/system/restart", nil)
	unauthorizedResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(unauthorizedResult, unauthorized)
	if unauthorizedResult.Code != http.StatusUnauthorized || restart.calls != 0 {
		t.Fatalf("unauthorized status=%d calls=%d", unauthorizedResult.Code, restart.calls)
	}

	authorized := httptest.NewRequest(http.MethodPost, "/v0/management/system/restart", nil)
	authorized.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
	authorizedResult := httptest.NewRecorder()
	server.Handler().ServeHTTP(authorizedResult, authorized)
	if authorizedResult.Code != http.StatusAccepted || restart.calls != 1 {
		t.Fatalf("authorized status=%d calls=%d body=%s", authorizedResult.Code, restart.calls, authorizedResult.Body.String())
	}
}
