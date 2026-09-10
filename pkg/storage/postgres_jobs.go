package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ JobRepository = (*PostgresJobRepository)(nil)
var _ HealthChecker = (*PostgresJobRepository)(nil)

// PostgresJobRepository is a PostgreSQL-backed implementation of JobRepository.
// Delivery semantics are explicitly at-least-once.
// It is safe for concurrent use.
type PostgresJobRepository struct {
	pool         *pgxpool.Pool
	closed       atomic.Bool
	redactTokens []string
}

// NewPostgresJobRepository creates a new PostgreSQL-backed durable job repository.
// It parses the DSN, applies pool bounds, connects, and pings the database.
func NewPostgresJobRepository(ctx context.Context, dsn string, options PostgresOptions) (*PostgresJobRepository, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("postgres job repository: connect: %w", ErrBlankDSN)
	}
	if options.MinConns > 0 && options.MaxConns > 0 && options.MinConns > options.MaxConns {
		return nil, fmt.Errorf("postgres job repository: configure pool: %w", ErrInvalidPoolBounds)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("postgres job repository: connect: %w", err)
	}

	tokens := extractRedactTokens(dsn)

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres job repository: parse config: %w", redactError(err, tokens))
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
		return nil, fmt.Errorf("postgres job repository: create pool: %w", redactError(err, tokens))
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres job repository: ping: %w", redactError(err, tokens))
	}

	return &PostgresJobRepository{
		pool:         pool,
		redactTokens: tokens,
	}, nil
}

// Migrate executes all versioned migrations using the transaction-scoped advisory lock.
func (r *PostgresJobRepository) Migrate(ctx context.Context) error {
	if r.closed.Load() {
		return fmt.Errorf("postgres job repository: migrate: %w", ErrStoreClosed)
	}
	if r.pool == nil {
		return fmt.Errorf("postgres job repository: migrate: pool is nil")
	}

	if err := runMigrations(ctx, r.pool, r.redactTokens); err != nil {
		return fmt.Errorf("postgres job repository: migrate: %w", err)
	}
	return nil
}

// Ping verifies communication with the database pool.
func (r *PostgresJobRepository) Ping(ctx context.Context) error {
	if r.closed.Load() {
		return fmt.Errorf("postgres job repository: ping: %w", ErrStoreClosed)
	}
	if r.pool == nil {
		return fmt.Errorf("postgres job repository: ping: pool is nil")
	}
	if err := r.pool.Ping(ctx); err != nil {
		return fmt.Errorf("postgres job repository: ping: %w", r.redactErr(err))
	}
	return nil
}

// Close closes the underlying connection pool idempotently.
func (r *PostgresJobRepository) Close() {
	if r.closed.CompareAndSwap(false, true) {
		if r.pool != nil {
			r.pool.Close()
		}
	}
}

