package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/adapter"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/dispatcher"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/server"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/storage"
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
	configPath := flag.String("config", "config/progo-a2a.example.yaml", "Path to YAML configuration file")
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

	// Initialize task store
	store, closeStore, err := buildTaskStore(cfg)
	if err != nil {
		logger.Error("failed to initialize task store", "error", err)
		os.Exit(1)
	}
	defer closeStore()
	logger.Info("initialized task store", "backend", cfg.Storage.Backend)

	// Initialize HTTP server
	srv := server.New(cfg, disp, server.WithLogger(logger), server.WithTaskStore(store))

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

func buildTaskStore(cfg *config.Config) (storage.TaskStore, func(), error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("config cannot be nil")
	}

	backend := cfg.Storage.Backend
	if backend == "" {
		backend = "memory"
	}

	switch backend {
	case "memory":
		maxTasks := cfg.Storage.Memory.MaxTasks
		if maxTasks <= 0 {
			maxTasks = storage.DefaultMaxTasks
		}
		store := storage.NewMemoryTaskStore(maxTasks)
		return store, func() {}, nil

	case "postgres":
		pgCfg := cfg.Storage.Postgres
		opts := storage.PostgresOptions{
			MaxConns:          int32(pgCfg.MaxConnections),
			MinConns:          int32(pgCfg.MinConnections),
			MaxConnLifetime:   time.Duration(pgCfg.MaxConnectionLifetimeSeconds) * time.Second,
			MaxConnIdleTime:   time.Duration(pgCfg.MaxConnectionIdleTimeSeconds) * time.Second,
			HealthCheckPeriod: time.Duration(pgCfg.HealthCheckPeriodSeconds) * time.Second,
		}

		timeout := time.Duration(pgCfg.ConnectTimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		store, err := storage.NewPostgresTaskStore(ctx, pgCfg.DSN, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to initialize postgres task store: %w", sanitizeStorageError(err, pgCfg.DSN))
		}

		if pgCfg.MigrateOnStart {
			if err := store.Migrate(ctx); err != nil {
				store.Close()
				return nil, nil, fmt.Errorf("failed to run postgres migrations: %w", sanitizeStorageError(err, pgCfg.DSN))
			}
		}

		var once sync.Once
		closeFn := func() {
			once.Do(func() {
				store.Close()
			})
		}
		return store, closeFn, nil

	default:
		return nil, nil, fmt.Errorf("unsupported storage backend: %q", cfg.Storage.Backend)
	}
}

func sanitizeStorageError(err error, dsn string) error {
	if err == nil {
		return nil
	}
	trimmed := strings.TrimSpace(dsn)
	msg := err.Error()
	hasDSN := (trimmed != "" && strings.Contains(msg, trimmed)) || (dsn != "" && strings.Contains(msg, dsn))
	if !hasDSN {
		return err
	}
	if trimmed != "" {
		msg = strings.ReplaceAll(msg, trimmed, "[REDACTED]")
	}
	if dsn != "" {
		msg = strings.ReplaceAll(msg, dsn, "[REDACTED]")
	}
	return errors.New(msg)
}
