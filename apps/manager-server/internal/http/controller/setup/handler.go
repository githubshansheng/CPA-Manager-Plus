package setup

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	setupsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/setup"
)

type Handler struct {
	App *app.Context
}

const sqliteAdoptionRequestLimit = 64 * 1024

func (h *Handler) Setup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !middleware.AuthorizeAdmin(w, r, h.App.AdminAuthService) {
		return
	}
	var req setupsvc.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.App.SetupService.Setup(r.Context(), req, r.Header.Get("Authorization"))
	if err != nil {
		response.Error(w, response.SetupErrorStatus(err), err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func (h *Handler) PreflightSQLiteAdoption(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !middleware.AuthorizeAdmin(w, r, h.App.AdminAuthService) {
		return
	}
	var req setupsvc.SQLiteAdoptionRequest
	if err := decodeSQLiteAdoptionRequest(w, r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.App.SetupService.PreflightSQLiteAdoption(r.Context(), req)
	if err != nil {
		writeSQLiteAdoptionError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func (h *Handler) AdoptSQLite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !middleware.AuthorizeAdmin(w, r, h.App.AdminAuthService) {
		return
	}
	var req setupsvc.SQLiteAdoptionRequest
	if err := decodeSQLiteAdoptionRequest(w, r, &req); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.App.SetupService.AdoptSQLite(r.Context(), req)
	if err != nil {
		writeSQLiteAdoptionError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func decodeSQLiteAdoptionRequest(w http.ResponseWriter, r *http.Request, target *setupsvc.SQLiteAdoptionRequest) error {
	r.Body = http.MaxBytesReader(w, r.Body, sqliteAdoptionRequestLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON object")
		}
		return err
	}
	return nil
}

func writeSQLiteAdoptionError(w http.ResponseWriter, err error) {
	code, details := setupsvc.SQLiteAdoptionErrorDetails(err)
	payload := map[string]any{
		"error": err.Error(),
		"code":  code,
	}
	if len(details) > 0 {
		payload["details"] = details
	}
	response.JSON(w, setupsvc.SQLiteAdoptionErrorStatus(err), payload)
}
