package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// JobState represents the execution lifecycle state of a durable job.
// Delivery semantics are explicitly at-least-once.
type JobState string

const (
	JobStateQueued     JobState = "queued"
	JobStateLeased     JobState = "leased"
	JobStateRunning    JobState = "running"
	JobStateSucceeded  JobState = "succeeded"
	JobStateFailed     JobState = "failed"
	JobStateCanceled   JobState = "canceled"
	JobStateDeadLetter JobState = "dead_letter"
)

// IsTerminal reports whether the job has reached a terminal state.
func (s JobState) IsTerminal() bool {
	switch s {
	case JobStateSucceeded, JobStateFailed, JobStateCanceled, JobStateDeadLetter:
		return true
	default:
		return false
	}
}

// Valid reports whether the state is one of the recognized JobState values.
func (s JobState) Valid() bool {
	switch s {
	case JobStateQueued, JobStateLeased, JobStateRunning, JobStateSucceeded,
		JobStateFailed, JobStateCanceled, JobStateDeadLetter:
		return true
	default:
		return false
	}
}

const (
	// MaxFailureCodeLength is the maximum allowed character/rune length for a public failure code.
	MaxFailureCodeLength = 128
	// MaxFailureMessageLength is the maximum allowed character/rune length for a public failure message.
	MaxFailureMessageLength = 2048
	// MaxAllowedAttempts is the upper bound on allowed maximum attempts.
	MaxAllowedAttempts      = 100
	MaxJobIDLength          = 256
	MaxTenantIDLength       = 128
	MaxIdempotencyKeyLength = 512
	MaxRoutingValueLength   = 256
	MaxLeaseOwnerLength     = 256
	MaxRetryBackoff         = 7 * 24 * time.Hour
)

var (
	// ErrEmptyTenantID is returned when a tenant ID is blank or whitespace-only.
	ErrEmptyTenantID = errors.New("tenant ID cannot be empty")
	// ErrEmptyJobID is returned when a job ID is blank or whitespace-only.
	ErrEmptyJobID = errors.New("job ID cannot be empty")
	// ErrEmptyIdempotencyKey is returned when an idempotency key is blank or whitespace-only.
	ErrEmptyIdempotencyKey = errors.New("idempotency key cannot be empty")
	// ErrInvalidRouting is returned when neither an agent ID nor a required capability is provided.
	ErrInvalidRouting = errors.New("invalid routing selection: agent ID or required capability must be specified")
	// ErrInvalidMaxAttempts is returned when max attempts is less than 1 or greater than MaxAllowedAttempts.
	ErrInvalidMaxAttempts = errors.New("max attempts must be between 1 and 100")
	// ErrEmptyLeaseOwner is returned when a lease owner is blank or whitespace-only.
	ErrEmptyLeaseOwner = errors.New("lease owner cannot be empty")
	// ErrInvalidLeaseToken is returned when a lease token is not positive.
	ErrInvalidLeaseToken = errors.New("lease token must be positive")
	// ErrFailureCodeTooLong is returned when a failure code exceeds MaxFailureCodeLength characters.
	ErrFailureCodeTooLong = errors.New("failure code exceeds maximum length")
	// ErrFailureMessageTooLong is returned when a failure message exceeds MaxFailureMessageLength characters.
	ErrFailureMessageTooLong = errors.New("failure message exceeds maximum length")
	// ErrNilRequest is returned when a job creation request payload is nil.
	ErrNilRequest = errors.New("job request payload cannot be nil")
	// ErrRequestHashMismatch is returned when a provided request hash does not match the deterministic hash of the request.
	ErrRequestHashMismatch   = errors.New("request hash does not match computed request hash")
	ErrJobIDTooLong          = errors.New("job ID exceeds maximum length")
	ErrTenantIDTooLong       = errors.New("tenant ID exceeds maximum length")
	ErrIdempotencyKeyTooLong = errors.New("idempotency key exceeds maximum length")
	ErrRoutingValueTooLong   = errors.New("routing value exceeds maximum length")
	ErrLeaseOwnerTooLong     = errors.New("lease owner exceeds maximum length")
	ErrInvalidRetryBackoff   = errors.New("retry backoff must be between zero and seven days")
)

