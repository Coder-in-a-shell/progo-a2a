package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
)

func TestNewPostgresBundle_BlankDSN(t *testing.T) {
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
			bundle, err := NewPostgresBundle(context.Background(), tc.dsn, PostgresOptions{})
			if bundle != nil {
				t.Fatalf("expected nil bundle for blank DSN, got %v", bundle)
			}
			if err == nil {
				t.Fatal("expected error for blank DSN, got nil")
			}
			if !errors.Is(err, ErrBlankDSN) {
				t.Fatalf("expected ErrBlankDSN sentinel, got: %v", err)
			}
			if !strings.HasPrefix(err.Error(), "postgres bundle: connect:") {
				t.Fatalf("expected stable error prefix, got: %s", err.Error())
			}
		})
	}
}

func TestNewPostgresBundle_InvalidPoolBounds(t *testing.T) {
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

			bundle, err := NewPostgresBundle(ctx, "postgres://user:pass@localhost:5432/test", tc.options)
			if bundle != nil {
				t.Fatalf("expected nil bundle, got %v", bundle)
			}
			if tc.expectError {
				if err == nil {
					t.Fatal("expected pool bounds error, got nil")
				}
				if !errors.Is(err, ErrInvalidPoolBounds) {
					t.Fatalf("expected ErrInvalidPoolBounds, got: %v", err)
				}
				if !strings.HasPrefix(err.Error(), "postgres bundle: configure pool:") {
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

func TestNewPostgresBundle_CanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	bundle, err := NewPostgresBundle(ctx, "postgres://user:pass@localhost:5432/test", PostgresOptions{})
	if bundle != nil {
		t.Fatalf("expected nil bundle, got %v", bundle)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got: %v", err)
	}
}

func TestPostgresBundle_CloseIdempotentAndClosedOperations(t *testing.T) {
	bundle := &PostgresBundle{
		taskStore: &PostgresTaskStore{ownsPool: false},
		jobRepo:   &PostgresJobRepository{ownsPool: false},
	}

	// Views accessible before close
	if bundle.TaskStore() == nil {
		t.Fatal("expected non-nil TaskStore")
	}
	if bundle.JobRepository() == nil {
		t.Fatal("expected non-nil JobRepository")
	}

	// Idempotent Close
	bundle.Close()
	bundle.Close()

	ctx := context.Background()
	if err := bundle.Migrate(ctx); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed for Migrate after Close, got: %v", err)
	}
	if err := bundle.Ping(ctx); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed for Ping after Close, got: %v", err)
	}
}

func TestPostgresBundle_CloseToleratesMissingViews(t *testing.T) {
	bundle := &PostgresBundle{}
	bundle.Close()
	bundle.Close()
}

func TestIntegration_PostgresBundleSharedPoolRoundTrip(t *testing.T) {
	dsn := getTestDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	bundle, err := NewPostgresBundle(ctx, dsn, PostgresOptions{MaxConns: 4, MinConns: 1})
	if err != nil {
		t.Fatalf("create postgres bundle: %v", err)
	}

	prefix := fmt.Sprintf("bundle-%d", time.Now().UnixNano())
	cleanup := func() {
		if bundle.pool == nil || bundle.closed.Load() {
			return
		}
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanCancel()
		_, _ = bundle.pool.Exec(cleanCtx, "DELETE FROM durable_jobs WHERE tenant_id = $1", prefix+"-tenant")
		_, _ = bundle.pool.Exec(cleanCtx, "DELETE FROM task_results WHERE lookup_id LIKE $1", prefix+"%")
	}
	t.Cleanup(func() {
		cleanup()
		bundle.Close()
	})

	if err := bundle.Migrate(ctx); err != nil {
		t.Fatalf("migrate postgres bundle: %v", err)
	}
	if bundle.taskStore.pool != bundle.pool || bundle.jobRepo.pool != bundle.pool {
		t.Fatal("task and job views must share the bundle connection pool")
	}

	response := &model.TaskResponse{
		TaskID:  prefix + "-task",
		AgentID: "bundle-agent",
		Status:  model.StatusCompleted,
		Output:  map[string]any{"source": "bundle"},
	}
	if err := bundle.TaskStore().Save(ctx, response, prefix+"-alias"); err != nil {
		t.Fatalf("save through task-store view: %v", err)
	}
	loaded, err := bundle.TaskStore().Load(ctx, prefix+"-alias")
	if err != nil {
		t.Fatalf("load through task-store view: %v", err)
	}
	if loaded.TaskID != response.TaskID {
		t.Fatalf("loaded task ID = %q, want %q", loaded.TaskID, response.TaskID)
	}

	created, err := bundle.JobRepository().CreateJob(ctx, model.CreateJobInput{
		ID:             prefix + "-job",
		TenantID:       prefix + "-tenant",
		IdempotencyKey: prefix + "-key",
		Request:        map[string]any{"task": "bundle-round-trip"},
		AgentID:        "bundle-agent",
		MaxAttempts:    1,
	})
	if err != nil {
		t.Fatalf("create through job-repository view: %v", err)
	}
	gotJob, err := bundle.JobRepository().GetJob(ctx, created.TenantID, created.ID)
	if err != nil {
		t.Fatalf("get through job-repository view: %v", err)
	}
	if gotJob.ID != created.ID || gotJob.State != model.JobStateQueued {
		t.Fatalf("unexpected durable job: %+v", gotJob)
	}

	cleanup()
	bundle.Close()
	bundle.Close()

	if _, err := bundle.TaskStore().Load(context.Background(), response.TaskID); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("task view after bundle close: expected ErrStoreClosed, got %v", err)
	}
	if _, err := bundle.JobRepository().GetJob(context.Background(), created.TenantID, created.ID); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("job view after bundle close: expected ErrStoreClosed, got %v", err)
	}
	if err := bundle.Ping(context.Background()); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("bundle ping after close: expected ErrStoreClosed, got %v", err)
	}
}
