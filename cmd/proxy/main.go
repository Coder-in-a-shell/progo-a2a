package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"a2a-proxy/pkg/adapter"
	"a2a-proxy/pkg/config"
	"a2a-proxy/pkg/dispatcher"
	"a2a-proxy/pkg/server"
)

func parseLogLevel(levelStr string) slog.Level {
	switch strings.ToLower(levelStr) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func main() {
	configPath := flag.String("config", "config/a2a-proxy.example.yaml", "Path to YAML configuration file")
	host := flag.String("host", "", "Server host address override")
	port := flag.Int("port", 0, "Server port override")
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	flag.Parse()

	// Setup structured logger
	level := parseLogLevel(*logLevel)
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	logger := slog.New(handler)
	slog.SetDefault(logger)

	logger.Info("starting A2A proxy",
		"config", *configPath,
		"log_level", level.String(),
	)

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load configuration", "error", err, "path", *configPath)
		os.Exit(1)
	}

	// Apply CLI overrides
	if *host != "" {
		cfg.Server.Host = *host
	}
	if *port > 0 {
		cfg.Server.Port = *port
	}

	if err := config.Validate(cfg); err != nil {
		logger.Error("invalid configuration after overrides", "error", err)
		os.Exit(1)
	}


	// Initialize adapter registry and register all 5 adapters
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewCustomAdapter())
	reg.Register(adapter.NewLangGraphAdapter())
	reg.Register(adapter.NewCrewAIAdapter())
	reg.Register(adapter.NewAutoGenAdapter())
	reg.Register(adapter.NewOpenAIAdapter())

	logger.Info("registered adapters", "count", reg.Count())

	// Initialize dispatcher
	disp := dispatcher.New(cfg, reg)

	// Initialize HTTP server
	srv := server.New(cfg, disp, server.WithLogger(logger))

	// Start server in background
	go func() {
		logger.Info("server listening", "addr", srv.Addr())
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Listen for shutdown signals
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	sig := <-quit
	logger.Info("received shutdown signal, shutting down gracefully...", "signal", sig.String())

	// 15-second graceful shutdown timeout
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("server forced to shutdown", "error", err)
		os.Exit(1)
	}

	logger.Info("server exited cleanly")
}