// CreateJob idempotently creates a new durable job.
// Caller cannot forge request hash; it is verified against or derived from marshaled request.
func (r *PostgresJobRepository) CreateJob(ctx context.Context, input model.CreateJobInput) (*model.Job, error) {
	if r.closed.Load() {
		return nil, fmt.Errorf("postgres job repository: create: %w", ErrStoreClosed)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("postgres job repository: create: %w", err)
	}
	if err := input.Validate(); err != nil {
		return nil, fmt.Errorf("postgres job repository: create: %w", err)
	}

	// Marshal and validate before any database mutation.
	reqBytes, err := marshalJobPayload(input.Request)
	if err != nil {
		return nil, fmt.Errorf("postgres job repository: create: marshal request: %w", err)
	}

	computedHash, err := computeJobIdempotencyHash(reqBytes, input)
	if err != nil {
		return nil, fmt.Errorf("postgres job repository: create: compute request hash: %w", err)
	}
	if input.RequestHash != "" && !strings.EqualFold(input.RequestHash, computedHash) {
		return nil, fmt.Errorf("postgres job repository: create: %w", ErrRequestHashMismatch)
	}
	reqHash := computedHash

	const insertJobSQL = `
INSERT INTO durable_jobs (
    id, tenant_id, idempotency_key, request_hash, request,
    agent_id, required_capability, state, attempt, max_attempts,
    next_run_at, lease_token, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5::jsonb,
    NULLIF(trim($6), ''), NULLIF(trim($7), ''), 'queued', 0, $8,
    COALESCE($9, NOW()), 0, NOW(), NOW()
)
ON CONFLICT DO NOTHING
RETURNING id, tenant_id, idempotency_key, request_hash, request,
          agent_id, required_capability, state, attempt, max_attempts,
          next_run_at, lease_owner, lease_token, lease_expires_at,
          response, failure_code, failure_message, cancel_requested_at,
          created_at, updated_at;
`

	row := r.pool.QueryRow(
		ctx,
		insertJobSQL,
		input.ID,
		input.TenantID,
		input.IdempotencyKey,
		reqHash,
		string(reqBytes),
		input.AgentID,
		input.RequiredCapability,
		input.MaxAttempts,
		input.NextRunAt,
	)

	job, err := scanJob(row)
	if err == nil {
		return job, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("postgres job repository: create: %w", r.redactErr(err))
	}

	// A row with (tenant_id, idempotency_key) already exists. Fetch and check request hash.
	const selectExistingSQL = `
SELECT id, tenant_id, idempotency_key, request_hash, request,
       agent_id, required_capability, state, attempt, max_attempts,
       next_run_at, lease_owner, lease_token, lease_expires_at,
       response, failure_code, failure_message, cancel_requested_at,
       created_at, updated_at
FROM durable_jobs
WHERE tenant_id = $1 AND idempotency_key = $2;
`
	existingRow := r.pool.QueryRow(ctx, selectExistingSQL, input.TenantID, input.IdempotencyKey)
	existingJob, err := scanJob(existingRow)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres job repository: create: %w", ErrJobIDConflict)
		}
		return nil, fmt.Errorf("postgres job repository: create: load existing: %w", r.redactErr(err))
	}

	if existingJob.RequestHash == reqHash {
		return existingJob, nil
	}

	return nil, fmt.Errorf("postgres job repository: create: %w", ErrIdempotencyConflict)
}

// GetJob retrieves a durable job by tenant ID and job ID.
func (r *PostgresJobRepository) GetJob(ctx context.Context, tenantID, jobID string) (*model.Job, error) {
	if r.closed.Load() {
		return nil, fmt.Errorf("postgres job repository: get: %w", ErrStoreClosed)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("postgres job repository: get: %w", err)
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("postgres job repository: get: %w", ErrEmptyTenantID)
	}
	if utf8.RuneCountInString(tenantID) > model.MaxTenantIDLength {
		return nil, fmt.Errorf("postgres job repository: get: %w", ErrTenantIDTooLong)
	}
	if strings.TrimSpace(jobID) == "" {
		return nil, fmt.Errorf("postgres job repository: get: %w", ErrEmptyJobID)
	}
	if utf8.RuneCountInString(jobID) > model.MaxJobIDLength {
		return nil, fmt.Errorf("postgres job repository: get: %w", ErrJobIDTooLong)
	}

	const selectJobSQL = `
SELECT id, tenant_id, idempotency_key, request_hash, request,
       agent_id, required_capability, state, attempt, max_attempts,
       next_run_at, lease_owner, lease_token, lease_expires_at,
       response, failure_code, failure_message, cancel_requested_at,
       created_at, updated_at
FROM durable_jobs
WHERE tenant_id = $1 AND id = $2;
`
	row := r.pool.QueryRow(ctx, selectJobSQL, tenantID, jobID)
	job, err := scanJob(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("postgres job repository: get: %w", ErrJobNotFound)
		}
		return nil, fmt.Errorf("postgres job repository: get: %w", r.redactErr(err))
	}
	return job, nil
}

