package storage

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
)

func TestPostgresTaskStore_CompileTimeInterface(t *testing.T) {
	var _ TaskStore = (*PostgresTaskStore)(nil)
}

func TestNewPostgresTaskStore_BlankDSN(t *testing.T) {
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
			store, err := NewPostgresTaskStore(context.Background(), tc.dsn, PostgresOptions{})
			if store != nil {
				t.Fatalf("expected nil store for blank DSN, got %v", store)
			}
			if err == nil {
				t.Fatal("expected error for blank DSN, got nil")
			}
			if !errors.Is(err, ErrBlankDSN) {
				t.Fatalf("expected ErrBlankDSN sentinel, got: %v", err)
			}
			if !strings.HasPrefix(err.Error(), "postgres task store: connect:") {
				t.Fatalf("expected stable error prefix, got: %s", err.Error())
			}
		})
	}
}

func TestNewPostgresTaskStore_InvalidPoolBounds(t *testing.T) {
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
		{
			name:        "min positive but max non-positive leaves max default",
			options:     PostgresOptions{MinConns: 5, MaxConns: 0},
			expectError: false,
		},
		{
			name:        "min non-positive but max positive leaves min default",
			options:     PostgresOptions{MinConns: 0, MaxConns: 5},
			expectError: false,
		},
		{
			name:        "both non-positive leaves defaults",
			options:     PostgresOptions{MinConns: -1, MaxConns: -1},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Using an invalid DSN with canceled context so non-failing option cases abort cleanly without DB
			ctx, cancel := context.WithCancel(context.Background())
			if !tc.expectError {
				cancel()
			} else {
				defer cancel()
			}

			store, err := NewPostgresTaskStore(ctx, "postgres://user:pass@localhost:5432/test", tc.options)
			if store != nil {
				t.Fatalf("expected nil store, got %v", store)
			}
			if tc.expectError {
				if err == nil {
					t.Fatal("expected pool bounds error, got nil")
				}
				if !errors.Is(err, ErrInvalidPoolBounds) {
					t.Fatalf("expected ErrInvalidPoolBounds, got: %v", err)
				}
				if !strings.HasPrefix(err.Error(), "postgres task store: configure pool:") {
					t.Fatalf("expected stable error prefix, got: %s", err.Error())
				}
			} else {
				// Should fail on context cancellation, never on pool bounds
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

func TestNewPostgresTaskStore_CanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	store, err := NewPostgresTaskStore(ctx, "postgres://user:pass@localhost:5432/test", PostgresOptions{})
	if store != nil {
		t.Fatalf("expected nil store for canceled context, got %v", store)
	}
	if err == nil {
		t.Fatal("expected error for canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "postgres task store:") {
		t.Fatalf("expected stable error prefix, got: %s", err.Error())
	}
}

func TestPostgresTaskStore_JSONMarshalFailureBeforeDB(t *testing.T) {
	// A nil pool ensures that any database mutation attempt would panic.
	store := &PostgresTaskStore{}

	// Channels cannot be marshaled to JSON.
	resp := &model.TaskResponse{
		TaskID: "task-bad-json",
		Output: make(chan int),
	}

	err := store.Save(context.Background(), resp)
	if err == nil {
		t.Fatal("expected JSON marshal error, got nil")
	}
	if !strings.Contains(err.Error(), "postgres task store: save: marshal response:") {
		t.Fatalf("expected marshal response error context, got: %v", err)
	}
}

func TestPostgresTaskStore_SaveValidationSentinels(t *testing.T) {
	store := &PostgresTaskStore{}
	ctx := context.Background()

	// 1. Nil response
	if err := store.Save(ctx, nil); !errors.Is(err, ErrNilTaskResponse) {
		t.Fatalf("expected ErrNilTaskResponse, got %v", err)
	}

	// 2. Empty TaskID
	if err := store.Save(ctx, &model.TaskResponse{TaskID: ""}); !errors.Is(err, ErrEmptyTaskID) {
		t.Fatalf("expected ErrEmptyTaskID, got %v", err)
	}

	// 3. Canceled context
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	err := store.Save(canceledCtx, &model.TaskResponse{TaskID: "t-1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "postgres task store: save:") {
		t.Fatalf("expected stable prefix, got: %v", err)
	}
}

func TestPostgresTaskStore_LoadValidationSentinels(t *testing.T) {
	store := &PostgresTaskStore{}
	ctx := context.Background()

	// 1. Empty lookup ID
	if _, err := store.Load(ctx, ""); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("expected ErrTaskNotFound for empty ID, got %v", err)
	}

	// 2. Canceled context
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err := store.Load(canceledCtx, "t-1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "postgres task store: load:") {
		t.Fatalf("expected stable prefix, got: %v", err)
	}
}

func TestPostgresTaskStore_CloseIdempotent(t *testing.T) {
	store := &PostgresTaskStore{}

	// Sequential calls
	store.Close()
	store.Close()

	if !store.closed.Load() {
		t.Fatal("expected store.closed to be true")
	}

	// Concurrent calls
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Close()
		}()
	}
	wg.Wait()

	// Verify operations on closed store return ErrStoreClosed
	ctx := context.Background()
	if err := store.Ping(ctx); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on Ping, got: %v", err)
	}
	if err := store.Migrate(ctx); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on Migrate, got: %v", err)
	}
	if err := store.Save(ctx, &model.TaskResponse{TaskID: "t-1"}); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on Save, got: %v", err)
	}
	if _, err := store.Load(ctx, "t-1"); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed on Load, got: %v", err)
	}
}

func TestPostgresTaskStore_RedactDSN(t *testing.T) {
	urlDSN := "postgres://admin:supersecretpass@db.internal:5432/proddb?sslmode=disable"
	tokens := extractRedactTokens(urlDSN)

	foundPass := false
	foundDSN := false
	for _, tok := range tokens {
		if tok == "supersecretpass" {
			foundPass = true
		}
		if tok == urlDSN {
			foundDSN = true
		}
	}
	if !foundPass || !foundDSN {
		t.Fatalf("failed to extract sensitive tokens from url DSN: %v", tokens)
	}

	rawErr := errors.New("connection to postgres://admin:supersecretpass@db.internal:5432/proddb?sslmode=disable failed: auth failed with supersecretpass")
	redacted := redactError(rawErr, tokens)

	if strings.Contains(redacted.Error(), "supersecretpass") {
		t.Fatalf("redacted error still contains secret password: %s", redacted.Error())
	}
	if strings.Contains(redacted.Error(), urlDSN) {
		t.Fatalf("redacted error still contains raw DSN: %s", redacted.Error())
	}
	if errors.Is(redacted, rawErr) {
		t.Fatal("redacted error must not expose the original unsanitized error")
	}

	kvDSN := "host=localhost port=5432 user=myuser password=topsecretkvpass dbname=mydb"
	kvTokens := extractRedactTokens(kvDSN)
	foundKVPass := false
	for _, tok := range kvTokens {
		if tok == "topsecretkvpass" {
			foundKVPass = true
		}
	}
	if !foundKVPass {
		t.Fatalf("failed to extract password from key-value DSN: %v", kvTokens)
	}
}
