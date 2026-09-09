package server

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"a2a-proxy/pkg/config"
	"a2a-proxy/pkg/dispatcher"
	"a2a-proxy/pkg/model"
	"a2a-proxy/pkg/stream"
)

// A2AHandler handles standard Agent-to-Agent (A2A) protocol endpoints.
type A2AHandler struct {
	cfg   *config.Config
	disp  *dispatcher.Dispatcher
	tasks sync.Map // map[string]*model.TaskResponse
}

// NewA2AHandler creates a new A2AHandler.
func NewA2AHandler(cfg *config.Config, disp *dispatcher.Dispatcher) *A2AHandler {
	return &A2AHandler{
		cfg:  cfg,
		disp: disp,
	}
}

// StoreTask stores a task response in the in-memory task map.
func (h *A2AHandler) StoreTask(resp *model.TaskResponse) {
	if resp != nil && resp.TaskID != "" {
		h.tasks.Store(resp.TaskID, resp)
	}
}

// ListAgents handles GET /a2a/v1/agents, returning all configured agents as model.AgentListResponse.
func (h *A2AHandler) ListAgents(w http.ResponseWriter, r *http.Request) {
	var cards []model.AgentCard
	if h.cfg != nil {
		cards = make([]model.AgentCard, 0, len(h.cfg.Agents))
		for _, a := range h.cfg.Agents {
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
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
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

	h.StoreTask(resp)
	if task.ID != "" && resp.TaskID != task.ID {
		h.tasks.Store(task.ID, resp)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// DispatchTaskStream handles POST /a2a/v1/tasks/stream.
func (h *A2AHandler) DispatchTaskStream(w http.ResponseWriter, r *http.Request) {
	var task model.TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&task); err != nil {
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

	sseWriter, err := stream.NewSSEResponseWriter(w)
	if err != nil {
		writeA2AError(w, "STREAM_UNSUPPORTED", err.Error(), task.AgentID, http.StatusInternalServerError)
		return
	}

	task.Stream = true
	if err := h.disp.DispatchStream(r.Context(), &task, sseWriter); err != nil {
		_ = sseWriter.Emit(model.EventTaskError, map[string]string{
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

	if val, ok := h.tasks.Load(taskID); ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(val)
		return
	}

	writeA2AError(w, "TASK_NOT_FOUND", fmt.Sprintf("task '%s' not found", taskID), "", http.StatusNotFound)
}

func generateTaskID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("task-%x", b)
}
