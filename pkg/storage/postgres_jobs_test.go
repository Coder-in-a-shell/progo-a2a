package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
)

func TestPostgresJobRepository_CompileTimeInterface(t *testing.T) {
	var _ JobRepository = (*PostgresJobRepository)(nil)
	var _ HealthChecker = (*PostgresJobRepository)(nil)
}

func TestNewPostgresJobRepository_BlankDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
	}{
		{name: "empty string", dsn: ""},
		{name: "spaces only", dsn: "   "},
		{name: "tabs and newlines", dsn: "\t\n\r"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, err := NewPostgresJobRepository(context.Background(), tc.dsn, PostgresOptions{})
			if repo != nil {
				t.Fatalf("expected nil repo for blank DSN, got %v", repo)
			}
			if err == nil {
				t.Fatal("expected error for blank DSN, got nil")
			}
			if !errors.Is(err, ErrBlankDSN) {
				t.Fatalf("expected ErrBlankDSN sentinel, got: %v", err)
			}
			if !strings.HasPrefix(err.Error(), "postgres job repository: connect:") {
				t.Fatalf("expected stable error prefix, got: %s", err.Error())
			}
		})
	}
}

func TestNewPostgresJobRepository_InvalidPoolBounds(t *testing.T) {
	tests := []struct {
		name        string
		options     PostgresOptions
		expectError bool
	}{
		{
			name:        "min exceeds max when both positive",
			options:     PostgresOptions{MinConns: 10, MaxConns: 5},
			expectError: true,
		},
		{
			name:        "min equals max when both positive",
			options:     PostgresOptions{MinConns: 5, MaxConns: 5},
			expectError: false,
		},
		{
			name:        "min less than max when both positive",
			options:     PostgresOptions{MinConns: 2, MaxConns: 5},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if !tc.expectError {
				cancel()
			} else {
				defer cancel()
			}

			repo, err := NewPostgresJobRepository(ctx, "postgres://user:pass@localhost:5432/test", tc.options)
			if repo != nil {
				t.Fatalf("expected nil repo, got %v", repo)
			}
			if tc.expectError {
				if err == nil {
					t.Fatal("expected pool bounds error, got nil")
				}
				if !errors.Is(err, ErrInvalidPoolBounds) {
					t.Fatalf("expected ErrInvalidPoolBounds, got: %v", err)
				}
				if !strings.HasPrefix(err.Error(), "postgres job repository: configure pool:") {
					t.Fatalf("expected stable error prefix, got: %s", err.Error())
				}
			} else {
				if errors.Is(err, ErrInvalidPoolBounds) {
					t.Fatalf("unexpected pool bounds error: %v", err)
				}
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected context.Canceled error, got: %v", err)
				}
			}
		})
	}
}

func TestNewPostgresJobRepository_CanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	repo, err := NewPostgresJobRepository(ctx, "postgres://user:pass@localhost:5432/test", PostgresOptions{})
	if repo != nil {
		t.Fatalf("expected nil repo for canceled context, got %v", repo)
	}
	if err == nil {
		t.Fatal("expected error for canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "postgres job repository:") {
		t.Fatalf("expected stable error prefix, got: %s", err.Error())
	}
}

func TestPostgresJobRepository_CloseIdempotent(t *testing.T) {
	repo := &PostgresJobRepository{}

	// Sequential calls
	repo.Close()
	repo.Close()

	if !repo.closed.Load() {
		t.Fatal("expected repo.closed to be true")
	}

	// Concurrent calls
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			repo.Close()
		}()
	}
	wg.Wait()

	// Verify all operations on closed repo return ErrStoreClosed
	ctx := context.Background()
	if err := repo.Ping(ctx); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on Ping, got: %v", err)
	}
	if err := repo.Migrate(ctx); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on Migrate, got: %v", err)
	}
	if _, err := repo.CreateJob(ctx, model.CreateJobInput{}); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on CreateJob, got: %v", err)
	}
	if _, err := repo.GetJob(ctx, "tenant", "job"); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on GetJob, got: %v", err)
	}
	if _, err := repo.AcquireLeases(ctx, "worker", 10, time.Second); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on AcquireLeases, got: %v", err)
	}
	fence := model.LeaseFence{TenantID: "t", JobID: "j", LeaseOwner: "o", LeaseToken: 1}
	if err := repo.MarkRunning(ctx, fence); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on MarkRunning, got: %v", err)
	}
	if err := repo.RenewLease(ctx, fence, time.Second); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on RenewLease, got: %v", err)
	}
	if err := repo.CompleteJob(ctx, fence, nil); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on CompleteJob, got: %v", err)
	}
	if err := repo.FailJob(ctx, model.FailJobInput{TenantID: "t", JobID: "j", LeaseOwner: "o", LeaseToken: 1}); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on FailJob, got: %v", err)
	}
	if err := repo.CancelJob(ctx, "t", "j"); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on CancelJob, got: %v", err)
	}
	if _, err := repo.ReclaimExpiredLeases(ctx, 10, time.Second); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on ReclaimExpiredLeases, got: %v", err)
	}
}

