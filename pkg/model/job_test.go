package model

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCreateJobInput_Validate(t *testing.T) {
	validRequest := map[string]any{"action": "test"}

	tests := []struct {
		name        string
		input       CreateJobInput
		expectedErr error
	}{
		{
			name: "valid with agent ID only",
			input: CreateJobInput{
				ID:             "job-1",
				TenantID:       "tenant-a",
				IdempotencyKey: "key-1",
				AgentID:        "agent-1",
				MaxAttempts:    3,
				Request:        validRequest,
			},
			expectedErr: nil,
		},
		{
			name: "valid with capability only",
			input: CreateJobInput{
				ID:                 "job-2",
				TenantID:           "tenant-a",
				IdempotencyKey:     "key-2",
				RequiredCapability: "summarization",
				MaxAttempts:        1,
				Request:            validRequest,
			},
			expectedErr: nil,
		},
		{
			name: "valid with both agent ID and capability",
			input: CreateJobInput{
				ID:                 "job-3",
				TenantID:           "tenant-a",
				IdempotencyKey:     "key-3",
				AgentID:            "agent-1",
				RequiredCapability: "summarization",
				MaxAttempts:        100,
				Request:            validRequest,
			},
			expectedErr: nil,
		},
		{
			name: "empty tenant ID",
			input: CreateJobInput{
				ID:             "job-1",
				TenantID:       "",
				IdempotencyKey: "key-1",
				AgentID:        "agent-1",
				MaxAttempts:    3,
				Request:        validRequest,
			},
			expectedErr: ErrEmptyTenantID,
		},
		{
			name: "whitespace-only tenant ID",
			input: CreateJobInput{
				ID:             "job-1",
				TenantID:       "   \t\n",
				IdempotencyKey: "key-1",
				AgentID:        "agent-1",
				MaxAttempts:    3,
				Request:        validRequest,
			},
			expectedErr: ErrEmptyTenantID,
		},
		{
			name: "empty job ID",
			input: CreateJobInput{
				ID:             "",
				TenantID:       "tenant-a",
				IdempotencyKey: "key-1",
				AgentID:        "agent-1",
				MaxAttempts:    3,
				Request:        validRequest,
			},
			expectedErr: ErrEmptyJobID,
		},
		{
			name: "whitespace-only job ID",
			input: CreateJobInput{
				ID:             "   ",
				TenantID:       "tenant-a",
				IdempotencyKey: "key-1",
				AgentID:        "agent-1",
				MaxAttempts:    3,
				Request:        validRequest,
			},
			expectedErr: ErrEmptyJobID,
		},
		{
			name: "empty idempotency key",
			input: CreateJobInput{
				ID:             "job-1",
				TenantID:       "tenant-a",
				IdempotencyKey: "",
				AgentID:        "agent-1",
				MaxAttempts:    3,
				Request:        validRequest,
			},
			expectedErr: ErrEmptyIdempotencyKey,
		},
		{
			name: "whitespace-only idempotency key",
			input: CreateJobInput{
				ID:             "job-1",
				TenantID:       "tenant-a",
				IdempotencyKey: "   ",
				AgentID:        "agent-1",
				MaxAttempts:    3,
				Request:        validRequest,
			},
			expectedErr: ErrEmptyIdempotencyKey,
		},
		{
			name: "invalid routing: both agent and capability empty",
			input: CreateJobInput{
				ID:                 "job-1",
				TenantID:           "tenant-a",
				IdempotencyKey:     "key-1",
				AgentID:            "",
				RequiredCapability: "",
				MaxAttempts:        3,
				Request:            validRequest,
			},
			expectedErr: ErrInvalidRouting,
		},
		{
			name: "invalid routing: whitespace-only agent and capability",
			input: CreateJobInput{
				ID:                 "job-1",
				TenantID:           "tenant-a",
				IdempotencyKey:     "key-1",
				AgentID:            "  ",
				RequiredCapability: " \t ",
				MaxAttempts:        3,
				Request:            validRequest,
			},
			expectedErr: ErrInvalidRouting,
		},
		{
			name: "attempt bounds: zero max attempts",
			input: CreateJobInput{
				ID:             "job-1",
				TenantID:       "tenant-a",
				IdempotencyKey: "key-1",
				AgentID:        "agent-1",
				MaxAttempts:    0,
				Request:        validRequest,
			},
			expectedErr: ErrInvalidMaxAttempts,
		},
		{
			name: "attempt bounds: negative max attempts",
			input: CreateJobInput{
				ID:             "job-1",
				TenantID:       "tenant-a",
				IdempotencyKey: "key-1",
				AgentID:        "agent-1",
				MaxAttempts:    -5,
				Request:        validRequest,
			},
			expectedErr: ErrInvalidMaxAttempts,
		},
		{
			name: "attempt bounds: exceeds max allowed attempts",
			input: CreateJobInput{
				ID:             "job-1",
				TenantID:       "tenant-a",
				IdempotencyKey: "key-1",
				AgentID:        "agent-1",
				MaxAttempts:    101,
				Request:        validRequest,
			},
			expectedErr: ErrInvalidMaxAttempts,
		},
		{
			name: "nil request payload",
			input: CreateJobInput{
				ID:             "job-1",
				TenantID:       "tenant-a",
				IdempotencyKey: "key-1",
				AgentID:        "agent-1",
				MaxAttempts:    3,
				Request:        nil,
			},
			expectedErr: ErrNilRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.input.Validate()
			if tc.expectedErr == nil {
				if err != nil {
					t.Fatalf("expected nil error, got: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tc.expectedErr)
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
				}
			}
		})
	}
}

