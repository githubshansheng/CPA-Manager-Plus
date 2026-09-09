package system

import (
	"errors"
	"log"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
)

type Handler struct {
	App *app.Context
}

type dataMigrationStatus struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	LastEventID   int64  `json:"lastEventId"`
	TargetEventID int64  `json:"targetEventId"`
	ProcessedRows int64  `json:"processedRows"`
	ChangedRows   int64  `json:"changedRows"`
	AppliedRows   int64  `json:"appliedRows"`
	StartedAtMS   int64  `json:"startedAtMs,omitempty"`
	UpdatedAtMS   int64  `json:"updatedAtMs"`
	FinishedAtMS  int64  `json:"finishedAtMs,omitempty"`
}

func (h *Handler) Info(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	info, err := h.App.SetupService.Info(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	response.JSON(w, http.StatusOK, info)
}

func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	databaseMaintenance, err := h.App.Store.DerivedMaintenanceStatus(r.Context())
	if err != nil {
		log.Printf("read database maintenance status: %v", err)
		response.Error(w, http.StatusInternalServerError, errors.New("database maintenance status unavailable"))
		return
	}
	if r.URL.Query().Get("scope") == "database-maintenance" {
		response.JSON(w, http.StatusOK, map[string]any{
			"databaseMaintenance": databaseMaintenance,
		})
		return
	}
	events, deadLetters, err := h.App.UsageService.Counts(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	status := h.App.CollectorService.Status()
	status.DeadLetters = deadLetters
	migration, err := h.App.Store.UsageCacheAccountingMigrationState(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	payload := map[string]any{
		"service":     h.App.ServiceID,
		"dbPath":      h.App.Config.DBPath,
		"events":      events,
		"deadLetters": deadLetters,
		"collector":   status,
		"dataMigration": dataMigrationStatus{
			Name:          migration.Name,
			Status:        migration.Status,
			LastEventID:   migration.LastEventID,
			TargetEventID: migration.TargetEventID,
			ProcessedRows: migration.ProcessedRows,
			ChangedRows:   migration.ChangedRows,
			AppliedRows:   migration.AppliedRows,
			StartedAtMS:   migration.StartedAtMS,
			UpdatedAtMS:   migration.UpdatedAtMS,
			FinishedAtMS:  migration.FinishedAtMS,
		},
		"databaseMaintenance": databaseMaintenance,
	}
	if sqliteSource, sourceErr := h.App.SetupService.SQLiteSourceStatus(); sourceErr != nil {
		log.Printf("read SQLite source selection status: %v", sourceErr)
		sqliteSource.LastError = sourceErr.Error()
		payload["sqliteSource"] = sqliteSource
	} else {
		payload["sqliteSource"] = sqliteSource
	}
	if h.App.DatabaseMaintenance != nil {
		payload["database"] = h.App.DatabaseMaintenance.Snapshot()
	}
	if h.App.DatabaseManagement != nil {
		databaseStatus, statusErr := h.App.DatabaseManagement.Status(r.Context())
		if statusErr != nil {
			log.Printf("read database topology status: %v", statusErr)
		} else {
			payload["databaseTopology"] = databaseStatus.DatabaseTopology
			payload["databases"] = databaseStatus.Databases
			payload["replication"] = databaseStatus.Replication
			payload["databaseMigration"] = databaseStatus.DatabaseMigration
			payload["cacheCoverage"] = databaseStatus.CacheCoverage
		}
	}
	response.JSON(w, http.StatusOK, payload)
}

func (h *Handler) Restart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	if h.App.RestartRequester == nil {
		response.JSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "in-process restart is unavailable for this Manager Server runtime",
			"code":  "system_restart_unavailable",
		})
		return
	}
	if !h.App.RestartRequester.RequestRestart() {
		response.JSON(w, http.StatusConflict, map[string]any{
			"error": "a Manager Server restart has already been requested",
			"code":  "system_restart_already_requested",
		})
		return
	}
	response.JSON(w, http.StatusAccepted, map[string]any{
		"ok":         true,
		"restarting": true,
		"startedAt":  h.App.StartedAt,
	})
}
