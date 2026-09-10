package storage

import (
	"context"
	"errors"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
)

// ErrTaskNotFound is returned when a requested task is not found in the store.
var ErrTaskNotFound = errors.New("task not found")

// ErrNilTaskResponse is returned when attempting to save a nil TaskResponse.
var ErrNilTaskResponse = errors.New("task response cannot be nil")

// ErrEmptyTaskID is returned when attempting to save a TaskResponse with an empty TaskID.
var ErrEmptyTaskID = errors.New("canonical task ID cannot be empty")

// ErrCapacityExceeded is returned when a Save operation contains more unique IDs than the store's maximum capacity.
var ErrCapacityExceeded = errors.New("capacity exceeded: keys to save exceed store capacity")

// TaskStore defines the storage interface for task responses.
type TaskStore interface {
	// Save atomically stores a TaskResponse under its canonical TaskID and zero or more alias IDs.
	Save(ctx context.Context, resp *model.TaskResponse, aliases ...string) error

	// Load retrieves a TaskResponse by its canonical TaskID or alias ID.
	// Returns ErrTaskNotFound if the task does not exist.
	Load(ctx context.Context, id string) (*model.TaskResponse, error)
}

// HealthChecker defines an interface for storage backends that support health checking via Ping.
type HealthChecker interface {
	Ping(ctx context.Context) error
}