// AcquireLeases atomically claims a batch of ready queued jobs ordered by next_run_at then created_at.
// Uses SKIP LOCKED in a short transaction, incrementing attempt and lease_token and using database time.
func (r *PostgresJobRepository) AcquireLeases(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error) {
	if r.closed.Load() {
		return nil, fmt.Errorf("postgres job repository: acquire leases: %w", ErrStoreClosed)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("postgres job repository: acquire leases: %w", err)
	}
	if strings.TrimSpace(owner) == "" {
		return nil, fmt.Errorf("postgres job repository: acquire leases: %w", ErrEmptyLeaseOwner)
	}
	if utf8.RuneCountInString(owner) > model.MaxLeaseOwnerLength {
		return nil, fmt.Errorf("postgres job repository: acquire leases: %w", ErrLeaseOwnerTooLong)
	}
	if err := validateJobBatchSize(batchSize); err != nil {
		return nil, fmt.Errorf("postgres job repository: acquire leases: %w", err)
	}
	if err := validateLeaseDuration(leaseDuration); err != nil {
		return nil, fmt.Errorf("postgres job repository: acquire leases: %w", err)
	}

	const acquireLeasesSQL = `
WITH ready AS (
    SELECT tenant_id, id
    FROM durable_jobs
    WHERE state = 'queued'
      AND next_run_at <= NOW()
      AND attempt < max_attempts
    ORDER BY next_run_at ASC, created_at ASC
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
UPDATE durable_jobs j
SET state = 'leased',
    attempt = j.attempt + 1,
    lease_owner = $2,
    lease_token = j.lease_token + 1,
    lease_expires_at = NOW() + ($3 * INTERVAL '1 millisecond'),
    updated_at = NOW()
FROM ready
WHERE j.tenant_id = ready.tenant_id AND j.id = ready.id
RETURNING j.id, j.tenant_id, j.idempotency_key, j.request_hash, j.request,
          j.agent_id, j.required_capability, j.state, j.attempt, j.max_attempts,
          j.next_run_at, j.lease_owner, j.lease_token, j.lease_expires_at,
          j.response, j.failure_code, j.failure_message, j.cancel_requested_at,
          j.created_at, j.updated_at;
`

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres job repository: acquire leases: %w", r.redactErr(err))
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	rows, err := tx.Query(ctx, acquireLeasesSQL, batchSize, owner, leaseDuration.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("postgres job repository: acquire leases: %w", r.redactErr(err))
	}
	defer rows.Close()

	var jobs []*model.Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres job repository: acquire leases: scan: %w", r.redactErr(err))
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres job repository: acquire leases: %w", r.redactErr(err))
	}
	rows.Close()

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("postgres job repository: acquire leases: commit: %w", r.redactErr(err))
	}

	if jobs == nil {
		jobs = []*model.Job{}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].NextRunAt.Equal(jobs[j].NextRunAt) {
			if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
				return jobs[i].ID < jobs[j].ID
			}
			return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
		}
		return jobs[i].NextRunAt.Before(jobs[j].NextRunAt)
	})
	return jobs, nil
}

// MarkRunning transitions a leased job to running. Fenced by tenant, job ID, lease owner, and lease token.
func (r *PostgresJobRepository) MarkRunning(ctx context.Context, fence model.LeaseFence) error {
	if r.closed.Load() {
		return fmt.Errorf("postgres job repository: mark running: %w", ErrStoreClosed)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("postgres job repository: mark running: %w", err)
	}
	if err := fence.Validate(); err != nil {
		return fmt.Errorf("postgres job repository: mark running: %w", err)
	}

	const markRunningSQL = `
UPDATE durable_jobs
SET state = 'running',
    updated_at = NOW()
WHERE tenant_id = $1
  AND id = $2
  AND lease_owner = $3
  AND lease_token = $4
  AND state = 'leased'
  AND cancel_requested_at IS NULL
  AND lease_expires_at > NOW();
`
	tag, err := r.pool.Exec(ctx, markRunningSQL, fence.TenantID, fence.JobID, fence.LeaseOwner, fence.LeaseToken)
	if err != nil {
		return fmt.Errorf("postgres job repository: mark running: %w", r.redactErr(err))
	}

	if tag.RowsAffected() == 0 {
		return r.handleLeaseMutationMiss(ctx, fence.TenantID, fence.JobID, "mark running", true)
	}

	return nil
}

