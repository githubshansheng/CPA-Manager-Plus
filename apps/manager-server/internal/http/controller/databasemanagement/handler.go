package databasemanagement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	databasemanagementsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/databasemanagement"
	setupsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/setup"
)

const maxRequestBytes = 2 << 20

type Handler struct {
	App *app.Context
}

func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	if h.App.DatabaseManagement == nil {
		response.Error(w, http.StatusServiceUnavailable, databasemanagementsvc.ErrUnavailable)
		return
	}
	status, err := h.App.DatabaseManagement.Status(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{
		"service":           h.App.ServiceID,
		"databaseTopology":  status.DatabaseTopology,
		"databases":         status.Databases,
		"replication":       status.Replication,
		"databaseMigration": status.DatabaseMigration,
		"cacheCoverage":     status.CacheCoverage,
		"recoveryMode":      true,
	})
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	if h.App.DatabaseManagement == nil {
		response.Error(w, http.StatusServiceUnavailable, databasemanagementsvc.ErrUnavailable)
		return
	}

	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/v0/management/databases/mysql/test":
		h.testMySQL(w, r)
	case path == "/v0/management/databases/mysql/config":
		h.saveMySQLConfig(w, r)
	case path == "/v0/management/databases/mysql/schema/reinitialize":
		h.reinitializeMySQLSchema(w, r)
	case path == "/v0/management/databases/replication/enable":
		h.withControl(w, r, http.MethodPost, h.App.DatabaseManagement.EnableReplication)
	case path == "/v0/management/databases/migrations":
		h.migrations(w, r)
	case strings.HasPrefix(path, "/v0/management/databases/migrations/"):
		h.updateMigration(w, r, path)
	case path == "/v0/management/databases/routing/cutover":
		h.cutover(w, r)
	case path == "/v0/management/databases/routing/failover":
		h.failover(w, r)
	case path == "/v0/management/databases/sqlite-cache/policy":
		h.updateCachePolicy(w, r)
	case path == "/v0/management/databases/sqlite-cache/cleanup/preview":
		h.previewCleanup(w, r)
	case path == "/v0/management/databases/sqlite-cache/cleanup":
		h.cleanupCache(w, r)
	case path == "/v0/management/databases/sqlite-cache/rebuild":
		h.rebuildCache(w, r)
	case path == "/v0/management/databases/sqlite-source/preflight":
		h.preflightSQLiteSource(w, r)
	case path == "/v0/management/databases/sqlite-source/switch":
		h.switchSQLiteSource(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) preflightSQLiteSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var input setupsvc.SQLiteSourceSwitchRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeSQLiteSourceError(w, err)
		return
	}
	if err := (databasemanagementsvc.MutationControl{
		ExpectedGeneration: input.ExpectedGeneration,
		IdempotencyKey:     input.IdempotencyKey,
	}).Validate(); err != nil {
		writeSQLiteSourceError(w, err)
		return
	}
	release, err := h.acquireSQLiteSourceSwitch(r.Context(), input.ExpectedGeneration)
	if err != nil {
		writeSQLiteSourceError(w, err)
		return
	}
	defer release()
	result, err := h.App.SetupService.PreflightSQLiteSwitch(r.Context(), input.SQLiteAdoptionRequest)
	if err != nil {
		writeSQLiteSourceError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func (h *Handler) switchSQLiteSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var input setupsvc.SQLiteSourceSwitchRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeSQLiteSourceError(w, err)
		return
	}
	if err := (databasemanagementsvc.MutationControl{
		ExpectedGeneration: input.ExpectedGeneration,
		IdempotencyKey:     input.IdempotencyKey,
	}).Validate(); err != nil {
		writeSQLiteSourceError(w, err)
		return
	}
	release, err := h.acquireSQLiteSourceSwitch(r.Context(), input.ExpectedGeneration)
	if err != nil {
		writeSQLiteSourceError(w, err)
		return
	}
	defer release()
	result, err := h.App.SetupService.SwitchSQLiteSource(r.Context(), input)
	if err != nil {
		writeSQLiteSourceError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func (h *Handler) acquireSQLiteSourceSwitch(ctx context.Context, expectedGeneration uint64) (func(), error) {
	if h.App.SQLiteSourceSwitchGuard == nil {
		return nil, databasemanagementsvc.ErrUnavailable
	}
	return h.App.SQLiteSourceSwitchGuard.AcquireSQLiteSourceSwitch(ctx, expectedGeneration)
}

func (h *Handler) migrations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit := 20
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 100 {
				writeError(w, errors.Join(databasemanagementsvc.ErrInvalidRequest,
					errors.New("limit must be between 1 and 100")))
				return
			}
			limit = parsed
		}
		history, err := h.App.DatabaseManagement.MigrationHistory(r.Context(), limit)
		if err != nil {
			writeError(w, err)
			return
		}
		response.JSON(w, http.StatusOK, history)
	case http.MethodPost:
		h.withControl(w, r, http.MethodPost, h.App.DatabaseManagement.StartMigration)
	default:
		response.MethodNotAllowed(w)
	}
}

func (h *Handler) reinitializeMySQLSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var input databasemanagementsvc.MySQLSchemaReinitializeMutation
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := input.Validate(); err != nil {
		writeError(w, err)
		return
	}
	status, err := h.App.DatabaseManagement.ReinitializeMySQLSchema(r.Context(), input)
	writeStatus(w, status, err)
}

func (h *Handler) testMySQL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var input databasemanagementsvc.MySQLConnectionInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := input.Validate(); err != nil {
		writeError(w, err)
		return
	}
	result, err := h.App.DatabaseManagement.TestMySQL(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func (h *Handler) saveMySQLConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	var input databasemanagementsvc.MySQLConfigMutation
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := input.MutationControl.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if err := input.MySQLConnectionInput.Validate(); err != nil {
		writeError(w, err)
		return
	}
	status, err := h.App.DatabaseManagement.SaveMySQLConfig(r.Context(), input)
	writeStatus(w, status, err)
}