// Job represents an immutable snapshot of a durable job in the ledger.
// Delivery semantics are explicitly at-least-once.
type Job struct {
	ID                 string          `json:"id"`
	TenantID           string          `json:"tenant_id"`
	IdempotencyKey     string          `json:"idempotency_key"`
	RequestHash        string          `json:"request_hash"`
	Request            json.RawMessage `json:"request"`
	AgentID            string          `json:"agent_id,omitempty"`
	RequiredCapability string          `json:"required_capability,omitempty"`
	State              JobState        `json:"state"`
	Attempt            int             `json:"attempt"`
	MaxAttempts        int             `json:"max_attempts"`
	NextRunAt          time.Time       `json:"next_run_at"`
	LeaseOwner         *string         `json:"lease_owner,omitempty"`
	LeaseToken         int64           `json:"lease_token"`
	LeaseExpiresAt     *time.Time      `json:"lease_expires_at,omitempty"`
	Response           json.RawMessage `json:"response,omitempty"`
	FailureCode        *string         `json:"failure_code,omitempty"`
	FailureMessage     *string         `json:"failure_message,omitempty"`
	CancelRequestedAt  *time.Time      `json:"cancel_requested_at,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// Fence returns the LeaseFence associated with the job's current lease snapshot.
func (j *Job) Fence() LeaseFence {
	owner := ""
	if j.LeaseOwner != nil {
		owner = *j.LeaseOwner
	}
	return LeaseFence{
		TenantID:   j.TenantID,
		JobID:      j.ID,
		LeaseOwner: owner,
		LeaseToken: j.LeaseToken,
	}
}

// LeaseFence contains fencing tokens required to mutate leased jobs safely.
type LeaseFence struct {
	TenantID   string `json:"tenant_id"`
	JobID      string `json:"job_id"`
	LeaseOwner string `json:"lease_owner"`
	LeaseToken int64  `json:"lease_token"`
}

// Validate checks structural constraints of a LeaseFence.
func (f LeaseFence) Validate() error {
	if strings.TrimSpace(f.TenantID) == "" {
		return ErrEmptyTenantID
	}
	if utf8.RuneCountInString(f.TenantID) > MaxTenantIDLength {
		return ErrTenantIDTooLong
	}
	if strings.TrimSpace(f.JobID) == "" {
		return ErrEmptyJobID
	}
	if utf8.RuneCountInString(f.JobID) > MaxJobIDLength {
		return ErrJobIDTooLong
	}
	if strings.TrimSpace(f.LeaseOwner) == "" {
		return ErrEmptyLeaseOwner
	}
	if utf8.RuneCountInString(f.LeaseOwner) > MaxLeaseOwnerLength {
		return ErrLeaseOwnerTooLong
	}
	if f.LeaseToken <= 0 {
		return ErrInvalidLeaseToken
	}
	return nil
}

// CreateJobInput encapsulates the parameters needed to create a new durable job.
type CreateJobInput struct {
	ID                 string     `json:"id"`
	TenantID           string     `json:"tenant_id"`
	IdempotencyKey     string     `json:"idempotency_key"`
	Request            any        `json:"request"`
	RequestHash        string     `json:"request_hash,omitempty"`
	AgentID            string     `json:"agent_id,omitempty"`
	RequiredCapability string     `json:"required_capability,omitempty"`
	MaxAttempts        int        `json:"max_attempts"`
	NextRunAt          *time.Time `json:"next_run_at,omitempty"`
}

// Validate checks the structural constraints of the creation input.
func (in *CreateJobInput) Validate() error {
	if strings.TrimSpace(in.TenantID) == "" {
		return ErrEmptyTenantID
	}
	if utf8.RuneCountInString(in.TenantID) > MaxTenantIDLength {
		return ErrTenantIDTooLong
	}
	if strings.TrimSpace(in.ID) == "" {
		return ErrEmptyJobID
	}
	if utf8.RuneCountInString(in.ID) > MaxJobIDLength {
		return ErrJobIDTooLong
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return ErrEmptyIdempotencyKey
	}
	if utf8.RuneCountInString(in.IdempotencyKey) > MaxIdempotencyKeyLength {
		return ErrIdempotencyKeyTooLong
	}
	if strings.TrimSpace(in.AgentID) == "" && strings.TrimSpace(in.RequiredCapability) == "" {
		return ErrInvalidRouting
	}
	if utf8.RuneCountInString(in.AgentID) > MaxRoutingValueLength || utf8.RuneCountInString(in.RequiredCapability) > MaxRoutingValueLength {
		return ErrRoutingValueTooLong
	}
	if in.MaxAttempts < 1 || in.MaxAttempts > MaxAllowedAttempts {
		return ErrInvalidMaxAttempts
	}
	if in.Request == nil {
		return ErrNilRequest
	}
	return nil
}

// FailJobInput encapsulates parameters for failing or retrying a leased job.
// Failure input is a bounded public code/message contract documented as already sanitized by caller.
// Raw Go errors must NEVER be passed or persisted.
type FailJobInput struct {
	TenantID       string        `json:"tenant_id"`
	JobID          string        `json:"job_id"`
	LeaseOwner     string        `json:"lease_owner"`
	LeaseToken     int64         `json:"lease_token"`
	FailureCode    string        `json:"failure_code"`
	FailureMessage string        `json:"failure_message"`
	Retryable      bool          `json:"retryable"`
	Backoff        time.Duration `json:"backoff,omitempty"`
}

// Fence returns the LeaseFence associated with this failure input.
func (in *FailJobInput) Fence() LeaseFence {
	return LeaseFence{
		TenantID:   in.TenantID,
		JobID:      in.JobID,
		LeaseOwner: in.LeaseOwner,
		LeaseToken: in.LeaseToken,
	}
}

// Validate checks structural constraints and enforces bounded failure code/message rune lengths.
func (in *FailJobInput) Validate() error {
	if err := in.Fence().Validate(); err != nil {
		return err
	}
	if utf8.RuneCountInString(in.FailureCode) > MaxFailureCodeLength {
		return ErrFailureCodeTooLong
	}
	if utf8.RuneCountInString(in.FailureMessage) > MaxFailureMessageLength {
		return ErrFailureMessageTooLong
	}
	if in.Backoff < 0 || in.Backoff > MaxRetryBackoff {
		return ErrInvalidRetryBackoff
	}
	return nil
}

// ComputeRequestHash computes a deterministic lowercase SHA-256 hex digest for a serialized request payload.
func ComputeRequestHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return strings.ToLower(hex.EncodeToString(sum[:]))
}
