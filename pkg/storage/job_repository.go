package storage

import (
	"context"
	"errors"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
)

var (
	// ErrJobNotFound is returned when a requested job is not found for the given tenant.
	ErrJobNotFound = errors.New("job not found")

	// ErrIdempotencyConflict is returned when attempting to create a job with an existing idempotency key but a different request.
	ErrIdempotencyConflict = errors.New("idempotency conflict: key already exists with different request")

	// ErrLeaseLost is returned when a fenced mutation fails due to a stale token, expiration, wrong owner, or invalid state.
	ErrLeaseLost             = errors.New("lease lost: job lease is invalid, expired, or owned by another worker")
	ErrJobIDConflict         = errors.New("job ID already exists for tenant")
	ErrCancellationRequested = errors.New("job cancellation has been requested")
	ErrInvalidBatchSize      = errors.New("batch size must be between 1 and 1000")
	ErrInvalidLeaseDuration  = errors.New("lease duration must be between one millisecond and 24 hours")
	ErrJobPayloadTooLarge    = errors.New("job payload exceeds 10 MiB limit")
)

const (
	MaxJobBatchSize     = 1000
	MaxJobLeaseDuration = 24 * time.Hour
	MaxJobPayloadBytes  = 10 << 20
)

// Re-export model validation sentinels for caller convenience.
var (
	ErrEmptyTenantID         = model.ErrEmptyTenantID
	ErrEmptyJobID            = model.ErrEmptyJobID
	ErrEmptyIdempotencyKey   = model.ErrEmptyIdempotencyKey
	ErrInvalidRouting        = model.ErrInvalidRouting
	ErrInvalidMaxAttempts    = model.ErrInvalidMaxAttempts
	ErrEmptyLeaseOwner       = model.ErrEmptyLeaseOwner
	ErrInvalidLeaseToken     = model.ErrInvalidLeaseToken
	ErrFailureCodeTooLong    = model.ErrFailureCodeTooLong
	ErrFailureMessageTooLong = model.ErrFailureMessageTooLong
	ErrNilRequest            = model.ErrNilRequest
	ErrRequestHashMismatch   = model.ErrRequestHashMismatch
	ErrJobIDTooLong          = model.ErrJobIDTooLong
	ErrTenantIDTooLong       = model.ErrTenantIDTooLong
	ErrIdempotencyKeyTooLong = model.ErrIdempotencyKeyTooLong
	ErrRoutingValueTooLong   = model.ErrRoutingValueTooLong
	ErrLeaseOwnerTooLong     = model.ErrLeaseOwnerTooLong
	ErrInvalidRetryBackoff   = model.ErrInvalidRetryBackoff
)

// JobRepository defines the tenant-aware, fenced durable job ledger interface.
// Delivery semantics are explicitly at-least-once. Workers must assume distributed leases
// may be reclaimed after expiration, and all state transitions are monotonically fenced.
type JobRepository interface {
	// CreateJob idempotently creates a new durable job.
	// If a job with the same tenant ID and idempotency key already exists:
	// - matching deterministic request hash returns the existing job snapshot;
	// - differing request hash returns ErrIdempotencyConflict.
	CreateJob(ctx context.Context, input model.CreateJobInput) (*model.Job, error)

	// GetJob retrieves a durable job snapshot by tenant ID and job ID.
	// Returns ErrJobNotFound if the job does not exist for the tenant.
	GetJob(ctx context.Context, tenantID, jobID string) (*model.Job, error)

	// AcquireLeases atomically claims a batch of ready queued jobs ordered by next_run_at, then created_at.
	// Increments attempt and lease_token, sets lease_owner, and computes lease_expires_at using database time.
	AcquireLeases(ctx context.Context, owner string, batchSize int, leaseDuration time.Duration) ([]*model.Job, error)

	// MarkRunning transitions a leased job to running. Fenced by tenant, job ID, lease owner, and lease token.
	// Returns ErrLeaseLost if the lease was lost, expired, or modified.
	MarkRunning(ctx context.Context, fence model.LeaseFence) error

	// RenewLease extends lease expiration for a leased or running job using database time. Fenced by tenant, job ID, owner, and token.
	// Returns ErrLeaseLost if the lease was lost, expired, or modified.
	RenewLease(ctx context.Context, fence model.LeaseFence, duration time.Duration) error

	// CompleteJob records terminal success with response payload and clears lease fields. Fenced by tenant, job ID, owner, and token.
	// Marshals response before any mutation. Returns ErrLeaseLost if the lease was lost, expired, or modified.
	CompleteJob(ctx context.Context, fence model.LeaseFence, response any) error

	// FailJob reports job failure. If retryable and attempts remain, job returns to queued with scheduled retry.
	// If attempts are exhausted, transitions to dead_letter. If non-retryable, transitions to failed.
	// Fenced by tenant, job ID, owner, and token. Clears lease fields. Returns ErrLeaseLost on fence failure.
	// Failure input must be already sanitized; raw Go errors must never be passed.
	FailJob(ctx context.Context, input model.FailJobInput) error

	// CancelJob requests cancellation. Queued jobs become canceled immediately.
	// Leased or running jobs record cancellation intent. Terminal jobs remain terminal.
	CancelJob(ctx context.Context, tenantID, jobID string) error

	// ReclaimExpiredLeases scans expired leased and running jobs up to batchSize.
	// Cancellation-requested jobs become canceled, retryable jobs return to queued, and exhausted jobs become dead_letter.
	// Increments lease_token to fence stale workers and clears lease fields.
	ReclaimExpiredLeases(ctx context.Context, batchSize int, defaultBackoff time.Duration) (int, error)
}
