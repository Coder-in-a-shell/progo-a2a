package server

import (
	"encoding/json"
	"net/http"
	"time"

	"a2a-proxy/pkg/config"
	"a2a-proxy/pkg/dispatcher"
)

// HealthHandler handles health check endpoints (/healthz and /readyz).
type HealthHandler struct {
	cfg  *config.Config
	disp *dispatcher.Dispatcher
}

// NewHealthHandler creates a new HealthHandler.
func NewHealthHandler(cfg *config.Config, disp *dispatcher.Dispatcher) *HealthHandler {
	return &HealthHandler{
		cfg:  cfg,
		disp: disp,
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

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "ready",
		"adapters": adapterCount,
		"time":     time.Now().UTC().Format(time.RFC3339),
	})
}
