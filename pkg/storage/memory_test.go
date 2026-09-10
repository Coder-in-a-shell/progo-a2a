package storage_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/storage"
)

func TestMemoryTaskStore_ValidationTable(t *testing.T) {
	tests := []struct {
		name        string
		maxTasks    int
		resp        *model.TaskResponse
		aliases     []string
		expectedErr error
	}{
		{
			name:        "nil response rejected",
			maxTasks:    10,
			resp:        nil,
			aliases:     nil,
			expectedErr: storage.ErrNilTaskResponse,
		},
		{
			name:        "empty canonical TaskID rejected",
			maxTasks:    10,
			resp:        &model.TaskResponse{TaskID: ""},
			aliases:     []string{"alias-1"},
			expectedErr: storage.ErrEmptyTaskID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemoryTaskStore(tc.maxTasks)
			err := store.Save(context.Background(), tc.resp, tc.aliases...)
			if !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
		})
	}
}

func TestMemoryTaskStore_DefaultCapacity(t *testing.T) {
	for _, capVal := range []int{0, -1, -100} {
		store := storage.NewMemoryTaskStore(capVal)
		// Default should be 10000
		if store == nil {
			t.Fatalf("expected non-nil store for capacity %d", capVal)
		}
	}
}

func TestMemoryTaskStore_AliasesAndEmptyIgnored(t *testing.T) {
	store := storage.NewMemoryTaskStore(10)
	ctx := context.Background()

	resp := &model.TaskResponse{
		TaskID:    "task-canon",
		AgentID:   "agent-1",
		Status:    model.StatusCompleted,
		Output:    "canon-output",
		Timestamp: time.Now().UTC(),
	}

	// Save with empty aliases and valid aliases
	err := store.Save(ctx, resp, "", "alias-1", "", "alias-2")
	if err != nil {
		t.Fatalf("unexpected save error: %v", err)
	}

	// Store should have 3 keys: task-canon, alias-1, alias-2
	if store.Len() != 3 {
		t.Fatalf("expected 3 keys, got %d", store.Len())
	}

	// Can load via canonical ID
	gotCanon, err := store.Load(ctx, "task-canon")
	if err != nil {
		t.Fatalf("failed to load canonical: %v", err)
	}
	if gotCanon.Output != "canon-output" {
		t.Fatalf("unexpected output: %v", gotCanon.Output)
	}

	// Can load via alias-1
	gotAlias1, err := store.Load(ctx, "alias-1")
	if err != nil {
		t.Fatalf("failed to load alias-1: %v", err)
	}
	if gotAlias1.TaskID != "task-canon" {
		t.Fatalf("unexpected task ID via alias-1: %v", gotAlias1.TaskID)
	}

	// Can load via alias-2
	gotAlias2, err := store.Load(ctx, "alias-2")
	if err != nil {
		t.Fatalf("failed to load alias-2: %v", err)
	}
	if gotAlias2.TaskID != "task-canon" {
		t.Fatalf("unexpected task ID via alias-2: %v", gotAlias2.TaskID)
	}

	// Empty string load returns ErrTaskNotFound
	_, err = store.Load(ctx, "")
	if !errors.Is(err, storage.ErrTaskNotFound) {
		t.Fatalf("expected ErrTaskNotFound for empty string, got %v", err)
	}
}

func TestMemoryTaskStore_Deduplication(t *testing.T) {
	store := storage.NewMemoryTaskStore(10)
	ctx := context.Background()

	resp := &model.TaskResponse{
		TaskID:  "task-dup",
		AgentID: "agent-1",
		Status:  model.StatusCompleted,
	}

	// Canonical ID repeated in aliases + duplicate aliases
	err := store.Save(ctx, resp, "task-dup", "alias-dup", "alias-dup", "task-dup")
	if err != nil {
		t.Fatalf("unexpected save error: %v", err)
	}

	// Total unique keys: task-dup, alias-dup => 2 keys
	if store.Len() != 2 {
		t.Fatalf("expected 2 keys after deduplication, got %d", store.Len())
	}
}

