package storage

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/001_create_task_results.sql
var defaultMigrationSQL string

const (
	migrationLockID = int64(0x5461736b53746f72) // "TaskStor"

	upsertTaskResultsSQL = `
INSERT INTO task_results (lookup_id, task_id, agent_id, status, response, created_at, updated_at)
SELECT
    k,
    $2,
    $3,
    $4,
    $5::jsonb,
    NOW(),
    NOW()
FROM unnest($1::text[]) AS k
ON CONFLICT (lookup_id) DO UPDATE SET
    task_id = EXCLUDED.task_id,
    agent_id = EXCLUDED.agent_id,
    status = EXCLUDED.status,
    response = EXCLUDED.response,
    updated_at = NOW();
`

	loadTaskResultSQL = `
SELECT response
FROM task_results
WHERE lookup_id = $1;
`
)

var (
	// ErrBlankDSN is returned when the provided DSN is empty or whitespace-only.
	ErrBlankDSN = errors.New("dsn cannot be blank")

	// ErrInvalidPoolBounds is returned when MinConns exceeds MaxConns and both are positive.
	ErrInvalidPoolBounds = errors.New("min conns cannot be greater than max conns")

	// ErrStoreClosed is returned when an operation is attempted on a closed store.
	ErrStoreClosed = errors.New("postgres task store is closed")
)

var _ TaskStore = (*PostgresTaskStore)(nil)

// PostgresOptions configures connection pool settings for PostgresTaskStore.
type PostgresOptions struct {
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
}

// PostgresTaskStore is a PostgreSQL-backed implementation of TaskStore.
// It is safe for concurrent use.
type PostgresTaskStore struct {
	pool         *pgxpool.Pool
	closed       atomic.Bool
	redactTokens []string
}

// NewPostgresTaskStore creates a new PostgreSQL-backed task store.
// It parses the DSN, applies positive options to the pool config, creates the pool,
// and pings the database using the provided context.
// Constructor errors are wrapped with stable operation context and never contain credentials.
// Migrations are not run implicitly.
func NewPostgresTaskStore(ctx context.Context, dsn string, options PostgresOptions) (*PostgresTaskStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("postgres task store: connect: %w", ErrBlankDSN)
	}
	if options.MinConns > 0 && options.MaxConns > 0 && options.MinConns > options.MaxConns {
		return nil, fmt.Errorf("postgres task store: configure pool: %w", ErrInvalidPoolBounds)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("postgres task store: connect: %w", err)
	}

	tokens := extractRedactTokens(dsn)

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres task store: parse config: %w", redactError(err, tokens))
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
		return nil, fmt.Errorf("postgres task store: create pool: %w", redactError(err, tokens))
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres task store: ping: %w", redactError(err, tokens))
	}

	return &PostgresTaskStore{
		pool:         pool,
		redactTokens: tokens,
	}, nil
}

// Migrate applies embedded idempotent schema migrations.
// A transaction-scoped advisory lock guarantees safe concurrent invocation.
func (s *PostgresTaskStore) Migrate(ctx context.Context) error {
	if s.closed.Load() {
		return fmt.Errorf("postgres task store: migrate: %w", ErrStoreClosed)
	}
	if s.pool == nil {
		return fmt.Errorf("postgres task store: migrate: pool is nil")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres task store: migrate: begin transaction: %w", s.redactErr(err))
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("postgres task store: migrate: acquire advisory lock: %w", s.redactErr(err))
	}

	if _, err := tx.Exec(ctx, defaultMigrationSQL); err != nil {
		return fmt.Errorf("postgres task store: migrate: execute migration: %w", s.redactErr(err))
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres task store: migrate: commit transaction: %w", s.redactErr(err))
	}

	return nil
}

