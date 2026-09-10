package storage

import (
	"container/list"
	"context"
	"sync"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
)

// DefaultMaxTasks is the default maximum number of cached task responses.
const DefaultMaxTasks = 10000

var _ TaskStore = (*MemoryTaskStore)(nil)

// MemoryTaskStore is a bounded, FIFO in-memory implementation of TaskStore.
// It is safe for concurrent use.
type MemoryTaskStore struct {
	mu       sync.RWMutex
	maxTasks int
	tasks    map[string]*list.Element
	ll       *list.List
}

type taskItem struct {
	id   string
	resp *model.TaskResponse
}

// NewMemoryTaskStore creates a new bounded memory task store with the given maximum capacity.
// If maxTasks is non-positive, it defaults to DefaultMaxTasks (10,000).
func NewMemoryTaskStore(maxTasks int) *MemoryTaskStore {
	if maxTasks <= 0 {
		maxTasks = DefaultMaxTasks
	}
	return &MemoryTaskStore{
		maxTasks: maxTasks,
		tasks:    make(map[string]*list.Element),
		ll:       list.New(),
	}
}

// Save atomically stores a TaskResponse under its canonical TaskID and zero or more alias IDs.
// It rejects nil responses and empty canonical TaskIDs with an error.
// Canonical ID and aliases are deduplicated before capacity accounting, and empty aliases are ignored.
// If updating an existing key, the key is moved to the newest FIFO position.
// The store never retains more keys than its configured maximum.
func (s *MemoryTaskStore) Save(ctx context.Context, resp *model.TaskResponse, aliases ...string) error {
	if resp == nil {
		return ErrNilTaskResponse
	}
	if resp.TaskID == "" {
		return ErrEmptyTaskID
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Deduplicate canonical ID and aliases before capacity accounting.
	// Empty aliases are ignored.
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

	s.mu.Lock()
	defer s.mu.Unlock()

	// If the batch of unique IDs to save exceeds the store's max capacity,
	// reject without mutating the store so atomic save semantics are preserved.
	if len(keys) > s.maxTasks {
		return ErrCapacityExceeded
	}

	// Update existing keys first and move them to the newest FIFO position (back).
	newKeys := make([]string, 0, len(keys))
	for _, k := range keys {
		if elem, ok := s.tasks[k]; ok {
			elem.Value = taskItem{id: k, resp: resp}
			s.ll.MoveToBack(elem)
		} else {
			newKeys = append(newKeys, k)
		}
	}

	// Evict oldest entries until there is enough capacity for new keys.
	for len(s.tasks)+len(newKeys) > s.maxTasks && s.ll.Len() > 0 {
		front := s.ll.Front()
		if front == nil {
			break
		}
		item := front.Value.(taskItem)
		delete(s.tasks, item.id)
		s.ll.Remove(front)
	}

	// Insert new keys into the newest FIFO position (back).
	for _, k := range newKeys {
		elem := s.ll.PushBack(taskItem{id: k, resp: resp})
		s.tasks[k] = elem
	}

	return nil
}

// Load retrieves a TaskResponse by its canonical TaskID or alias ID.
// If the task does not exist, ErrTaskNotFound is returned.
func (s *MemoryTaskStore) Load(ctx context.Context, id string) (*model.TaskResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == "" {
		return nil, ErrTaskNotFound
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	elem, ok := s.tasks[id]
	if !ok {
		return nil, ErrTaskNotFound
	}
	return elem.Value.(taskItem).resp, nil
}

// Len returns the current number of keys in the store.
func (s *MemoryTaskStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tasks)
}
