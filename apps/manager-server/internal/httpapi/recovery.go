package httpapi

import (
	"net/http"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	databasemanagementcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/databasemanagement"
	healthcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/health"
	panelcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/panel"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	databasemanagementsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/databasemanagement"
	panelsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/panel"
)

// NewDatabaseRecovery exposes only authentication, system/database status and
// explicit database management operations. Business APIs stay unavailable so
// a broken SQLite cache cannot be mistaken for a healthy normal runtime.
func NewDatabaseRecovery(
	cfg config.Config,
	auth app.AdminAuthenticationService,
	manager databasemanagementsvc.Manager,
) *Server {
	appCtx := &app.Context{
		Config: cfg, StartedAt: time.Now().UnixMilli(), ServiceID: serviceID,
		AdminAuthService: auth, DatabaseManagement: manager,
		PanelService: panelsvc.New(cfg.PanelPath, embeddedPanel),
	}
	databaseHandler := &databasemanagementcontroller.Handler{App: appCtx}
	healthHandler := &healthcontroller.Handler{ServiceID: serviceID + "-database-recovery"}
	panelHandler := &panelcontroller.Handler{App: appCtx}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", middleware.WithCORS(cfg, healthHandler.Health))
	mux.HandleFunc("/status", middleware.WithCORS(cfg, databaseHandler.Status))
	mux.HandleFunc("/management.html", panelHandler.ManagementHTML)
	mux.HandleFunc("/v0/management/databases/", middleware.WithCORS(cfg, databaseHandler.Handle))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/management.html", http.StatusTemporaryRedirect)
			return
		}
		if r.Method == http.MethodOptions {
			middleware.WriteCORS(cfg, w, r)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		response.Error(w, http.StatusServiceUnavailable, databasemanagementsvc.ErrUnavailable)
	})
	withCoverage := middleware.WithDatabaseCoverage(mux)
	return &Server{handler: middleware.Recovery(middleware.RequestLogger(withCoverage)), appCtx: appCtx}
}
