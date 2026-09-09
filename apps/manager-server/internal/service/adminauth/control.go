package adminauth

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// ControlService authenticates the restricted database recovery plane from
// the encrypted control-file credential copy when SQLite cannot be opened.
type ControlService struct {
	credential model.AdminCredential
}

func NewControlService(encodedCredential string) (*ControlService, error) {
	if encodedCredential == "" {
		return nil, errors.New("database control authentication copy is missing")
	}
	var credential model.AdminCredential
	if err := json.Unmarshal([]byte(encodedCredential), &credential); err != nil {
		return nil, errors.New("database control authentication copy is invalid")
	}
	if credential.Salt == "" || credential.KeyHash == "" {
		return nil, errors.New("database control authentication copy is incomplete")
	}
	return &ControlService{credential: credential}, nil
}

func (s *ControlService) VerifyHeader(_ context.Context, authorizationHeader string) (bool, error) {
	if s == nil {
		return false, errors.New("database control authentication is unavailable")
	}
	return security.VerifyAdminKey(s.credential, security.ExtractBearerToken(authorizationHeader)), nil
}

func (s *ControlService) VerifyPanelHeader(ctx context.Context, authorizationHeader string) (bool, error) {
	return s.VerifyHeader(ctx, authorizationHeader)
}

func (s *ControlService) VerifySubmittedExternalConfigHeader(ctx context.Context, authorizationHeader string, _ store.ManagerConfig) (bool, error) {
	return s.VerifyHeader(ctx, authorizationHeader)
}

func (s *ControlService) PanelUsesExternalManagementKey(context.Context) (bool, error) {
	return false, nil
}