func (h *Handler) withControl(
	w http.ResponseWriter,
	r *http.Request,
	method string,
	operation func(context.Context, databasemanagementsvc.MutationControl) (databasemanagementsvc.Status, error),
) {
	if r.Method != method {
		response.MethodNotAllowed(w)
		return
	}
	var input databasemanagementsvc.MutationControl
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := input.Validate(); err != nil {
		writeError(w, err)
		return
	}
	status, err := operation(r.Context(), input)
	writeStatus(w, status, err)
}

func (h *Handler) updateMigration(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	remainder := strings.TrimPrefix(path, "/v0/management/databases/migrations/")
	parts := strings.Split(remainder, "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	migrationID, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(migrationID) == "" {
		writeError(w, errors.Join(databasemanagementsvc.ErrInvalidRequest, errors.New("migration id is required")))
		return
	}
	action := parts[1]
	if action != "pause" && action != "resume" && action != "cancel" && action != "validate" {
		http.NotFound(w, r)
		return
	}
	var input databasemanagementsvc.MutationControl
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := input.Validate(); err != nil {
		writeError(w, err)
		return
	}
	status, err := h.App.DatabaseManagement.UpdateMigration(r.Context(), migrationID, action, input)
	writeStatus(w, status, err)
}

func (h *Handler) cutover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var input databasemanagementsvc.CutoverMutation
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := input.MutationControl.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if err := input.DangerousOperationConfirmation.Validate("mysql"); err != nil {
		writeError(w, err)
		return
	}
	status, err := h.App.DatabaseManagement.Cutover(r.Context(), input)
	writeStatus(w, status, err)
}

func (h *Handler) failover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var input databasemanagementsvc.FailoverMutation
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := input.MutationControl.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if err := input.DangerousOperationConfirmation.Validate("sqlite", "mysql"); err != nil {
		writeError(w, err)
		return
	}
	status, err := h.App.DatabaseManagement.Failover(r.Context(), input)
	writeStatus(w, status, err)
}

func (h *Handler) updateCachePolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	var input databasemanagementsvc.CachePolicyMutation
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := input.MutationControl.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if input.RetentionDays < 1 || input.RetentionDays > 3650 {
		writeError(w, errors.Join(databasemanagementsvc.ErrInvalidRequest, errors.New("retentionDays must be between 1 and 3650")))
		return
	}
	status, err := h.App.DatabaseManagement.UpdateCachePolicy(r.Context(), input)
	writeStatus(w, status, err)
}

func (h *Handler) previewCleanup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var input databasemanagementsvc.MutationControl
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := input.Validate(); err != nil {
		writeError(w, err)
		return
	}
	preview, err := h.App.DatabaseManagement.PreviewCacheCleanup(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, preview)
}

func (h *Handler) cleanupCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var input databasemanagementsvc.CacheCleanupMutation
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := input.MutationControl.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if err := input.DangerousOperationConfirmation.Validate("sqlite"); err != nil {
		writeError(w, err)
		return
	}
	status, err := h.App.DatabaseManagement.CleanupCache(r.Context(), input)
	writeStatus(w, status, err)
}

func (h *Handler) rebuildCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var input databasemanagementsvc.CacheRebuildMutation
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := input.MutationControl.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if err := input.DangerousOperationConfirmation.Validate("sqlite"); err != nil {
		writeError(w, err)
		return
	}
	if input.RetentionDays < 1 || input.RetentionDays > 3650 {
		writeError(w, errors.Join(databasemanagementsvc.ErrInvalidRequest, errors.New("retentionDays must be between 1 and 3650")))
		return
	}
	status, err := h.App.DatabaseManagement.RebuildCache(r.Context(), input)
	writeStatus(w, status, err)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.Join(databasemanagementsvc.ErrInvalidRequest, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("request body must contain exactly one JSON object")
		}
		return errors.Join(databasemanagementsvc.ErrInvalidRequest, err)
	}
	return nil
}

func writeStatus(w http.ResponseWriter, status databasemanagementsvc.Status, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, status)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, databasemanagementsvc.ErrGenerationConflict):
		status = http.StatusConflict
	case errors.Is(err, databasemanagementsvc.ErrInvalidRequest),
		errors.Is(err, databasemanagementsvc.ErrUnsafeOperation):
		status = http.StatusBadRequest
	case errors.Is(err, databasemanagementsvc.ErrUnavailable):
		status = http.StatusServiceUnavailable
	}
	response.Error(w, status, fmt.Errorf("database management: %w", err))
}

func writeSQLiteSourceError(w http.ResponseWriter, err error) {
	var adoptionErr *setupsvc.SQLiteAdoptionError
	if errors.As(err, &adoptionErr) {
		code, details := setupsvc.SQLiteAdoptionErrorDetails(err)
		payload := map[string]any{"error": err.Error(), "code": code}
		if len(details) > 0 {
			payload["details"] = details
		}
		response.JSON(w, setupsvc.SQLiteAdoptionErrorStatus(err), payload)
		return
	}
	if errors.Is(err, databasemanagementsvc.ErrSQLiteSourceUnsafe) {
		response.JSON(w, http.StatusConflict, map[string]any{
			"error": err.Error(),
			"code":  setupsvc.SQLiteAdoptionCodeTopologyUnsafe,
			"details": map[string]any{
				"stage": "database topology",
				"cause": err.Error(),
			},
		})
		return
	}
	writeError(w, err)
}