func TestFailJobInput_Validate(t *testing.T) {
	tests := []struct {
		name        string
		input       FailJobInput
		expectedErr error
	}{
		{
			name: "valid failure input",
			input: FailJobInput{
				TenantID:       "tenant-a",
				JobID:          "job-1",
				LeaseOwner:     "worker-1",
				LeaseToken:     1,
				FailureCode:    "DOWNSTREAM_UNAVAILABLE",
				FailureMessage: "upstream service returned HTTP 503",
				Retryable:      true,
			},
			expectedErr: nil,
		},
		{
			name: "valid empty code and message",
			input: FailJobInput{
				TenantID:   "tenant-a",
				JobID:      "job-1",
				LeaseOwner: "worker-1",
				LeaseToken: 2,
				Retryable:  false,
			},
			expectedErr: nil,
		},
		{
			name: "empty tenant ID",
			input: FailJobInput{
				TenantID:   "",
				JobID:      "job-1",
				LeaseOwner: "worker-1",
				LeaseToken: 1,
			},
			expectedErr: ErrEmptyTenantID,
		},
		{
			name: "empty job ID",
			input: FailJobInput{
				TenantID:   "tenant-a",
				JobID:      "",
				LeaseOwner: "worker-1",
				LeaseToken: 1,
			},
			expectedErr: ErrEmptyJobID,
		},
		{
			name: "empty lease owner",
			input: FailJobInput{
				TenantID:   "tenant-a",
				JobID:      "job-1",
				LeaseOwner: "",
				LeaseToken: 1,
			},
			expectedErr: ErrEmptyLeaseOwner,
		},
		{
			name: "zero lease token",
			input: FailJobInput{
				TenantID:   "tenant-a",
				JobID:      "job-1",
				LeaseOwner: "worker-1",
				LeaseToken: 0,
			},
			expectedErr: ErrInvalidLeaseToken,
		},
		{
			name: "negative lease token",
			input: FailJobInput{
				TenantID:   "tenant-a",
				JobID:      "job-1",
				LeaseOwner: "worker-1",
				LeaseToken: -1,
			},
			expectedErr: ErrInvalidLeaseToken,
		},
		{
			name: "oversized failure code",
			input: FailJobInput{
				TenantID:       "tenant-a",
				JobID:          "job-1",
				LeaseOwner:     "worker-1",
				LeaseToken:     1,
				FailureCode:    strings.Repeat("C", MaxFailureCodeLength+1),
				FailureMessage: "message",
			},
			expectedErr: ErrFailureCodeTooLong,
		},
		{
			name: "exact max failure code length",
			input: FailJobInput{
				TenantID:       "tenant-a",
				JobID:          "job-1",
				LeaseOwner:     "worker-1",
				LeaseToken:     1,
				FailureCode:    strings.Repeat("C", MaxFailureCodeLength),
				FailureMessage: "message",
			},
			expectedErr: nil,
		},
		{
			name: "oversized failure message",
			input: FailJobInput{
				TenantID:       "tenant-a",
				JobID:          "job-1",
				LeaseOwner:     "worker-1",
				LeaseToken:     1,
				FailureCode:    "CODE",
				FailureMessage: strings.Repeat("M", MaxFailureMessageLength+1),
			},
			expectedErr: ErrFailureMessageTooLong,
		},
		{
			name: "exact max failure message length",
			input: FailJobInput{
				TenantID:       "tenant-a",
				JobID:          "job-1",
				LeaseOwner:     "worker-1",
				LeaseToken:     1,
				FailureCode:    "CODE",
				FailureMessage: strings.Repeat("M", MaxFailureMessageLength),
			},
			expectedErr: nil,
		},
		{
			name: "exact max failure code length with multibyte unicode characters",
			input: FailJobInput{
				TenantID:       "tenant-a",
				JobID:          "job-1",
				LeaseOwner:     "worker-1",
				LeaseToken:     1,
				FailureCode:    strings.Repeat("日", MaxFailureCodeLength), // 128 runes, 384 bytes
				FailureMessage: "message",
			},
			expectedErr: nil,
		},
		{
			name: "oversized failure code with multibyte unicode characters",
			input: FailJobInput{
				TenantID:       "tenant-a",
				JobID:          "job-1",
				LeaseOwner:     "worker-1",
				LeaseToken:     1,
				FailureCode:    strings.Repeat("日", MaxFailureCodeLength+1), // 129 runes
				FailureMessage: "message",
			},
			expectedErr: ErrFailureCodeTooLong,
		},
		{
			name: "exact max failure message length with multibyte unicode characters",
			input: FailJobInput{
				TenantID:       "tenant-a",
				JobID:          "job-1",
				LeaseOwner:     "worker-1",
				LeaseToken:     1,
				FailureCode:    "CODE",
				FailureMessage: strings.Repeat("界", MaxFailureMessageLength), // 2048 runes, 6144 bytes
			},
			expectedErr: nil,
		},
		{
			name: "oversized failure message length with multibyte unicode characters",
			input: FailJobInput{
				TenantID:       "tenant-a",
				JobID:          "job-1",
				LeaseOwner:     "worker-1",
				LeaseToken:     1,
				FailureCode:    "CODE",
				FailureMessage: strings.Repeat("界", MaxFailureMessageLength+1), // 2049 runes
			},
			expectedErr: ErrFailureMessageTooLong,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.input.Validate()
			if tc.expectedErr == nil {
				if err != nil {
					t.Fatalf("expected nil error, got: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tc.expectedErr)
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
				}
			}
		})
	}
}

