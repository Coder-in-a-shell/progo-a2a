package storage

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresBundle owns a single PostgreSQL connection pool and provides
// TaskStore and JobRepository views over that pool.
// Delivery semantics are explicitly at-least-once.
type PostgresBundle struct {
	pool         *pgxpool.Pool
	closed       atomic.Bool
	redactTokens []string
	taskStore    *PostgresTaskStore
	jobRepo      *PostgresJobRepository
}

// NewPostgresBundle creates a shared PostgreSQL connection pool and constructs
// TaskStore and JobRepository views over the same pool.
// Redacts DSN credentials on connection and ping errors.
func NewPostgresBundle(ctx context.Context, dsn string, options PostgresOptions) (*PostgresBundle, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("postgres bundle: connect: %w", ErrBlankDSN)
	}
	if options.MinConns > 0 && options.MaxConns > 0 && options.MinConns > options.MaxConns {
		return nil, fmt.Errorf("postgres bundle: configure pool: %w", ErrInvalidPoolBounds)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("postgres bundle: connect: %w", err)
	}

	tokens := extractRedactTokens(dsn)

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres bundle: parse config: %w", redactError(err, tokens))
	}

	if options.MaxConns > 0 {
		cfg.MaxConns = options.MaxConns
	}
	if options.MinConns > 0 {
		cfg.MinConns = options.MinConns
	}
	if options.MaxConnLifetime > 0 {
		cfg.MaxConnLifetime = options.MaxConnLifetime
	}
	if options.MaxConnIdleTime > 0 {
		cfg.MaxConnIdleTime = options.MaxConnIdleTime
	}
	if options.HealthCheckPeriod > 0 {
		cfg.HealthCheckPeriod = options.HealthCheckPeriod
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		if pool != nil {
			pool.Close()
		}
		return nil, fmt.Errorf("postgres bundle: create pool: %w", redactError(err, tokens))
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres bundle: ping: %w", redactError(err, tokens))
	}

	bundle := &PostgresBundle{
		pool:         pool,
		redactTokens: tokens,
		taskStore: &PostgresTaskStore{
			pool:         pool,
			redactTokens: tokens,
			ownsPool:     false,
		},
		jobRepo: &PostgresJobRepository{
			pool:         pool,
			redactTokens: tokens,
			ownsPool:     false,
		},
	}

	return bundle, nil
}

// TaskStore returns the TaskStore view over the shared pool.
func (b *PostgresBundle) TaskStore() TaskStore {
	return b.taskStore
}

// JobRepository returns the JobRepository view over the shared pool.
func (b *PostgresBundle) JobRepository() JobRepository {
	return b.jobRepo
}

// Migrate executes all versioned schema migrations once using the transaction-scoped advisory lock.
func (b *PostgresBundle) Migrate(ctx context.Context) error {
	if b.closed.Load() {
		return fmt.Errorf("postgres bundle: migrate: %w", ErrStoreClosed)
	}
	if b.pool == nil {
		return fmt.Errorf("postgres bundle: migrate: pool is nil")
	}
	if err := runMigrations(ctx, b.pool, b.redactTokens); err != nil {
		return fmt.Errorf("postgres bundle: migrate: %w", err)
	}
	return nil
}

// Ping verifies communication with the shared database pool.
func (b *PostgresBundle) Ping(ctx context.Context) error {
	if b.closed.Load() {
		return fmt.Errorf("postgres bundle: ping: %w", ErrStoreClosed)
	}
	if b.pool == nil {
		return fmt.Errorf("postgres bundle: ping: pool is nil")
	}
	if err := b.pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres bundle: ping: %w", redactError(err, b.redactTokens))
	}
	return nil
}

// Close closes the underlying shared connection pool exactly once.
func (b *PostgresBundle) Close() {
	if b.closed.CompareAndSwap(false, true) {
		if b.taskStore != nil {
			b.taskStore.closed.Store(true)
		}
		if b.jobRepo != nil {
			b.jobRepo.closed.Store(true)
		}
		if b.pool != nil {
			b.pool.Close()
		}
	}
}
