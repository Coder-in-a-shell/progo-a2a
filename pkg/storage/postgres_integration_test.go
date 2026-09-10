package storage

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
)

func getTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if strings.TrimSpace(dsn) == "" {
		t.Skip("skipping integration test: POSTGRES_TEST_DSN is not set")
	}
	return dsn
}

func setupIntegrationTest(t *testing.T) (*PostgresTaskStore, string) {
	t.Helper()
	dsn := getTestDSN(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := NewPostgresTaskStore(ctx, dsn, PostgresOptions{
		MaxConns: 10,
		MinConns: 2,
	})
	if err != nil {
		t.Fatalf("failed to create test postgres task store: %v", err)
	}

	if err := store.Migrate(ctx); err != nil {
		store.Close()
		t.Fatalf("failed to migrate test database: %v", err)
	}

	prefix := fmt.Sprintf("test-%s-%d-%d", t.Name(), time.Now().UnixNano(), rand.Intn(100000))

	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanCancel()

		if store.pool != nil {
			_, _ = store.pool.Exec(cleanCtx, "DELETE FROM task_results WHERE lookup_id LIKE $1", prefix+"%")
		}
		store.Close()
	})

	return store, prefix
}

func TestIntegration_MigrationIdempotency(t *testing.T) {
	store, _ := setupIntegrationTest(t)
	ctx := context.Background()

	// Sequential repeated migration
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("repeated migration failed: %v", err)
	}

	// Concurrent migrations with advisory lock
	var wg sync.WaitGroup
	errCh := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.Migrate(ctx); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent migration failed: %v", err)
	}
}

func TestIntegration_CanonicalAndAliasRoundTrip(t *testing.T) {
	store, prefix := setupIntegrationTest(t)
	ctx := context.Background()

	canonID := prefix + "-canon"
	alias1 := prefix + "-alias-1"
	alias2 := prefix + "-alias-2"

	original := &model.TaskResponse{
		TaskID:    canonID,
		AgentID:   "agent-integration",
		Status:    model.StatusCompleted,
		Output:    map[string]any{"key": "value", "count": float64(42)},
		Artifacts: []model.Artifact{{Name: "result.txt", MimeType: "text/plain"}},
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
	}

	if err := store.Save(ctx, original, "", alias1, alias2, ""); err != nil {
		t.Fatalf("failed to save task response: %v", err)
	}

	// Load by canonical ID
	gotCanon, err := store.Load(ctx, canonID)
	if err != nil {
		t.Fatalf("failed to load by canonical ID: %v", err)
	}
	if gotCanon.TaskID != canonID || gotCanon.AgentID != original.AgentID || gotCanon.Status != original.Status {
		t.Fatalf("canonical response mismatch: %+v", gotCanon)
	}
	if len(gotCanon.Artifacts) != 1 || gotCanon.Artifacts[0].Name != "result.txt" {
		t.Fatalf("unexpected artifacts in canonical: %+v", gotCanon.Artifacts)
	}

	// Load by alias 1
	gotAlias1, err := store.Load(ctx, alias1)
	if err != nil {
		t.Fatalf("failed to load by alias 1: %v", err)
	}
	if gotAlias1.TaskID != canonID {
		t.Fatalf("alias 1 did not resolve to canonical TaskID: %s", gotAlias1.TaskID)
	}

	// Load by alias 2
	gotAlias2, err := store.Load(ctx, alias2)
	if err != nil {
		t.Fatalf("failed to load by alias 2: %v", err)
	}
	if gotAlias2.TaskID != canonID {
		t.Fatalf("alias 2 did not resolve to canonical TaskID: %s", gotAlias2.TaskID)
	}
}

