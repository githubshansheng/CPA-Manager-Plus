package adminauth

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

func TestControlServiceVerifiesEncryptedControlCredentialCopy(t *testing.T) {
	credential, err := security.NewAdminCredential("cpamp_recovery_test", "test")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewControlService(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := service.VerifyPanelHeader(context.Background(), "Bearer cpamp_recovery_test")
	if err != nil || !ok {
		t.Fatalf("VerifyPanelHeader() ok=%v err=%v", ok, err)
	}
	ok, err = service.VerifyPanelHeader(context.Background(), "Bearer wrong")
	if err != nil || ok {
		t.Fatalf("VerifyPanelHeader(wrong) ok=%v err=%v", ok, err)
	}
}
