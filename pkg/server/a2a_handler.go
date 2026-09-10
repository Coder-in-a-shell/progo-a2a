package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/dispatcher"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/metrics"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/storage"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/stream"
)

// A2AHandler handles standard Agent-to-Agent (A2A) protocol endpoints.
type A2AHandler struct {
	mu              sync.RWMutex
	cfg             *config.Config
	disp            *dispatcher.Dispatcher
	metricsRegistry *metrics.Registry
	tasks           storage.TaskStore
}

// NewA2AHandler creates a new A2AHandler.
func NewA2AHandler(cfg *config.Config, disp *dispatcher.Dispatcher, reg ...*metrics.Registry) *A2AHandler {
	var m *metrics.Registry
	if len(reg) > 0 {
		m = reg[0]
	}
	return &A2AHandler{
		cfg:             cfg,
		disp:            disp,
		metricsRegistry: m,
		tasks:           storage.NewMemoryTaskStore(storage.DefaultMaxTasks),
	}
}

// SetMetricsRegistry configures the metrics registry used by the handler.
func (h *A2AHandler) SetMetricsRegistry(reg *metrics.Registry) {
	h.metricsRegistry = reg
}

// SetMaxTasks configures the maximum number of cached task responses.
func (h *A2AHandler) SetMaxTasks(max int) {
	h.mu.Lock()
	h.tasks = storage.NewMemoryTaskStore(max)
	h.mu.Unlock()
}

// SetTaskStore configures the task store used by the handler. A nil store is ignored.
func (h *A2AHandler) SetTaskStore(store storage.TaskStore) {
	if store == nil {
		return
	}
	h.mu.Lock()
	h.tasks = store
	h.mu.Unlock()
}

func (h *A2AHandler) getTaskStore() storage.TaskStore {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.tasks
}

func (h *A2AHandler) getMetrics() *metrics.Registry {
	if h.metricsRegistry != nil {
		return h.metricsRegistry
	}
	return metrics.DefaultRegistry
}

// StoreTask stores a task response in the task store.
func (h *A2AHandler) StoreTask(resp *model.TaskResponse) {
	if resp == nil || resp.TaskID == "" {
		return
	}
	_ = h.getTaskStore().Save(context.Background(), resp)
}

func (h *A2AHandler) saveTask(ctx context.Context, resp *model.TaskResponse, aliases ...string) error {
	return h.getTaskStore().Save(ctx, resp, aliases...)
}

// ListAgents handles GET /a2a/v1/agents, returning all configured agents as model.AgentListResponse.
func (h *A2AHandler) ListAgents(w http.ResponseWriter, r *http.Request) {
	var cards []model.AgentCard
	if h.cfg != nil {
		cards = make([]model.AgentCard, 0, len(h.cfg.Agents))
		for _, a := range h.cfg.Agents {
			if creds, ok := GetClientCredentials(r.Context()); ok && !creds.Allows(a.ID) {
				continue
			}
			cards = append(cards, model.AgentCard{
				ID:           a.ID,
				Name:         a.Name,
				Description:  a.Description,
				Capabilities: a.Capabilities,
				Tags:         a.Tags,
			})
		}
	} else {
		cards = make([]model.AgentCard, 0)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(model.AgentListResponse{Agents: cards})
}

// GetAgent handles GET /a2a/v1/agents/{id}, returning model.AgentCard or 404 AGENT_NOT_FOUND.
func (h *A2AHandler) GetAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		id = extractAgentID(r)
	}

	agent, err := h.disp.GetAgent(id)
	if err != nil {
		var a2aErr *model.A2AError
		if errors.As(err, &a2aErr) {
			writeA2AError(w, a2aErr.Code, a2aErr.Message, a2aErr.AgentID, a2aErr.Status)
			return
		}
		writeA2AError(w, "AGENT_NOT_FOUND", fmt.Sprintf("agent '%s' not found", id), id, http.StatusNotFound)
		return
	}

	card := model.AgentCard{
		ID:           agent.ID,
		Name:         agent.Name,
		Description:  agent.Description,
		Capabilities: agent.Capabilities,
		Tags:         agent.Tags,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(card)
}