func TestMemoryTaskStore_UpdateOrdering(t *testing.T) {
	// Capacity of 3
	store := storage.NewMemoryTaskStore(3)
	ctx := context.Background()

	// Insert k1, k2, k3
	for i := 1; i <= 3; i++ {
		err := store.Save(ctx, &model.TaskResponse{
			TaskID: fmt.Sprintf("k%d", i),
			Output: fmt.Sprintf("val%d", i),
		})
		if err != nil {
			t.Fatalf("save k%d failed: %v", i, err)
		}
	}

	// FIFO order is now: k1 (oldest), k2, k3 (newest)
	// Update k1: this should move k1 to the newest position
	err := store.Save(ctx, &model.TaskResponse{
		TaskID: "k1",
		Output: "val1-updated",
	})
	if err != nil {
		t.Fatalf("update k1 failed: %v", err)
	}

	// FIFO order should now be: k2 (oldest), k3, k1 (newest)
	// Add k4: this should evict k2!
	err = store.Save(ctx, &model.TaskResponse{
		TaskID: "k4",
		Output: "val4",
	})
	if err != nil {
		t.Fatalf("save k4 failed: %v", err)
	}

	// k2 should be evicted
	_, err = store.Load(ctx, "k2")
	if !errors.Is(err, storage.ErrTaskNotFound) {
		t.Fatalf("expected k2 to be evicted, got error: %v", err)
	}

	// k1, k3, k4 should remain
	for _, key := range []string{"k1", "k3", "k4"} {
		got, err := store.Load(ctx, key)
		if err != nil {
			t.Fatalf("expected %s to be present, got error: %v", key, err)
		}
		if key == "k1" && got.Output != "val1-updated" {
			t.Fatalf("expected updated value for k1, got %v", got.Output)
		}
	}
}

func TestMemoryTaskStore_CapacityWhenMultiIDSaveExceedsLimit(t *testing.T) {
	t.Run("single save with more keys than capacity returns ErrCapacityExceeded without store mutation", func(t *testing.T) {
		// Capacity = 2
		store := storage.NewMemoryTaskStore(2)
		ctx := context.Background()

		// Pre-populate with an entry
		prior := &model.TaskResponse{
			TaskID: "prior-task",
			Output: "prior-output",
		}
		if err := store.Save(ctx, prior); err != nil {
			t.Fatalf("pre-population failed: %v", err)
		}

		resp := &model.TaskResponse{
			TaskID: "canonical",
			Output: "data",
		}
		// 3 unique keys: canonical, alias-1, alias-2 with store capacity 2
		err := store.Save(ctx, resp, "alias-1", "alias-2")
		if !errors.Is(err, storage.ErrCapacityExceeded) {
			t.Fatalf("expected ErrCapacityExceeded, got %v", err)
		}

		// Store must be completely unchanged
		if store.Len() != 1 {
			t.Fatalf("expected store length to remain 1, got %d", store.Len())
		}

		gotPrior, err := store.Load(ctx, "prior-task")
		if err != nil {
			t.Fatalf("expected prior-task to remain intact, got err: %v", err)
		}
		if gotPrior.Output != "prior-output" {
			t.Fatalf("expected prior-output, got %v", gotPrior.Output)
		}

		// None of the rejected keys should exist
		for _, k := range []string{"canonical", "alias-1", "alias-2"} {
			_, err := store.Load(ctx, k)
			if !errors.Is(err, storage.ErrTaskNotFound) {
				t.Fatalf("expected key %s to not exist, got %v", k, err)
			}
		}
	})

	t.Run("multi-ID save evicts older keys from previous saves", func(t *testing.T) {
		// Capacity = 2
		store := storage.NewMemoryTaskStore(2)
		ctx := context.Background()

		// Save old-1 and old-2
		_ = store.Save(ctx, &model.TaskResponse{TaskID: "old-1"})
		_ = store.Save(ctx, &model.TaskResponse{TaskID: "old-2"})

		// Save new-1 with alias-1 (2 keys)
		err := store.Save(ctx, &model.TaskResponse{TaskID: "new-1"}, "alias-1")
		if err != nil {
			t.Fatalf("save failed: %v", err)
		}

		if store.Len() != 2 {
			t.Fatalf("expected store length 2, got %d", store.Len())
		}

		// Both old-1 and old-2 should be evicted
		for _, k := range []string{"old-1", "old-2"} {
			_, err := store.Load(ctx, k)
			if !errors.Is(err, storage.ErrTaskNotFound) {
				t.Fatalf("expected %s to be evicted, got %v", k, err)
			}
		}

		// new-1 and alias-1 should be present
		for _, k := range []string{"new-1", "alias-1"} {
			_, err := store.Load(ctx, k)
			if err != nil {
				t.Fatalf("expected %s to be present, got %v", k, err)
			}
		}
	})

	t.Run("existing key updated in multi-ID save is preserved over older key", func(t *testing.T) {
		// Capacity = 2
		store := storage.NewMemoryTaskStore(2)
		ctx := context.Background()

		_ = store.Save(ctx, &model.TaskResponse{TaskID: "k1"})
		_ = store.Save(ctx, &model.TaskResponse{TaskID: "k2"})

		// Save new task with canonical "k3" and alias "k1" (k1 exists!)
		err := store.Save(ctx, &model.TaskResponse{TaskID: "k3", Output: "v3"}, "k1")
		if err != nil {
			t.Fatalf("save failed: %v", err)
		}

		if store.Len() != 2 {
			t.Fatalf("expected store length 2, got %d", store.Len())
		}

		// k2 was not in this save and was older than k1, so k2 should be evicted
		_, err = store.Load(ctx, "k2")
		if !errors.Is(err, storage.ErrTaskNotFound) {
			t.Fatalf("expected k2 to be evicted, got %v", err)
		}

		// Both k1 and k3 should be present with the new task response
		for _, k := range []string{"k1", "k3"} {
			got, err := store.Load(ctx, k)
			if err != nil {
				t.Fatalf("expected %s to be present, got %v", k, err)
			}
			if got.Output != "v3" {
				t.Fatalf("unexpected output for %s: %v", k, got.Output)
			}
		}
	})
}

