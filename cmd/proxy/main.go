package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
	"github.com/Coder-in-a-shell/progo-a2a/pkg/worker"
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		slog.Error("application error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("progo-a2a", flag.ContinueOnError)
	fs.SetOutput(stderr)

	configPath := fs.String("config", "config/progo-a2a.example.yaml", "Path to YAML configuration file")
	host := fs.String("host", "", "Server host address override")
	port := fs.Int("port", 0, "Server port override")
	logLevel := fs.String("log-level", "info", "Log level (debug, info, warn, error)")
	role := fs.String("role", "", "Runtime role override (api, worker, all)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	level := parseLogLevel(*logLevel)
	handler := slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: level})
	logger := slog.New(handler)
	slog.SetDefault(logger)

	logger.Info("starting ProGoA2A",
		"config", *configPath,
		"log_level", level.String(),
	)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Apply CLI overrides
	if *host != "" {
		cfg.Server.Host = *host
	}
	if *port > 0 {
		cfg.Server.Port = *port
	}
	if *role != "" {
		cfg.Role = *role
	}

	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("invalid configuration after overrides: %w", err)
	}

	// Initialize adapter registry and register all 5 adapters
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewCustomAdapter())
	reg.Register(adapter.NewLangGraphAdapter())
	reg.Register(adapter.NewCrewAIAdapter())
	reg.Register(adapter.NewAutoGenAdapter())
	reg.Register(adapter.NewOpenAIAdapter())

	logger.Info("registered adapters", "count", reg.Count())

	disp := dispatcher.New(cfg, reg)

	switch cfg.Role {
	case "api":
		return runAPI(ctx, cfg, disp, logger)
	case "worker":
		return runWorker(ctx, cfg, disp, logger)
	case "all":
		return runAll(ctx, cfg, disp, logger)
	default:
		return fmt.Errorf("unsupported runtime role: %q", cfg.Role)
	}
}

func runAPI(ctx context.Context, cfg *config.Config, disp *dispatcher.Dispatcher, logger *slog.Logger) error {
	store, closeStore, err := buildTaskStore(cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize task store: %w", err)
	}
	defer closeStore()

	logger.Info("initialized task store", "backend", cfg.Storage.Backend)

	srv := server.New(cfg, disp, server.WithLogger(logger), server.WithTaskStore(store))

	serverDone := make(chan error, 1)
	go func() {
		logger.Info("server listening", "addr", srv.Addr())
		err := srv.Start()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverDone <- err
	}()

	select {
	case err := <-serverDone:
		if err == nil {
			return errors.New("server exited unexpectedly with nil error")
		}
		return fmt.Errorf("server error: %w", err)
	case <-ctx.Done():
		logger.Info("received shutdown signal, shutting down server gracefully...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		shutdownErr := srv.Shutdown(shutdownCtx)
		select {
		case startErr := <-serverDone:
			if shutdownErr != nil {
				return fmt.Errorf("server forced to shutdown: %w", shutdownErr)
			}
			if startErr != nil {
				return fmt.Errorf("server error during shutdown: %w", startErr)
			}
		case <-shutdownCtx.Done():
			if shutdownErr != nil {
				return fmt.Errorf("server forced to shutdown: %w", shutdownErr)
			}
			return errors.New("timed out waiting for server to stop")
		}
		logger.Info("server exited cleanly")
		return nil
	}
}

func runWorker(ctx context.Context, cfg *config.Config, disp *dispatcher.Dispatcher, logger *slog.Logger) error {
	repo, closeRepo, err := buildJobRepository(cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize job repository: %w", err)
	}
	defer closeRepo()

	logger.Info("initialized job repository", "backend", cfg.Storage.Backend)

	exec := worker.NewDispatcherExecutor(disp)
	eng := worker.NewEngine(cfg.Worker, repo, exec, worker.WithLogger(logger))

	return eng.Run(ctx)
}

type serverComponent interface {
	Start() error
	Shutdown(ctx context.Context) error
}

type workerComponent interface {
	Run(ctx context.Context) error
}

func runAllComponents(ctx context.Context, srv serverComponent, eng workerComponent, drainTimeoutSeconds int, logger *slog.Logger) error {
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	serverDone := make(chan error, 1)
	go func() {
		err := srv.Start()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverDone <- err
	}()

	workerDone := make(chan error, 1)
	go func() {
		workerDone <- eng.Run(runCtx)
	}()

	var earlyErr error
	var serverExited, workerExited bool

	select {
	case err := <-serverDone:
		serverExited = true
		if ctx.Err() != nil && err == nil {
			// The parent cancellation and normal server exit raced in select.
		} else if err != nil {
			earlyErr = fmt.Errorf("server exited unexpectedly: %w", err)
		} else {
			earlyErr = errors.New("server exited unexpectedly with nil error")
		}
		cancelRun()
	case err := <-workerDone:
		workerExited = true
		if ctx.Err() != nil && err == nil {
			// The parent cancellation and normal worker exit raced in select.
		} else if err != nil {
			earlyErr = fmt.Errorf("worker exited unexpectedly: %w", err)
		} else {
			earlyErr = errors.New("worker exited unexpectedly with nil error")
		}
		cancelRun()
	case <-ctx.Done():
		if logger != nil {
			logger.Info("received shutdown signal, stopping all runtime components gracefully...")
		}
		cancelRun()
	}

	// 1. Shutdown server if not already exited
	var serverShutdownErr error
	if !serverExited {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancelShutdown()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverShutdownErr = fmt.Errorf("server shutdown error: %w", err)
		}
		select {
		case err := <-serverDone:
			if err != nil && serverShutdownErr == nil {
				serverShutdownErr = err
			}
		case <-shutdownCtx.Done():
			if serverShutdownErr == nil {
				serverShutdownErr = errors.New("timed out waiting for server to stop")
			}
		}
	}

	// 2. Wait for worker to finish draining if not already exited
	var workerShutdownErr error
	if !workerExited {
		drainTimeout := time.Duration(drainTimeoutSeconds+5) * time.Second
		drainTimer := time.NewTimer(drainTimeout)
		defer drainTimer.Stop()

		select {
		case err := <-workerDone:
			workerShutdownErr = err
		case <-drainTimer.C:
			workerShutdownErr = errors.New("worker drain deadline exceeded")
		}
	}

	return errors.Join(earlyErr, serverShutdownErr, workerShutdownErr)
}

