package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTaskRequestSerialization(t *testing.T) {
	req := TaskRequest{
		ID:                 "task-123",
		AgentID:            "researcher",
		RequiredCapability: "web_search",
		Input:              "Find latest AI papers",
		Context:            map[string]any{"user_id": "u42"},
		Stream:             true,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var parsed TaskRequest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if parsed.ID != req.ID || parsed.AgentID != req.AgentID || parsed.Stream != true {
		t.Errorf("unmarshaled struct mismatch: %+v", parsed)
	}
}

func TestA2AErrorSerialization(t *testing.T) {
	errObj := NewA2AError("DOWNSTREAM_TIMEOUT", "Agent timed out", "agent-1", 504)
	data, err := json.Marshal(errObj)
	if err != nil {
		t.Fatalf("marshal error failed: %v", err)
	}

	var parsed A2AErrorEnvelope
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal error envelope failed: %v", err)
	}

	if parsed.Error.Code != "DOWNSTREAM_TIMEOUT" || parsed.Error.Status != 504 {
		t.Errorf("parsed error mismatch: %+v", parsed.Error)
	}

	errStr := parsed.Error.Error()
	if !strings.Contains(errStr, "DOWNSTREAM_TIMEOUT") || !strings.Contains(errStr, "Agent timed out") {
		t.Errorf("expected Error() string to contain code and message, got: %s", errStr)
	}
}

func TestAgentCardAndListResponseSerialization(t *testing.T) {
	resp := AgentListResponse{
		Agents: []AgentCard{
			{
				ID:           "agent-1",
				Name:         "Agent One",
				Description:  "First agent",
				Capabilities: []string{"search", "summarize"},
				Tags:         []string{"fast"},
			},
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var parsed AgentListResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(parsed.Agents) != 1 || parsed.Agents[0].ID != "agent-1" || parsed.Agents[0].Capabilities[0] != "search" {
		t.Errorf("unmarshaled agent list mismatch: %+v", parsed)
	}
}

func TestTaskResponseSerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	errMsg := "something went wrong"
	res := TaskResponse{
		TaskID:  "task-456",
		AgentID: "coder",
		Status:  StatusCompleted,
		Output:  map[string]any{"result": "success"},
		Artifacts: []Artifact{
			{
				Name:     "output.txt",
				MimeType: "text/plain",
				URI:      "file:///tmp/output.txt",
				Data:     "hello world",
			},
		},
		Error:     &errMsg,
		Timestamp: now,
	}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var parsed TaskResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if parsed.TaskID != "task-456" || parsed.Status != StatusCompleted || len(parsed.Artifacts) != 1 {
		t.Errorf("unmarshaled task response mismatch: %+v", parsed)
	}
	if parsed.Error == nil || *parsed.Error != errMsg {
		t.Errorf("expected error message %q, got %+v", errMsg, parsed.Error)
	}
}

func TestStreamEventSerialization(t *testing.T) {
	evt := StreamEvent{
		Type: EventTokenDelta,
		Data: map[string]any{"token": "hello"},
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var parsed StreamEvent
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if parsed.Type != EventTokenDelta {
		t.Errorf("expected event type %s, got %s", EventTokenDelta, parsed.Type)
	}
}