// RenewLease extends lease expiry for a leased or running job using database time.
func (r *PostgresJobRepository) RenewLease(ctx context.Context, fence model.LeaseFence, duration time.Duration) error {
	if r.closed.Load() {
		return fmt.Errorf("postgres job repository: renew lease: %w", ErrStoreClosed)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("postgres job repository: renew lease: %w", err)
	}
	if err := fence.Validate(); err != nil {
		return fmt.Errorf("postgres job repository: renew lease: %w", err)
	}
	if err := validateLeaseDuration(duration); err != nil {
		return fmt.Errorf("postgres job repository: renew lease: %w", err)
	}

	const renewLeaseSQL = `
UPDATE durable_jobs
SET lease_expires_at = NOW() + ($5 * INTERVAL '1 millisecond'),
    updated_at = NOW()
WHERE tenant_id = $1
  AND id = $2
  AND lease_owner = $3
  AND lease_token = $4
  AND state IN ('leased', 'running')
  AND lease_expires_at > NOW();
`
	tag, err := r.pool.Exec(ctx, renewLeaseSQL, fence.TenantID, fence.JobID, fence.LeaseOwner, fence.LeaseToken, duration.Milliseconds())
	if err != nil {
		return fmt.Errorf("postgres job repository: renew lease: %w", r.redactErr(err))
	}

	if tag.RowsAffected() == 0 {
		return r.handleLeaseMutationMiss(ctx, fence.TenantID, fence.JobID, "renew lease", false)
	}

	return nil
}

// CompleteJob transitions a job to succeeded, stores response JSON, and clears lease fields.
func (r *PostgresJobRepository) CompleteJob(ctx context.Context, fence model.LeaseFence, response any) error {
	if r.closed.Load() {
		return fmt.Errorf("postgres job repository: complete: %w", ErrStoreClosed)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("postgres job repository: complete: %w", err)
	}
	if err := fence.Validate(); err != nil {
		return fmt.Errorf("postgres job repository: complete: %w", err)
	}

	respBytes, err := marshalJobPayload(response)
	if err != nil {
		return fmt.Errorf("postgres job repository: complete: marshal response: %w", err)
	}

	const completeJobSQL = `
UPDATE durable_jobs
SET state = 'succeeded',
    response = $5::jsonb,
    lease_owner = NULL,
    lease_expires_at = NULL,
    updated_at = NOW()
WHERE tenant_id = $1
  AND id = $2
  AND lease_owner = $3
  AND lease_token = $4
  AND state IN ('leased', 'running')
  AND lease_expires_at > NOW();
`
	tag, err := r.pool.Exec(ctx, completeJobSQL, fence.TenantID, fence.JobID, fence.LeaseOwner, fence.LeaseToken, string(respBytes))
	if err != nil {
		return fmt.Errorf("postgres job repository: complete: %w", r.redactErr(err))
	}

	if tag.RowsAffected() == 0 {
		return r.handleLeaseMutationMiss(ctx, fence.TenantID, fence.JobID, "complete", false)
	}

	return nil
}

// FailJob reports job failure. If retryable and attempts remain, returns to queued with backoff.
// If exhausted, moves to dead_letter. If non-retryable, moves to failed.
func (r *PostgresJobRepository) FailJob(ctx context.Context, input model.FailJobInput) error {
	if r.closed.Load() {
		return fmt.Errorf("postgres job repository: fail: %w", ErrStoreClosed)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("postgres job repository: fail: %w", err)
	}
	if err := input.Validate(); err != nil {
		return fmt.Errorf("postgres job repository: fail: %w", err)
	}

	const failJobSQL = `
UPDATE durable_jobs
SET state = CASE
        WHEN NOT $5 THEN 'failed'
        WHEN attempt < max_attempts THEN 'queued'
        ELSE 'dead_letter'
    END,
    next_run_at = CASE
        WHEN $5 AND attempt < max_attempts THEN NOW() + ($6 * INTERVAL '1 millisecond')
        ELSE next_run_at
    END,
    failure_code = NULLIF(trim($7), ''),
    failure_message = NULLIF(trim($8), ''),
    lease_owner = NULL,
    lease_expires_at = NULL,
    updated_at = NOW()
WHERE tenant_id = $1
  AND id = $2
  AND lease_owner = $3
  AND lease_token = $4
  AND state IN ('leased', 'running')
  AND lease_expires_at > NOW();
`
	tag, err := r.pool.Exec(
		ctx,
		failJobSQL,
		input.TenantID,
		input.JobID,
		input.LeaseOwner,
		input.LeaseToken,
		input.Retryable,
		input.Backoff.Milliseconds(),
		input.FailureCode,
		input.FailureMessage,
	)
	if err != nil {
		return fmt.Errorf("postgres job repository: fail: %w", r.redactErr(err))
	}

	if tag.RowsAffected() == 0 {
		return r.handleLeaseMutationMiss(ctx, input.TenantID, input.JobID, "fail", false)
	}

	return nil
}

