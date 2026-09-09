package databasemanagement

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	dmsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/databasemanagement"
)

type confirmationManager struct {
	dmsvc.Manager
	cleanupCalls      int
	reinitializeCalls int
	historyLimit      int
}

func (m *confirmationManager) MigrationHistory(
	_ context.Context,
	limit int,
) (dmsvc.MigrationHistoryResponse, error) {
	m.historyLimit = limit
	return dmsvc.MigrationHistoryResponse{
		ActiveMigrationID: "migration-current",
		Migrations: []dmsvc.MigrationHistoryRecord{{
			ID: "migration-failed", Status: "failed", LastError: "visible migration error",
		}},
	}, nil
}

func (m *confirmationManager) ReinitializeMySQLSchema(
	_ context.Context,
	_ dmsvc.MySQLSchemaReinitializeMutation,
) (dmsvc.Status, error) {
	m.reinitializeCalls++
	return dmsvc.Status{}, nil
}

func (m *confirmationManager) CleanupCache(
	_ context.Context,
	_ dmsvc.CacheCleanupMutation,
) (dmsvc.Status, error) {
	m.cleanupCalls++
	return dmsvc.Status{}, nil
}

func TestReinitializeMySQLSchemaRequiresExactExplicitConfirmation(t *testing.T) {
	manager := &confirmationManager{}
	handler := &Handler{App: &app.Context{DatabaseManagement: manager}}

	for name, body := range map[string]string{
		"missing drop":     `{"expectedGeneration":7,"idempotencyKey":"schema-7","target":"mysql","confirmDatabase":"cpamp"}`,
		"wrong target":     `{"expectedGeneration":7,"idempotencyKey":"schema-7","target":"sqlite","confirmDatabase":"cpamp","confirmDrop":true}`,
		"missing database": `{"expectedGeneration":7,"idempotencyKey":"schema-7","target":"mysql","confirmDrop":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost,
				"/v0/management/databases/mysql/schema/reinitialize", strings.NewReader(body))
			responseRecorder := httptest.NewRecorder()
			handler.reinitializeMySQLSchema(responseRecorder, request)
			if responseRecorder.Code != http.StatusBadRequest || manager.reinitializeCalls != 0 {
				t.Fatalf("response=%d calls=%d body=%s", responseRecorder.Code,
					manager.reinitializeCalls, responseRecorder.Body.String())
			}
		})
	}

	confirmed := httptest.NewRequest(http.MethodPost,
		"/v0/management/databases/mysql/schema/reinitialize", strings.NewReader(`{
			"expectedGeneration":7,
			"idempotencyKey":"schema-7-confirmed",
			"target":"mysql",
			"confirmDatabase":"cpamp",
			"confirmDrop":true
		}`))
	confirmedResponse := httptest.NewRecorder()
	handler.reinitializeMySQLSchema(confirmedResponse, confirmed)
	if confirmedResponse.Code != http.StatusOK || manager.reinitializeCalls != 1 {
		t.Fatalf("confirmed response=%d calls=%d body=%s", confirmedResponse.Code,
			manager.reinitializeCalls, confirmedResponse.Body.String())
	}
}

func TestCleanupCacheRequiresExplicitTargetMigrationAndValidationToken(t *testing.T) {
	manager := &confirmationManager{}
	handler := &Handler{App: &app.Context{DatabaseManagement: manager}}

	missing := httptest.NewRequest(http.MethodPost,
		"/v0/management/databases/sqlite-cache/cleanup",
		strings.NewReader(`{"expectedGeneration":7,"idempotencyKey":"cleanup-7"}`))
	missing.Header.Set("Content-Type", "application/json")
	missingResponse := httptest.NewRecorder()
	handler.cleanupCache(missingResponse, missing)
	if missingResponse.Code != http.StatusBadRequest || manager.cleanupCalls != 0 {
		t.Fatalf("missing confirmation response=%d calls=%d body=%s",
			missingResponse.Code, manager.cleanupCalls, missingResponse.Body.String())
	}

	confirmed := httptest.NewRequest(http.MethodPost,
		"/v0/management/databases/sqlite-cache/cleanup", strings.NewReader(`{
			"expectedGeneration":7,
			"idempotencyKey":"cleanup-7-confirmed",
			"target":"sqlite",
			"migrationId":"migration-7",
			"validationToken":"validation-7"
		}`))
	confirmed.Header.Set("Content-Type", "application/json")
	confirmedResponse := httptest.NewRecorder()
	handler.cleanupCache(confirmedResponse, confirmed)
	if confirmedResponse.Code != http.StatusOK || manager.cleanupCalls != 1 {
		t.Fatalf("confirmed response=%d calls=%d body=%s",
			confirmedResponse.Code, manager.cleanupCalls, confirmedResponse.Body.String())
	}
}

func TestMigrationHistoryReturnsBoundedRecordsAndErrors(t *testing.T) {
	manager := &confirmationManager{}
	handler := &Handler{App: &app.Context{DatabaseManagement: manager}}
	request := httptest.NewRequest(http.MethodGet,
		"/v0/management/databases/migrations?limit=12", nil)
	responseRecorder := httptest.NewRecorder()

	handler.migrations(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK || manager.historyLimit != 12 {
		t.Fatalf("response=%d historyLimit=%d body=%s", responseRecorder.Code,
			manager.historyLimit, responseRecorder.Body.String())
	}
	if body := responseRecorder.Body.String(); !strings.Contains(body, "migration-failed") ||
		!strings.Contains(body, "visible migration error") {
		t.Fatalf("migration history hid task error: %s", body)
	}

	invalid := httptest.NewRequest(http.MethodGet,
		"/v0/management/databases/migrations?limit=101", nil)
	invalidResponse := httptest.NewRecorder()
	handler.migrations(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit response=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}
}
