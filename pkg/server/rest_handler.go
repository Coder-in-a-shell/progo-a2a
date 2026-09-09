package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"a2a-proxy/pkg/config"
	"a2a-proxy/pkg/dispatcher"
	"a2a-proxy/pkg/metrics"
	"a2a-proxy/pkg/model"
	"a2a-proxy/pkg/stream"
)

// RESTHandler provides simplified REST endpoints for direct agent invocation and streaming.
type RESTHandler struct {
	cfg             *config.Config
	disp            *dispatcher.Dispatcher
	a2aHandler      *A2AHandler
	metricsRegistry *metrics.Registry
}

// NewRESTHandler creates a new RESTHandler.
func NewRESTHandler(cfg *config.Config, disp *dispatcher.Dispatcher, a2aHandler *A2AHandler, reg ...*metrics.Registry) *RESTHandler {
	var m *metrics.Registry
	if len(reg) > 0 {
		m = reg[0]
	}
	return &RESTHandler{
		cfg:             cfg,
		disp:            disp,
		a2aHandler:      a2aHandler,
		metricsRegistry: m,
	}
}

// SetMetricsRegistry configures the metrics registry used by the handler.
func (h *RESTHandler) SetMetricsRegistry(reg *metrics.Registry) {
	h.metricsRegistry = reg
}

func (h *RESTHandler) getMetrics() *metrics.Registry {
	if h.metricsRegistry != nil {
		return h.metricsRegistry
	}
	return metrics.DefaultRegistry
}


// InvokeAgent handles POST /api/v1/invoke/{agent_id}.
func (h *RESTHandler) InvokeAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if agentID == "" {
		agentID = extractAgentID(r)
	}

	// Verify agent exists
	if _, err := h.disp.GetAgent(agentID); err != nil {
		var a2aErr *model.A2AError
		if errors.As(err, &a2aErr) {
			writeA2AError(w, a2aErr.Code, a2aErr.Message, a2aErr.AgentID, a2aErr.Status)
			return
		}
		writeA2AError(w, "AGENT_NOT_FOUND", fmt.Sprintf("agent '%s' not found", agentID), agentID, http.StatusNotFound)
		return
	}

	// RBAC verification
	if creds, ok := GetClientCredentials(r.Context()); ok {
		if !creds.Allows(agentID) {
			writeA2AError(w, "FORBIDDEN", fmt.Sprintf("Access to agent '%s' is forbidden", agentID), agentID, http.StatusForbidden)
			return
		}
	}

	// Parse body into task input
	task, err := h.buildTaskFromBody(r, agentID)
	if err != nil {
		writeA2AError(w, "INVALID_REQUEST", err.Error(), agentID, http.StatusBadRequest)
		return
	}

	resp, err := h.disp.Dispatch(r.Context(), task)
	if err != nil {
		var a2aErr *model.A2AError
		if errors.As(err, &a2aErr) {
			writeA2AError(w, a2aErr.Code, a2aErr.Message, a2aErr.AgentID, a2aErr.Status)
			return
		}
		writeA2AError(w, "INTERNAL_ERROR", err.Error(), agentID, http.StatusInternalServerError)
		return
	}

	if h.a2aHandler != nil {
		h.a2aHandler.StoreTask(resp)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// StreamAgent handles POST /api/v1/stream/{agent_id}.
func (h *RESTHandler) StreamAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agent_id")
	if agentID == "" {
		agentID = extractAgentID(r)
	}

	// Verify agent exists
	if _, err := h.disp.GetAgent(agentID); err != nil {
		var a2aErr *model.A2AError
		if errors.As(err, &a2aErr) {
			writeA2AError(w, a2aErr.Code, a2aErr.Message, a2aErr.AgentID, a2aErr.Status)
			return
		}
		writeA2AError(w, "AGENT_NOT_FOUND", fmt.Sprintf("agent '%s' not found", agentID), agentID, http.StatusNotFound)
		return
	}

	// RBAC verification before creating SSE writer
	if creds, ok := GetClientCredentials(r.Context()); ok {
		if !creds.Allows(agentID) {
			writeA2AError(w, "FORBIDDEN", fmt.Sprintf("Access to agent '%s' is forbidden", agentID), agentID, http.StatusForbidden)
			return
		}
	}

	// Parse body into task input
	task, err := h.buildTaskFromBody(r, agentID)
	if err != nil {
		writeA2AError(w, "INVALID_REQUEST", err.Error(), agentID, http.StatusBadRequest)
		return
	}
	task.Stream = true

	// Clear write deadline for SSE streams
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	sseWriter, err := stream.NewSSEResponseWriter(w)
	if err != nil {
		writeA2AError(w, "STREAM_UNSUPPORTED", err.Error(), agentID, http.StatusInternalServerError)
		return
	}

	h.getMetrics().IncActiveStreams()
	defer h.getMetrics().DecActiveStreams()

	if err := h.disp.DispatchStream(r.Context(), task, sseWriter); err != nil {
		_ = sseWriter.Emit(model.EventTaskError, map[string]string{
			"error": err.Error(),
		})
	}
}

func (h *RESTHandler) buildTaskFromBody(r *http.Request, agentID string) (*model.TaskRequest, error) {
	limitedBody := io.LimitReader(r.Body, 10*1024*1024)
	bodyBytes, err := io.ReadAll(limitedBody)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	var input any
	var taskCtx map[string]any
	var meta map[string]any

	if len(bodyBytes) > 0 {
		var raw any
		if err := json.Unmarshal(bodyBytes, &raw); err != nil {
			return nil, fmt.Errorf("failed to parse JSON body: %w", err)
		}

		if rawMap, ok := raw.(map[string]any); ok {
			if inVal, hasInput := rawMap["input"]; hasInput {
				input = inVal
				if cVal, hasCtx := rawMap["context"].(map[string]any); hasCtx {
					taskCtx = cVal
				}
				if mVal, hasMeta := rawMap["metadata"].(map[string]any); hasMeta {
					meta = mVal
				}
			} else {
				input = raw
			}
		} else {
			input = raw
		}
	}

	return &model.TaskRequest{
		ID:       generateTaskID(),
		AgentID:  agentID,
		Input:    input,
		Context:  taskCtx,
		Metadata: meta,
	}, nil
}