// Save atomically stores a TaskResponse under its canonical TaskID and zero or more alias IDs.
// Validates nil/empty canonical TaskID and respects canceled contexts.
// Canonical ID and aliases are deduplicated, and empty aliases are ignored.
// JSON-marshals the response before any database mutation.
// Lookup IDs are atomically upserted in one SQL statement, preserving created_at on conflict.
func (s *PostgresTaskStore) Save(ctx context.Context, resp *model.TaskResponse, aliases ...string) error {
	if resp == nil {
		return ErrNilTaskResponse
	}
	if resp.TaskID == "" {
		return ErrEmptyTaskID
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("postgres task store: save: %w", err)
	}

	seen := make(map[string]struct{}, 1+len(aliases))
	keys := make([]string, 0, 1+len(aliases))

	seen[resp.TaskID] = struct{}{}
	keys = append(keys, resp.TaskID)

	for _, a := range aliases {
		if a == "" {
			continue
		}
		if _, ok := seen[a]; !ok {
			seen[a] = struct{}{}
			keys = append(keys, a)
		}
	}

	respJSON, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("postgres task store: save: marshal response: %w", err)
	}

	if s.closed.Load() {
		return fmt.Errorf("postgres task store: save: %w", ErrStoreClosed)
	}
	if s.pool == nil {
		return fmt.Errorf("postgres task store: save: pool is nil")
	}

	_, err = s.pool.Exec(
		ctx,
		upsertTaskResultsSQL,
		keys,
		resp.TaskID,
		resp.AgentID,
		string(resp.Status),
		string(respJSON),
	)
	if err != nil {
		return fmt.Errorf("postgres task store: save: %w", s.redactErr(err))
	}

	return nil
}

// Load retrieves a TaskResponse by its canonical TaskID or alias ID.
// Validates context, queries response by lookup ID, maps pgx.ErrNoRows to ErrTaskNotFound,
// unmarshals JSON, and returns wrapped backend/decode errors.
func (s *PostgresTaskStore) Load(ctx context.Context, id string) (*model.TaskResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("postgres task store: load: %w", err)
	}
	if id == "" {
		return nil, ErrTaskNotFound
	}
	if s.closed.Load() {
		return nil, fmt.Errorf("postgres task store: load: %w", ErrStoreClosed)
	}
	if s.pool == nil {
		return nil, fmt.Errorf("postgres task store: load: pool is nil")
	}

	var respJSON []byte
	err := s.pool.QueryRow(ctx, loadTaskResultSQL, id).Scan(&respJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTaskNotFound
		}
		return nil, fmt.Errorf("postgres task store: load: %w", s.redactErr(err))
	}

	var resp model.TaskResponse
	if err := json.Unmarshal(respJSON, &resp); err != nil {
		return nil, fmt.Errorf("postgres task store: load: decode response: %w", err)
	}

	return &resp, nil
}

// Ping verifies that the connection pool can communicate with the database.
func (s *PostgresTaskStore) Ping(ctx context.Context) error {
	if s.closed.Load() {
		return fmt.Errorf("postgres task store: ping: %w", ErrStoreClosed)
	}
	if s.pool == nil {
		return fmt.Errorf("postgres task store: ping: pool is nil")
	}
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres task store: ping: %w", s.redactErr(err))
	}
	return nil
}

// Close closes the underlying connection pool. It is idempotent and safe for concurrent calls.
func (s *PostgresTaskStore) Close() {
	if s.closed.CompareAndSwap(false, true) {
		if s.pool != nil {
			s.pool.Close()
		}
	}
}

func (s *PostgresTaskStore) redactErr(err error) error {
	if err == nil {
		return nil
	}
	return redactError(err, s.redactTokens)
}

func extractRedactTokens(dsn string) []string {
	var tokens []string
	trimmed := strings.TrimSpace(dsn)
	if trimmed != "" {
		tokens = append(tokens, trimmed)
	}
	if dsn != "" && dsn != trimmed {
		tokens = append(tokens, dsn)
	}
	if u, err := url.Parse(dsn); err == nil && u != nil {
		if u.User != nil {
			if pass, ok := u.User.Password(); ok && pass != "" {
				tokens = append(tokens, pass)
			}
			userInfo := u.User.String()
			if userInfo != "" {
				tokens = append(tokens, userInfo)
			}
		}
	}
	for _, part := range strings.Fields(dsn) {
		if strings.HasPrefix(part, "password=") {
			val := strings.TrimPrefix(part, "password=")
			val = strings.Trim(val, "'\"")
			if val != "" {
				tokens = append(tokens, val)
			}
		}
	}
	return tokens
}

func redactString(msg string, tokens []string) string {
	for _, t := range tokens {
		if t != "" {
			msg = strings.ReplaceAll(msg, t, "[REDACTED]")
		}
	}
	return msg
}

func redactError(err error, tokens []string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New(redactString(err.Error(), tokens))
}
