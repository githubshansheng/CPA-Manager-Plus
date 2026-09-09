package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

func TestManagerConfigCustomPagesRoundTripAndValidation(t *testing.T) {
	handler, db := newCompatHandler(t, testutil.NewConfig(t), nil)

	updateBody := `{"config":{"collector":{"enabled":false},"customPages":[{"id":" status ","title":" Status board ","url":" https://status.example.test/overview "}]}}`
	updateRR := testutil.Request(
		t,
		handler,
		http.MethodPut,
		"/usage-service/config",
		updateBody,
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, updateRR, http.StatusOK)
	if !strings.Contains(updateRR.Body.String(), `"customPages":[{"id":"status","title":"Status board","url":"https://status.example.test/overview"}]`) {
		t.Fatalf("updated config body = %s", updateRR.Body.String())
	}

	persisted, ok, err := db.LoadManagerConfig(context.Background())
	if err != nil || !ok {
		t.Fatalf("load manager config ok=%v err=%v", ok, err)
	}
	if len(persisted.CustomPages) != 1 || persisted.CustomPages[0].ID != "status" {
		t.Fatalf("persisted custom pages = %#v", persisted.CustomPages)
	}

	getRR := testutil.Request(t, handler, http.MethodGet, "/usage-service/config", "", testutil.AdminKey)
	testutil.RequireStatus(t, getRR, http.StatusOK)
	if !strings.Contains(getRR.Body.String(), `"customPages":[{"id":"status"`) {
		t.Fatalf("get config body = %s", getRR.Body.String())
	}

	invalidBody := `{"config":{"collector":{"enabled":false},"customPages":[{"id":"unsafe","title":"Unsafe","url":"javascript:alert(1)"}]}}`
	invalidRR := testutil.Request(
		t,
		handler,
		http.MethodPut,
		"/usage-service/config",
		invalidBody,
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, invalidRR, http.StatusBadRequest)
	if !strings.Contains(invalidRR.Body.String(), `"code":"invalid_custom_page"`) {
		t.Fatalf("invalid config body = %s", invalidRR.Body.String())
	}
}
