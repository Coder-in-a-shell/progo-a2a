package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"a2a-proxy/pkg/config"
	"a2a-proxy/pkg/dispatcher"
)

// Server represents the A2A Proxy HTTP server.
type Server struct {
	httpServer *http.Server
	cfg        *config.Config
	disp       *dispatcher.Dispatcher
}

// New creates a new Server instance configured with routes, middleware, and server timeouts.
func New(cfg *config.Config, disp *dispatcher.Dispatcher, opts ...RouterOption) *Server {
	return NewServer(cfg, disp, opts...)
}

// NewServer creates a new Server instance configured with routes, middleware, and server timeouts.
func NewServer(cfg *config.Config, disp *dispatcher.Dispatcher, opts ...RouterOption) *Server {
	handler := SetupRouter(cfg, disp, opts...)

	addr := ""
	var readTimeout, writeTimeout, idleTimeout time.Duration

	if cfg != nil {
		addr = fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
		if cfg.Server.ReadTimeoutSeconds > 0 {
			readTimeout = time.Duration(cfg.Server.ReadTimeoutSeconds) * time.Second
		}
		if cfg.Server.WriteTimeoutSeconds > 0 {
			writeTimeout = time.Duration(cfg.Server.WriteTimeoutSeconds) * time.Second
		}
		if cfg.Server.IdleTimeoutSeconds > 0 {
			idleTimeout = time.Duration(cfg.Server.IdleTimeoutSeconds) * time.Second
		}
	}

	httpServer := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}

	return &Server{
		httpServer: httpServer,
		cfg:        cfg,
		disp:       disp,
	}
}

// Start runs ListenAndServe on the configured Host and Port.
func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

// Serve accepts incoming connections on the provided Listener.
func (s *Server) Serve(ln net.Listener) error {
	return s.httpServer.Serve(ln)
}

// Shutdown gracefully shuts down the server without interrupting active connections.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Addr returns the configured server address string.
func (s *Server) Addr() string {
	return s.httpServer.Addr
}

// Handler returns the root HTTP handler for the server.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}