func TestPostgresJobRepository_JSONMarshalFailureBeforeDB(t *testing.T) {
	// A nil pool ensures that any database mutation attempt would panic.
	repo := &PostgresJobRepository{}

	// Channels cannot be marshaled to JSON.
	input := model.CreateJobInput{
		ID:             "job-bad-json",
		TenantID:       "tenant-a",
		IdempotencyKey: "key-1",
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        make(chan int),
	}

	_, err := repo.CreateJob(context.Background(), input)
	if err == nil {
		t.Fatal("expected JSON marshal error in CreateJob, got nil")
	}
	if !strings.Contains(err.Error(), "postgres job repository: create: marshal request:") {
		t.Fatalf("expected marshal request error context, got: %v", err)
	}

	fence := model.LeaseFence{
		TenantID:   "tenant-a",
		JobID:      "job-1",
		LeaseOwner: "worker-1",
		LeaseToken: 1,
	}
	err = repo.CompleteJob(context.Background(), fence, make(chan int))
	if err == nil {
		t.Fatal("expected JSON marshal error in CompleteJob, got nil")
	}
	if !strings.Contains(err.Error(), "postgres job repository: complete: marshal response:") {
		t.Fatalf("expected marshal response error context, got: %v", err)
	}

	input.Request = json.RawMessage(`{"broken":`)
	if _, err := repo.CreateJob(context.Background(), input); err == nil || !strings.Contains(err.Error(), "invalid JSON payload") {
		t.Fatalf("expected invalid raw request JSON to fail before DB, got: %v", err)
	}
	if err := repo.CompleteJob(context.Background(), fence, json.RawMessage(`{"broken":`)); err == nil || !strings.Contains(err.Error(), "invalid JSON payload") {
		t.Fatalf("expected invalid raw response JSON to fail before DB, got: %v", err)
	}
}

func TestPostgresJobRepository_ForgedRequestHashRejected(t *testing.T) {
	repo := &PostgresJobRepository{}

	input := model.CreateJobInput{
		ID:             "job-forged-hash",
		TenantID:       "tenant-a",
		IdempotencyKey: "key-1",
		AgentID:        "agent-1",
		MaxAttempts:    3,
		Request:        map[string]any{"action": "valid"},
		RequestHash:    strings.Repeat("f", 64), // Forged hash
	}

	_, err := repo.CreateJob(context.Background(), input)
	if err == nil {
		t.Fatal("expected error on forged request hash, got nil")
	}
	if !errors.Is(err, ErrRequestHashMismatch) {
		t.Fatalf("expected ErrRequestHashMismatch, got: %v", err)
	}
}

func TestPostgresJobRepository_ValidationSentinels(t *testing.T) {
	repo := &PostgresJobRepository{}
	ctx := context.Background()

	// 1. GetJob empty tenant / ID
	if _, err := repo.GetJob(ctx, "", "job-1"); !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("expected ErrEmptyTenantID, got: %v", err)
	}
	if _, err := repo.GetJob(ctx, "tenant-a", ""); !errors.Is(err, ErrEmptyJobID) {
		t.Fatalf("expected ErrEmptyJobID, got: %v", err)
	}

	// 2. AcquireLeases empty owner / non-positive duration
	if _, err := repo.AcquireLeases(ctx, "", 10, time.Second); !errors.Is(err, ErrEmptyLeaseOwner) {
		t.Fatalf("expected ErrEmptyLeaseOwner, got: %v", err)
	}
	if _, err := repo.AcquireLeases(ctx, "worker", 10, 0); err == nil {
		t.Fatal("expected error for zero lease duration, got nil")
	}
	if _, err := repo.AcquireLeases(ctx, "worker", 0, time.Second); !errors.Is(err, ErrInvalidBatchSize) {
		t.Fatalf("expected ErrInvalidBatchSize for zero batch, got: %v", err)
	}
	if _, err := repo.AcquireLeases(ctx, "worker", MaxJobBatchSize+1, time.Second); !errors.Is(err, ErrInvalidBatchSize) {
		t.Fatalf("expected ErrInvalidBatchSize for oversized batch, got: %v", err)
	}
	if _, err := repo.AcquireLeases(ctx, "worker", 1, MaxJobLeaseDuration+time.Millisecond); !errors.Is(err, ErrInvalidLeaseDuration) {
		t.Fatalf("expected ErrInvalidLeaseDuration, got: %v", err)
	}

	// 3. RenewLease non-positive duration
	fence := model.LeaseFence{TenantID: "t", JobID: "j", LeaseOwner: "o", LeaseToken: 1}
	if err := repo.RenewLease(ctx, fence, 0); err == nil {
		t.Fatal("expected error for zero renew duration, got nil")
	}

	// 4. CancelJob empty tenant / ID
	if err := repo.CancelJob(ctx, "", "j"); !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("expected ErrEmptyTenantID, got: %v", err)
	}
	if err := repo.CancelJob(ctx, "t", ""); !errors.Is(err, ErrEmptyJobID) {
		t.Fatalf("expected ErrEmptyJobID, got: %v", err)
	}
}

func TestMarshalJobPayload_LimitsAndByteEncoding(t *testing.T) {
	if _, err := marshalJobPayload(strings.Repeat("x", MaxJobPayloadBytes+1)); !errors.Is(err, ErrJobPayloadTooLarge) {
		t.Fatalf("expected ErrJobPayloadTooLarge, got: %v", err)
	}
	payload, err := marshalJobPayload([]byte("not-json"))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(payload) {
		t.Fatalf("byte slice must be encoded as valid JSON, got %q", payload)
	}
}

func TestComputeJobIdempotencyHash_IncludesExecutionSemantics(t *testing.T) {
	request := []byte(`{"task":"same"}`)
	base := model.CreateJobInput{AgentID: "agent-a", MaxAttempts: 3}
	baseHash, err := computeJobIdempotencyHash(request, base)
	if err != nil {
		t.Fatal(err)
	}
	variants := []model.CreateJobInput{
		{AgentID: "agent-b", MaxAttempts: 3},
		{RequiredCapability: "summarize", MaxAttempts: 3},
		{AgentID: "agent-a", MaxAttempts: 4},
	}
	for _, variant := range variants {
		hash, err := computeJobIdempotencyHash(request, variant)
		if err != nil {
			t.Fatal(err)
		}
		if hash == baseHash {
			t.Fatalf("semantic variant produced same hash: %+v", variant)
		}
	}
}