func TestJobState_Methods(t *testing.T) {
	states := []struct {
		state      JobState
		isTerminal bool
		valid      bool
	}{
		{JobStateQueued, false, true},
		{JobStateLeased, false, true},
		{JobStateRunning, false, true},
		{JobStateSucceeded, true, true},
		{JobStateFailed, true, true},
		{JobStateCanceled, true, true},
		{JobStateDeadLetter, true, true},
		{JobState("unknown"), false, false},
	}

	for _, tc := range states {
		t.Run(string(tc.state), func(t *testing.T) {
			if tc.state.IsTerminal() != tc.isTerminal {
				t.Fatalf("state %s: expected IsTerminal=%v, got %v", tc.state, tc.isTerminal, tc.state.IsTerminal())
			}
			if tc.state.Valid() != tc.valid {
				t.Fatalf("state %s: expected Valid=%v, got %v", tc.state, tc.valid, tc.state.Valid())
			}
		})
	}
}

func TestJob_Fence(t *testing.T) {
	owner := "worker-alpha"
	job := &Job{
		ID:         "job-xyz",
		TenantID:   "tenant-beta",
		LeaseOwner: &owner,
		LeaseToken: 42,
	}

	fence := job.Fence()
	if fence.TenantID != "tenant-beta" {
		t.Fatalf("expected TenantID tenant-beta, got %s", fence.TenantID)
	}
	if fence.JobID != "job-xyz" {
		t.Fatalf("expected JobID job-xyz, got %s", fence.JobID)
	}
	if fence.LeaseOwner != "worker-alpha" {
		t.Fatalf("expected LeaseOwner worker-alpha, got %s", fence.LeaseOwner)
	}
	if fence.LeaseToken != 42 {
		t.Fatalf("expected LeaseToken 42, got %d", fence.LeaseToken)
	}

	// Nil lease owner
	jobNilOwner := &Job{
		ID:         "job-xyz",
		TenantID:   "tenant-beta",
		LeaseOwner: nil,
		LeaseToken: 0,
	}
	fenceNil := jobNilOwner.Fence()
	if fenceNil.LeaseOwner != "" {
		t.Fatalf("expected empty LeaseOwner, got %s", fenceNil.LeaseOwner)
	}
}

func TestComputeRequestHash(t *testing.T) {
	payload1 := []byte(`{"message":"hello"}`)
	payload2 := []byte(`{"message":"hello"}`)
	payload3 := []byte(`{"message":"world"}`)

	hash1 := ComputeRequestHash(payload1)
	hash2 := ComputeRequestHash(payload2)
	hash3 := ComputeRequestHash(payload3)

	if hash1 == "" {
		t.Fatal("hash should not be empty")
	}
	if hash1 != hash2 {
		t.Fatalf("deterministic hash mismatch: %s != %s", hash1, hash2)
	}
	if hash1 == hash3 {
		t.Fatalf("different payloads produced same hash: %s", hash1)
	}
	if len(hash1) != 64 {
		t.Fatalf("expected 64 hex characters for SHA-256, got %d", len(hash1))
	}
}

func TestFailJobInput_BackoffBounds(t *testing.T) {
	base := FailJobInput{TenantID: "tenant", JobID: "job", LeaseOwner: "worker", LeaseToken: 1}
	for _, test := range []struct {
		name    string
		backoff time.Duration
		wantErr bool
	}{
		{name: "zero", backoff: 0},
		{name: "maximum", backoff: MaxRetryBackoff},
		{name: "negative", backoff: -time.Millisecond, wantErr: true},
		{name: "too large", backoff: MaxRetryBackoff + time.Millisecond, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.Backoff = test.backoff
			err := input.Validate()
			if test.wantErr && !errors.Is(err, ErrInvalidRetryBackoff) {
				t.Fatalf("expected ErrInvalidRetryBackoff, got %v", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