func TestIntegration_UpsertAndUpdate(t *testing.T) {
	store, prefix := setupIntegrationTest(t)
	ctx := context.Background()

	canonID := prefix + "-upsert-task"
	alias := prefix + "-upsert-alias"

	initial := &model.TaskResponse{
		TaskID:    canonID,
		AgentID:   "agent-initial",
		Status:    model.StatusInProgress,
		Output:    "initial output",
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
	}

	if err := store.Save(ctx, initial, alias); err != nil {
		t.Fatalf("initial save failed: %v", err)
	}

	var createdAt1, updatedAt1 time.Time
	err := store.pool.QueryRow(ctx, "SELECT created_at, updated_at FROM task_results WHERE lookup_id = $1", canonID).Scan(&createdAt1, &updatedAt1)
	if err != nil {
		t.Fatalf("failed to query timestamps: %v", err)
	}

	// Sleep briefly so updated_at timestamp advances
	time.Sleep(50 * time.Millisecond)

	updated := &model.TaskResponse{
		TaskID:    canonID,
		AgentID:   "agent-updated",
		Status:    model.StatusCompleted,
		Output:    "final output",
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
	}

	if err := store.Save(ctx, updated, alias); err != nil {
		t.Fatalf("update save failed: %v", err)
	}

	// Verify loaded response has updated data
	loaded, err := store.Load(ctx, canonID)
	if err != nil {
		t.Fatalf("load after update failed: %v", err)
	}
	if loaded.Status != model.StatusCompleted || loaded.Output != "final output" || loaded.AgentID != "agent-updated" {
		t.Fatalf("unexpected loaded data after update: %+v", loaded)
	}

	var createdAt2, updatedAt2 time.Time
	err = store.pool.QueryRow(ctx, "SELECT created_at, updated_at FROM task_results WHERE lookup_id = $1", canonID).Scan(&createdAt2, &updatedAt2)
	if err != nil {
		t.Fatalf("failed to query updated timestamps: %v", err)
	}

	if !createdAt1.Equal(createdAt2) {
		t.Fatalf("created_at was modified on conflict: before=%v, after=%v", createdAt1, createdAt2)
	}
	if !updatedAt2.After(updatedAt1) {
		t.Fatalf("updated_at did not advance: before=%v, after=%v", updatedAt1, updatedAt2)
	}
}

func TestIntegration_MissingTaskSentinel(t *testing.T) {
	store, prefix := setupIntegrationTest(t)
	ctx := context.Background()

	missingID := prefix + "-nonexistent-id"
	resp, err := store.Load(ctx, missingID)
	if resp != nil {
		t.Fatalf("expected nil response for missing task, got: %v", resp)
	}
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("expected ErrTaskNotFound, got: %v", err)
	}
}

func TestIntegration_InvalidJSONPayload(t *testing.T) {
	store, prefix := setupIntegrationTest(t)
	ctx := context.Background()

	invalidID := prefix + "-invalid-json"

	// Insert invalid response payload (a JSON number '123' cannot unmarshal into TaskResponse struct)
	_, err := store.pool.Exec(
		ctx,
		"INSERT INTO task_results (lookup_id, task_id, agent_id, status, response) VALUES ($1, $2, $3, $4, '123'::jsonb)",
		invalidID,
		invalidID,
		"agent-test",
		"COMPLETED",
	)
	if err != nil {
		t.Fatalf("failed to insert invalid JSON payload: %v", err)
	}

	resp, err := store.Load(ctx, invalidID)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected unmarshal error, got nil")
	}
	if errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("decode error should not be ErrTaskNotFound: %v", err)
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("expected 'decode response' in error text, got: %v", err)
	}
}

func TestIntegration_Cancellation(t *testing.T) {
	store, prefix := setupIntegrationTest(t)

	resp := &model.TaskResponse{
		TaskID: prefix + "-cancel-task",
		Status: model.StatusCompleted,
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// Save with canceled context
	if err := store.Save(canceledCtx, resp); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on Save, got: %v", err)
	}

	// Load with canceled context
	if _, err := store.Load(canceledCtx, resp.TaskID); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on Load, got: %v", err)
	}
}

func TestIntegration_ConcurrentSavesAndLoads(t *testing.T) {
	store, prefix := setupIntegrationTest(t)
	ctx := context.Background()

	const concurrency = 10
	var wg sync.WaitGroup
	errCh := make(chan error, concurrency*2)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		id := fmt.Sprintf("%s-worker-%d", prefix, i)
		alias := fmt.Sprintf("%s-worker-%d-alias", prefix, i)
		taskResp := &model.TaskResponse{
			TaskID:  id,
			AgentID: fmt.Sprintf("agent-%d", i),
			Status:  model.StatusCompleted,
			Output:  fmt.Sprintf("output-%d", i),
		}

		go func(task *model.TaskResponse, a string) {
			defer wg.Done()
			if err := store.Save(ctx, task, a); err != nil {
				errCh <- fmt.Errorf("concurrent save failed for %s: %w", task.TaskID, err)
				return
			}

			loaded, err := store.Load(ctx, a)
			if err != nil {
				errCh <- fmt.Errorf("concurrent load failed for alias %s: %w", a, err)
				return
			}
			if loaded.TaskID != task.TaskID {
				errCh <- fmt.Errorf("mismatched task id: expected %s, got %s", task.TaskID, loaded.TaskID)
			}
		}(taskResp, alias)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}
}

func TestIntegration_PingAndClose(t *testing.T) {
	dsn := getTestDSN(t)
	ctx := context.Background()

	store, err := NewPostgresTaskStore(ctx, dsn, PostgresOptions{})
	if err != nil {
		t.Fatalf("failed to create store for PingAndClose: %v", err)
	}

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	store.Close()
	// Idempotent close
	store.Close()

	if err := store.Ping(ctx); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("expected ErrStoreClosed after Close, got: %v", err)
	}
}