// CancelJob requests job cancellation. Queued jobs cancel immediately.
// Leased/running jobs record intent. Terminal jobs remain terminal.
func (r *PostgresJobRepository) CancelJob(ctx context.Context, tenantID, jobID string) error {
	if r.closed.Load() {
		return fmt.Errorf("postgres job repository: cancel: %w", ErrStoreClosed)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("postgres job repository: cancel: %w", err)
	}
	if strings.TrimSpace(tenantID) == "" {
		return fmt.Errorf("postgres job repository: cancel: %w", ErrEmptyTenantID)
	}
	if utf8.RuneCountInString(tenantID) > model.MaxTenantIDLength {
		return fmt.Errorf("postgres job repository: cancel: %w", ErrTenantIDTooLong)
	}
	if strings.TrimSpace(jobID) == "" {
		return fmt.Errorf("postgres job repository: cancel: %w", ErrEmptyJobID)
	}
	if utf8.RuneCountInString(jobID) > model.MaxJobIDLength {
		return fmt.Errorf("postgres job repository: cancel: %w", ErrJobIDTooLong)
	}

	const cancelJobSQL = `
UPDATE durable_jobs
SET state = CASE
        WHEN state = 'queued' THEN 'canceled'
        ELSE state
    END,
    cancel_requested_at = CASE
        WHEN state IN ('queued', 'leased', 'running') THEN COALESCE(cancel_requested_at, NOW())
        ELSE cancel_requested_at
    END,
    updated_at = CASE
        WHEN state IN ('queued', 'leased', 'running') THEN NOW()
        ELSE updated_at
    END
WHERE tenant_id = $1 AND id = $2
RETURNING state;
`
	var state string
	err := r.pool.QueryRow(ctx, cancelJobSQL, tenantID, jobID).Scan(&state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("postgres job repository: cancel: %w", ErrJobNotFound)
		}
		return fmt.Errorf("postgres job repository: cancel: %w", r.redactErr(err))
	}

	return nil
}

// ReclaimExpiredLeases reclaims expired leases up to batchSize.
// Cancellation-requested jobs become canceled, retryable jobs become queued with backoff, and exhausted become dead_letter.
// Monotonically increments lease_token to fence stale workers and clears lease fields.
func (r *PostgresJobRepository) ReclaimExpiredLeases(ctx context.Context, batchSize int, defaultBackoff time.Duration) (int, error) {
	if r.closed.Load() {
		return 0, fmt.Errorf("postgres job repository: reclaim expired leases: %w", ErrStoreClosed)
	}
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("postgres job repository: reclaim expired leases: %w", err)
	}
	if err := validateJobBatchSize(batchSize); err != nil {
		return 0, fmt.Errorf("postgres job repository: reclaim expired leases: %w", err)
	}
	if defaultBackoff < 0 || defaultBackoff > model.MaxRetryBackoff {
		return 0, fmt.Errorf("postgres job repository: reclaim expired leases: %w", ErrInvalidRetryBackoff)
	}

	const reclaimExpiredSQL = `
WITH expired AS (
    SELECT tenant_id, id
    FROM durable_jobs
    WHERE state IN ('leased', 'running')
      AND lease_expires_at <= NOW()
    ORDER BY lease_expires_at ASC
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
UPDATE durable_jobs j
SET state = CASE
        WHEN j.cancel_requested_at IS NOT NULL THEN 'canceled'
        WHEN j.attempt < j.max_attempts THEN 'queued'
        ELSE 'dead_letter'
    END,
    next_run_at = CASE
        WHEN j.cancel_requested_at IS NULL AND j.attempt < j.max_attempts
        THEN NOW() + ($2 * INTERVAL '1 millisecond')
        ELSE j.next_run_at
    END,
    lease_owner = NULL,
    lease_expires_at = NULL,
    lease_token = j.lease_token + 1,
    updated_at = NOW()
FROM expired
WHERE j.tenant_id = expired.tenant_id AND j.id = expired.id;
`
	tag, err := r.pool.Exec(ctx, reclaimExpiredSQL, batchSize, defaultBackoff.Milliseconds())
	if err != nil {
		return 0, fmt.Errorf("postgres job repository: reclaim expired leases: %w", r.redactErr(err))
	}

	return int(tag.RowsAffected()), nil
}