// DispatchTask handles POST /a2a/v1/tasks.
func (h *A2AHandler) DispatchTask(w http.ResponseWriter, r *http.Request) {
	var task model.TaskRequest
	body, err := readRequestBody(r.Body)
	if errors.Is(err, errRequestBodyTooLarge) {
		writeA2AError(w, "REQUEST_TOO_LARGE", err.Error(), "", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		writeA2AError(w, "INVALID_REQUEST", "Failed to read request body: "+err.Error(), "", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(body, &task); err != nil {
		writeA2AError(w, "INVALID_REQUEST", "Failed to parse JSON body: "+err.Error(), "", http.StatusBadRequest)
		return
	}

	if task.ID == "" {
		task.ID = generateTaskID()
	}

	// Check agent RBAC from task payload
	if creds, ok := GetClientCredentials(r.Context()); ok {
		targetAgentID := task.AgentID
		if targetAgentID == "" && task.RequiredCapability != "" {
			resolvedAgent, err := h.disp.FindAgentByCapability(task.RequiredCapability)
			if err != nil {
				var a2aErr *model.A2AError
				if errors.As(err, &a2aErr) {
					writeA2AError(w, a2aErr.Code, a2aErr.Message, a2aErr.AgentID, a2aErr.Status)
				} else {
					writeA2AError(w, "CAPABILITY_NOT_FOUND", err.Error(), "", http.StatusNotFound)
				}
				return
			}
			targetAgentID = resolvedAgent.ID
		}

		if targetAgentID != "" && !creds.Allows(targetAgentID) {
			writeA2AError(w, "FORBIDDEN", fmt.Sprintf("Access to agent '%s' is forbidden", targetAgentID), targetAgentID, http.StatusForbidden)
			return
		}
	}

	resp, err := h.disp.Dispatch(r.Context(), &task)
	if err != nil {
		var a2aErr *model.A2AError
		if errors.As(err, &a2aErr) {
			writeA2AError(w, a2aErr.Code, a2aErr.Message, a2aErr.AgentID, a2aErr.Status)
			return
		}
		writeA2AError(w, "INTERNAL_ERROR", err.Error(), task.AgentID, http.StatusInternalServerError)
		return
	}

	var aliases []string
	if task.ID != "" && resp.TaskID != task.ID {
		aliases = append(aliases, task.ID)
	}

	if err := h.saveTask(r.Context(), resp, aliases...); err != nil {
		slog.Error("failed to persist task response", "task_id", resp.TaskID, "error", err)
		writeA2AError(w, "TASK_STORAGE_UNAVAILABLE", "task storage is temporarily unavailable", task.AgentID, http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// DispatchTaskStream handles POST /a2a/v1/tasks/stream.
func (h *A2AHandler) DispatchTaskStream(w http.ResponseWriter, r *http.Request) {
	var task model.TaskRequest
	body, err := readRequestBody(r.Body)
	if errors.Is(err, errRequestBodyTooLarge) {
		writeA2AError(w, "REQUEST_TOO_LARGE", err.Error(), "", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		writeA2AError(w, "INVALID_REQUEST", "Failed to read request body: "+err.Error(), "", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(body, &task); err != nil {
		writeA2AError(w, "INVALID_REQUEST", "Failed to parse JSON body: "+err.Error(), "", http.StatusBadRequest)
		return
	}

	if task.ID == "" {
		task.ID = generateTaskID()
	}

	// Check agent RBAC from task payload before opening SSE stream
	if creds, ok := GetClientCredentials(r.Context()); ok {
		targetAgentID := task.AgentID
		if targetAgentID == "" && task.RequiredCapability != "" {
			resolvedAgent, err := h.disp.FindAgentByCapability(task.RequiredCapability)
			if err != nil {
				var a2aErr *model.A2AError
				if errors.As(err, &a2aErr) {
					writeA2AError(w, a2aErr.Code, a2aErr.Message, a2aErr.AgentID, a2aErr.Status)
				} else {
					writeA2AError(w, "CAPABILITY_NOT_FOUND", err.Error(), "", http.StatusNotFound)
				}
				return
			}
			targetAgentID = resolvedAgent.ID
		}

		if targetAgentID != "" && !creds.Allows(targetAgentID) {
			writeA2AError(w, "FORBIDDEN", fmt.Sprintf("Access to agent '%s' is forbidden", targetAgentID), targetAgentID, http.StatusForbidden)
			return
		}
	}

	// Clear write deadline for SSE streams
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	sseWriter, err := stream.NewSSEResponseWriter(w)
	if err != nil {
		writeA2AError(w, "STREAM_UNSUPPORTED", err.Error(), task.AgentID, http.StatusInternalServerError)
		return
	}

	h.getMetrics().IncActiveStreams()
	defer h.getMetrics().DecActiveStreams()

	task.Stream = true
	trackedEmitter := &errorTrackingEmitter{Emitter: sseWriter}
	if err := h.disp.DispatchStream(r.Context(), &task, trackedEmitter); err != nil && !trackedEmitter.errorEmitted.Load() {
		_ = trackedEmitter.Emit(model.EventTaskError, map[string]string{
			"error": err.Error(),
		})
	}
}

// GetTask handles GET /a2a/v1/tasks/{task_id}.
func (h *A2AHandler) GetTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	if taskID == "" {
		// Fallback path segment parsing
		cleanPath := r.URL.Path
		idx := len("/a2a/v1/tasks/")
		if len(cleanPath) > idx {
			taskID = cleanPath[idx:]
		}
	}

	val, err := h.getTaskStore().Load(r.Context(), taskID)
	if err != nil {
		if errors.Is(err, storage.ErrTaskNotFound) {
			writeA2AError(w, "TASK_NOT_FOUND", fmt.Sprintf("task '%s' not found", taskID), "", http.StatusNotFound)
			return
		}
		slog.Error("failed to load task response", "task_id", taskID, "error", err)
		writeA2AError(w, "TASK_STORAGE_UNAVAILABLE", "task storage is temporarily unavailable", "", http.StatusServiceUnavailable)
		return
	}

	if creds, authenticated := GetClientCredentials(r.Context()); authenticated && !creds.Allows(val.AgentID) {
		writeA2AError(w, "FORBIDDEN", fmt.Sprintf("Access to agent '%s' is forbidden", val.AgentID), val.AgentID, http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(val)
}

func generateTaskID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("task-%x", b)
}
