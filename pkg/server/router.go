package server

import (
	"log/slog"
	"net/http"

	"a2a-proxy/pkg/config"
	"a2a-proxy/pkg/dispatcher"
	"a2a-proxy/pkg/metrics"
)

// RouterOption allows customizing router behavior.
type RouterOption func(*routerConfig)

type routerConfig struct {
	metricsRegistry *metrics.Registry
	logger          *slog.Logger
}

// WithMetricsRegistry provides a custom metrics registry.
func WithMetricsRegistry(reg *metrics.Registry) RouterOption {
	return func(rc *routerConfig) {
		rc.metricsRegistry = reg
	}
}

// WithLogger provides a custom slog.Logger.
func WithLogger(logger *slog.Logger) RouterOption {
	return func(rc *routerConfig) {
		rc.logger = logger
	}
}

// SetupRouter initializes routes and middleware using Go 1.22+ http.NewServeMux.
// Middleware order: Recovery -> RequestID -> Metrics -> Logging -> Auth -> Handler.
func SetupRouter(cfg *config.Config, disp *dispatcher.Dispatcher, opts ...RouterOption) http.Handler {
	rc := &routerConfig{
		metricsRegistry: metrics.DefaultRegistry,
		logger:          slog.Default(),
	}
	for _, opt := range opts {
		opt(rc)
	}

	if disp != nil && rc.metricsRegistry != nil {
		disp.SetMetricsRegistry(rc.metricsRegistry)
	}

	mux := http.NewServeMux()

	a2aH := NewA2AHandler(cfg, disp, rc.metricsRegistry)
	restH := NewRESTHandler(cfg, disp, a2aH, rc.metricsRegistry)
	healthH := NewHealthHandler(cfg, disp)

	// Standard A2A Routes
	mux.HandleFunc("GET /a2a/v1/agents", a2aH.ListAgents)
	mux.HandleFunc("GET /a2a/v1/agents/{id}", a2aH.GetAgent)
	mux.HandleFunc("POST /a2a/v1/tasks", a2aH.DispatchTask)
	mux.HandleFunc("POST /a2a/v1/tasks/stream", a2aH.DispatchTaskStream)
	mux.HandleFunc("GET /a2a/v1/tasks/{task_id}", a2aH.GetTask)

	// Unified REST Routes
	mux.HandleFunc("POST /api/v1/invoke/{agent_id}", restH.InvokeAgent)
	mux.HandleFunc("POST /api/v1/stream/{agent_id}", restH.StreamAgent)

	// Health & Metrics Routes
	mux.HandleFunc("GET /healthz", healthH.Healthz)
	mux.HandleFunc("GET /readyz", healthH.Readyz)
	mux.Handle("GET /metrics", rc.metricsRegistry.Handler())

	// Build middleware chain: Recovery -> RequestID -> Metrics -> Logging -> Auth -> Handler
	var securityCfg *config.SecurityConfig
	if cfg != nil {
		securityCfg = &cfg.Security
	}

	var handler http.Handler = mux
	handler = AuthMiddleware(securityCfg)(handler)
	handler = LoggingMiddlewareWithLogger(rc.logger)(handler)
	handler = MetricsMiddleware(rc.metricsRegistry)(handler)
	handler = RequestIDMiddleware(handler)
	handler = RecoveryMiddleware(handler)

	return handler
}