func (r *PostgresJobRepository) handleLeaseMutationMiss(ctx context.Context, tenantID, jobID, op string, reportCancellation bool) error {
	const checkExistsSQL = `SELECT cancel_requested_at IS NOT NULL FROM durable_jobs WHERE tenant_id = $1 AND id = $2;`
	var cancellationRequested bool
	err := r.pool.QueryRow(ctx, checkExistsSQL, tenantID, jobID).Scan(&cancellationRequested)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("postgres job repository: %s: %w", op, ErrJobNotFound)
		}
		return fmt.Errorf("postgres job repository: %s: %w", op, r.redactErr(err))
	}
	if reportCancellation && cancellationRequested {
		return fmt.Errorf("postgres job repository: %s: %w", op, ErrCancellationRequested)
	}
	return fmt.Errorf("postgres job repository: %s: %w", op, ErrLeaseLost)
}

func marshalJobPayload(value any) ([]byte, error) {
	var (
		payload []byte
		err     error
	)
	if raw, ok := value.(json.RawMessage); ok {
		if !json.Valid(raw) {
			return nil, errors.New("invalid JSON payload")
		}
		payload = append([]byte(nil), raw...)
	} else {
		payload, err = json.Marshal(value)
		if err != nil {
			return nil, err
		}
	}
	if len(payload) > MaxJobPayloadBytes {
		return nil, ErrJobPayloadTooLarge
	}
	return payload, nil
}

func computeJobIdempotencyHash(request []byte, input model.CreateJobInput) (string, error) {
	fingerprint, err := json.Marshal(struct {
		Request            json.RawMessage `json:"request"`
		AgentID            string          `json:"agent_id,omitempty"`
		RequiredCapability string          `json:"required_capability,omitempty"`
		MaxAttempts        int             `json:"max_attempts"`
	}{
		Request:            request,
		AgentID:            strings.TrimSpace(input.AgentID),
		RequiredCapability: strings.TrimSpace(input.RequiredCapability),
		MaxAttempts:        input.MaxAttempts,
	})
	if err != nil {
		return "", err
	}
	return model.ComputeRequestHash(fingerprint), nil
}

func validateJobBatchSize(batchSize int) error {
	if batchSize < 1 || batchSize > MaxJobBatchSize {
		return ErrInvalidBatchSize
	}
	return nil
}

func validateLeaseDuration(duration time.Duration) error {
	if duration < time.Millisecond || duration > MaxJobLeaseDuration {
		return ErrInvalidLeaseDuration
	}
	return nil
}

func (r *PostgresJobRepository) redactErr(err error) error {
	if err == nil {
		return nil
	}
	return redactError(err, r.redactTokens)
}

func scanJob(row pgx.Row) (*model.Job, error) {
	var (
		j                  model.Job
		reqBytes           []byte
		respBytes          []byte
		agentID            *string
		requiredCapability *string
		state              string
	)

	err := row.Scan(
		&j.ID,
		&j.TenantID,
		&j.IdempotencyKey,
		&j.RequestHash,
		&reqBytes,
		&agentID,
		&requiredCapability,
		&state,
		&j.Attempt,
		&j.MaxAttempts,
		&j.NextRunAt,
		&j.LeaseOwner,
		&j.LeaseToken,
		&j.LeaseExpiresAt,
		&respBytes,
		&j.FailureCode,
		&j.FailureMessage,
		&j.CancelRequestedAt,
		&j.CreatedAt,
		&j.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	j.State = model.JobState(state)
	if agentID != nil {
		j.AgentID = *agentID
	}
	if requiredCapability != nil {
		j.RequiredCapability = *requiredCapability
	}

	// Copy byte slices into newly allocated memory so caller receives an independent decoded snapshot
	if len(reqBytes) > 0 {
		j.Request = make(json.RawMessage, len(reqBytes))
		copy(j.Request, reqBytes)
	}
	if len(respBytes) > 0 {
		j.Response = make(json.RawMessage, len(respBytes))
		copy(j.Response, respBytes)
	}

	return &j, nil
}