func runAll(ctx context.Context, cfg *config.Config, disp *dispatcher.Dispatcher, logger *slog.Logger) error {
	bundle, closeBundle, err := buildStorageBundle(cfg)
	if err != nil {
		return fmt.Errorf("failed to initialize storage bundle: %w", err)
	}
	defer closeBundle()

	logger.Info("initialized shared postgres storage bundle")

	srv := server.New(cfg, disp, server.WithLogger(logger), server.WithTaskStore(bundle.TaskStore()))
	exec := worker.NewDispatcherExecutor(disp)
	eng := worker.NewEngine(cfg.Worker, bundle.JobRepository(), exec, worker.WithLogger(logger))

	return runAllComponents(ctx, srv, eng, cfg.Worker.DrainTimeoutSeconds, logger)
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

func buildJobRepository(cfg *config.Config) (storage.JobRepository, func(), error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("config cannot be nil")
	}
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

	repo, err := storage.NewPostgresJobRepository(ctx, pgCfg.DSN, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize postgres job repository: %w", sanitizeStorageError(err, pgCfg.DSN))
	}

	if pgCfg.MigrateOnStart {
		if err := repo.Migrate(ctx); err != nil {
			repo.Close()
			return nil, nil, fmt.Errorf("failed to run postgres migrations: %w", sanitizeStorageError(err, pgCfg.DSN))
		}
	}

	var once sync.Once
	closeFn := func() {
		once.Do(func() {
			repo.Close()
		})
	}
	return repo, closeFn, nil
}

func buildStorageBundle(cfg *config.Config) (*storage.PostgresBundle, func(), error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("config cannot be nil")
	}
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

	bundle, err := storage.NewPostgresBundle(ctx, pgCfg.DSN, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize postgres storage bundle: %w", sanitizeStorageError(err, pgCfg.DSN))
	}

	if pgCfg.MigrateOnStart {
		if err := bundle.Migrate(ctx); err != nil {
			bundle.Close()
			return nil, nil, fmt.Errorf("failed to run postgres migrations: %w", sanitizeStorageError(err, pgCfg.DSN))
		}
	}

	var once sync.Once
	closeFn := func() {
		once.Do(func() {
			bundle.Close()
		})
	}
	return bundle, closeFn, nil
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