func TestMemoryTaskStore_ContextCancellation(t *testing.T) {
	store := storage.NewMemoryTaskStore(10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Save with canceled context
	err := store.Save(ctx, &model.TaskResponse{TaskID: "t1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on Save, got %v", err)
	}

	// Load with canceled context
	_, err = store.Load(ctx, "t1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on Load, got %v", err)
	}
}

func TestMemoryTaskStore_ConcurrentRace(t *testing.T) {
	store := storage.NewMemoryTaskStore(50)
	ctx := context.Background()

	const numWorkers = 20
	const numOps = 100

	var wg sync.WaitGroup
	wg.Add(numWorkers * 2)

	// Writers
	for w := 0; w < numWorkers; w++ {
		workerID := w
		go func() {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				taskID := fmt.Sprintf("task-%d-%d", workerID, i%10)
				alias := fmt.Sprintf("alias-%d-%d", workerID, i%10)
				_ = store.Save(ctx, &model.TaskResponse{
					TaskID:    taskID,
					AgentID:   fmt.Sprintf("agent-%d", workerID),
					Status:    model.StatusCompleted,
					Output:    fmt.Sprintf("output-%d-%d", workerID, i),
					Timestamp: time.Now().UTC(),
				}, alias)
			}
		}()
	}

	// Readers
	for r := 0; r < numWorkers; r++ {
		readerID := r
		go func() {
			defer wg.Done()
			for i := 0; i < numOps; i++ {
				targetID := fmt.Sprintf("task-%d-%d", readerID%numWorkers, i%10)
				resp, err := store.Load(ctx, targetID)
				if err != nil && !errors.Is(err, storage.ErrTaskNotFound) {
					t.Errorf("unexpected error on Load: %v", err)
				}
				if err == nil && resp == nil {
					t.Errorf("expected non-nil response when err is nil")
				}
			}
		}()
	}

	wg.Wait()

	if store.Len() > 50 {
		t.Fatalf("store length %d exceeded maxTasks 50", store.Len())
	}
}
