package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/dispatcher"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/storage"
)

// HealthHandler handles health check endpoints (/healthz and /readyz).
type HealthHandler struct {
	cfg     *config.Config
	disp    *dispatcher.Dispatcher
	checker storage.HealthChecker
	logger  *slog.Logger
}

// NewHealthHandler creates a new HealthHandler.
func NewHealthHandler(cfg *config.Config, disp *dispatcher.Dispatcher) *HealthHandler {
	return &HealthHandler{
		cfg:    cfg,
		disp:   disp,
		logger: slog.Default(),
	}
}

// SetHealthChecker sets the optional storage health checker for readiness checks.
func (h *HealthHandler) SetHealthChecker(checker storage.HealthChecker) {
	h.checker = checker
}

// SetLogger sets the structured logger for the health handler.
func (h *HealthHandler) SetLogger(logger *slog.Logger) {
	if logger != nil {
		h.logger = logger
	}
}

// Healthz handles GET /healthz (liveness probe).
func (h *HealthHandler) Healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "healthy",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

// Readyz handles GET /readyz (readiness probe).
// Checks that config is loaded and at least one adapter is registered.
// When a storage HealthChecker is configured, it verifies database connectivity with a 2-second timeout.
func (h *HealthHandler) Readyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	adapterCount := 0
	if h.disp != nil && h.disp.Registry() != nil {
		adapterCount = h.disp.Registry().Count()
	}

	if h.cfg == nil || adapterCount == 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":   "not ready",
			"error":    "no adapters registered or configuration missing",
			"adapters": adapterCount,
			"time":     time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	if h.checker != nil {
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := h.checker.Ping(pingCtx); err != nil {
			logger := h.logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.Error("storage readiness ping failed", "error", err)

			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":   "not ready",
				"error":    "storage unavailable",
				"adapters": adapterCount,
				"time":     time.Now().UTC().Format(time.RFC3339),
			})
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "ready",
		"adapters": adapterCount,
		"time":     time.Now().UTC().Format(time.RFC3339),
	})
}
