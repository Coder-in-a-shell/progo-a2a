package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
)

func requestWithCredentials(req *http.Request, allowed ...string) *http.Request {
	creds := ClientCredentials{ClientID: "test-client", AllowedAgents: allowed}
	ctx := context.WithValue(req.Context(), ClientCredentialsKey, creds)
	return req.WithContext(ctx)
}

func TestGetTaskEnforcesAgentAllowlist(t *testing.T) {
	h := NewA2AHandler(nil, nil)
	h.StoreTask(&model.TaskResponse{TaskID: "task-a", AgentID: "agent-a", Status: model.StatusCompleted})

	req := httptest.NewRequest(http.MethodGet, "/a2a/v1/tasks/task-a", nil)
	req.SetPathValue("task_id", "task-a")
	req = requestWithCredentials(req, "agent-b")
	rec := httptest.NewRecorder()

	h.GetTask(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestListAgentsFiltersByAllowlist(t *testing.T) {
	h := NewA2AHandler(&config.Config{Agents: []config.AgentConfig{
		{ID: "agent-a", Name: "A"},
		{ID: "agent-b", Name: "B"},
	}}, nil)
	req := requestWithCredentials(httptest.NewRequest(http.MethodGet, "/a2a/v1/agents", nil), "agent-b")
	rec := httptest.NewRecorder()

	h.ListAgents(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var got model.AgentListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Agents) != 1 || got.Agents[0].ID != "agent-b" {
		t.Fatalf("agents = %#v, want only agent-b", got.Agents)
	}
}
