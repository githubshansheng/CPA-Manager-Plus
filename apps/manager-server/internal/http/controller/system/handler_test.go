package system

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type restartTestAuth struct{ allow bool }

func (a restartTestAuth) VerifyHeader(context.Context, string) (bool, error) { return a.allow, nil }
func (a restartTestAuth) VerifyPanelHeader(context.Context, string) (bool, error) {
	return a.allow, nil
}
func (restartTestAuth) VerifySubmittedExternalConfigHeader(context.Context, string, store.ManagerConfig) (bool, error) {
	return false, nil
}
func (restartTestAuth) PanelUsesExternalManagementKey(context.Context) (bool, error) {
	return false, nil
}

type restartTestRequester struct{ requested bool }

func (r *restartTestRequester) RequestRestart() bool {
	if r.requested {
		return false
	}
	r.requested = true
	return true
}

func TestRestartRequiresPanelAuthAndAcceptsOnlyOneRequest(t *testing.T) {
	requester := &restartTestRequester{}
	handler := &Handler{App: &app.Context{
		StartedAt:        1234,
		AdminAuthService: restartTestAuth{allow: true},
		RestartRequester: requester,
	}}

	first := httptest.NewRecorder()
	handler.Restart(first, httptest.NewRequest(http.MethodPost, "/v0/management/system/restart", nil))
	if first.Code != http.StatusAccepted || !requester.requested {
		t.Fatalf("first restart status=%d requested=%t body=%s", first.Code, requester.requested, first.Body.String())
	}
	if body := first.Body.String(); body == "" || !containsAll(body, `"restarting":true`, `"startedAt":1234`) {
		t.Fatalf("first restart body=%s", body)
	}

	second := httptest.NewRecorder()
	handler.Restart(second, httptest.NewRequest(http.MethodPost, "/v0/management/system/restart", nil))
	if second.Code != http.StatusConflict {
		t.Fatalf("second restart status=%d body=%s", second.Code, second.Body.String())
	}

	unauthorizedHandler := &Handler{App: &app.Context{
		AdminAuthService: restartTestAuth{}, RestartRequester: &restartTestRequester{},
	}}
	unauthorized := httptest.NewRecorder()
	unauthorizedHandler.Restart(unauthorized, httptest.NewRequest(http.MethodPost, "/v0/management/system/restart", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized restart status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
}

func containsAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}
