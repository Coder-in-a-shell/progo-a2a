package adapter

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"

	"a2a-proxy/pkg/config"
	"a2a-proxy/pkg/model"
	"a2a-proxy/pkg/stream"
)

type mockEmitter struct {
	events []model.StreamEvent
}

var _ stream.Emitter = (*mockEmitter)(nil)

func (m *mockEmitter) Emit(eventType model.StreamEventType, data any) error {
	m.events = append(m.events, model.StreamEvent{
		Type: eventType,
		Data: data,
	})
	return nil
}

func TestCustomAdapterRequestTranslation(t *testing.T) {
	adapter := NewCustomAdapter()
	agent := &config.AgentConfig{
		ID:       "support-bot",
		Type:     "custom",
		Endpoint: "http://example.com/api/query",
		Auth: config.AuthConfig{
			Type:        "header",
			HeaderName:  "X-Custom-Token",
			HeaderValue: "tok-abc",
		},
		Mapping: &config.CustomMapping{
			Request: config.RequestTemplate{
				Method: "POST",
				Headers: map[string]string{
					"Content-Type": "application/json",
				},
				BodyTemplate: `{"query": {{ .Task.Input | toJson }}, "session": {{ .Task.ID | toJson }}}`,
			},
		},
	}

	task := &model.TaskRequest{
		ID:    "t-100",
		Input: "Hello Support",
	}

	httpReq, err := adapter.TranslateRequest(context.Background(), agent, task)
	if err != nil {
		t.Fatalf("request translation failed: %v", err)
	}

	if httpReq.Method != "POST" {
		t.Errorf("expected POST, got %s", httpReq.Method)
	}
	if httpReq.Header.Get("X-Custom-Token") != "tok-abc" {
		t.Errorf("missing auth header")
	}
	if httpReq.Header.Get("Content-Type") != "application/json" {
		t.Errorf("missing Content-Type header")
	}

	body, _ := io.ReadAll(httpReq.Body)
	if !bytes.Contains(body, []byte(`"query": "Hello Support"`)) {
		t.Errorf("body mismatch: %s", string(body))
	}
}

func TestCustomAdapterRequestTranslation_BearerAuthAndDefaults(t *testing.T) {
	adapter := NewCustomAdapter()
	os.Setenv("TEST_CUSTOM_ENV_VAR", "env-secret-123")
	defer os.Unsetenv("TEST_CUSTOM_ENV_VAR")

	agent := &config.AgentConfig{
		ID:       "bearer-bot",
		Type:     "custom",
		Endpoint: "http://example.com/api/v1",
		Auth: config.AuthConfig{
			Type:  "bearer",
			Token: "bearer-xyz",
		},
		Mapping: &config.CustomMapping{
			Request: config.RequestTemplate{
				BodyTemplate: `{"fallback": {{ default "default-value" "" | toJson }}, "env": {{ env "TEST_CUSTOM_ENV_VAR" | toJson }}}`,
			},
		},
	}

	task := &model.TaskRequest{
		ID:    "t-101",
		Input: "test input",
	}

	httpReq, err := adapter.TranslateRequest(context.Background(), agent, task)
	if err != nil {
		t.Fatalf("request translation failed: %v", err)
	}

	if httpReq.Method != "POST" { // default method should be POST
		t.Errorf("expected default method POST, got %s", httpReq.Method)
	}
	if httpReq.Header.Get("Authorization") != "Bearer bearer-xyz" {
		t.Errorf("expected Bearer auth header, got %s", httpReq.Header.Get("Authorization"))
	}

	body, _ := io.ReadAll(httpReq.Body)
	expectedJSON := `{"fallback": "default-value", "env": "env-secret-123"}`
	if !bytes.Contains(body, []byte(`"fallback": "default-value"`)) || !bytes.Contains(body, []byte(`"env": "env-secret-123"`)) {
		t.Errorf("expected body to contain default and env values, got %s (expected %s)", string(body), expectedJSON)
	}
}

func TestCustomAdapterRequestTranslation_MissingMapping(t *testing.T) {
	adapter := NewCustomAdapter()
	agent := &config.AgentConfig{
		ID:       "no-mapping-bot",
		Type:     "custom",
		Endpoint: "http://example.com/api",
		Mapping:  nil,
	}

	task := &model.TaskRequest{ID: "t-1"}
	_, err := adapter.TranslateRequest(context.Background(), agent, task)
	if err == nil {
		t.Fatalf("expected error for nil mapping, got nil")
	}
}

func TestCustomAdapterResponseTranslation(t *testing.T) {
	adapter := NewCustomAdapter()
	agent := &config.AgentConfig{
		ID:   "support-bot",
		Type: "custom",
		Mapping: &config.CustomMapping{
			Response: config.ResponseMapping{
				OutputPath: "data.answer",
				StatusPath: "data.state",
			},
		},
	}

	rawResp := `{"data": {"answer": "Your ticket is resolved", "state": "success"}}`
	httpResp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(rawResp)),
	}

	resp, err := adapter.TranslateResponse(context.Background(), agent, httpResp)
	if err != nil {
		t.Fatalf("response translation failed: %v", err)
	}

	if resp.Status != model.StatusCompleted {
		t.Errorf("expected completed, got %v", resp.Status)
	}
	if resp.Output != "Your ticket is resolved" {
		t.Errorf("expected answer, got %v", resp.Output)
	}
	if resp.AgentID != "support-bot" {
		t.Errorf("expected agent ID support-bot, got %s", resp.AgentID)
	}
}

func TestCustomAdapterResponseTranslation_ErrorStatusCode(t *testing.T) {
	adapter := NewCustomAdapter()
	agent := &config.AgentConfig{
		ID:   "error-bot",
		Type: "custom",
		Mapping: &config.CustomMapping{
			Response: config.ResponseMapping{
				ErrorPath: "error.message",
			},
		},
	}

	rawResp := `{"error": {"message": "Invalid API token"}}`
	httpResp := &http.Response{
		StatusCode: 401,
		Body:       io.NopCloser(bytes.NewBufferString(rawResp)),
	}

	resp, err := adapter.TranslateResponse(context.Background(), agent, httpResp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Status != model.StatusFailed {
		t.Errorf("expected failed status, got %v", resp.Status)
	}
	if resp.Error == nil || *resp.Error != "Invalid API token" {
		t.Errorf("expected error message 'Invalid API token', got %v", resp.Error)
	}
}

func TestCustomAdapterResponseTranslation_FailedStatusInBody(t *testing.T) {
	adapter := NewCustomAdapter()
	agent := &config.AgentConfig{
		ID:   "fail-bot",
		Type: "custom",
		Mapping: &config.CustomMapping{
			Response: config.ResponseMapping{
				StatusPath: "status",
				ErrorPath:  "details",
			},
		},
	}

	rawResp := `{"status": "FAILED", "details": "Something went wrong"}`
	httpResp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(rawResp)),
	}

	resp, err := adapter.TranslateResponse(context.Background(), agent, httpResp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Status != model.StatusFailed {
		t.Errorf("expected status FAILED, got %v", resp.Status)
	}
	if resp.Error == nil || *resp.Error != "Something went wrong" {
		t.Errorf("expected error details, got %v", resp.Error)
	}
}

func TestCustomAdapterResponseTranslation_Artifacts(t *testing.T) {
	adapter := NewCustomAdapter()
	agent := &config.AgentConfig{
		ID:   "artifact-bot",
		Type: "custom",
		Mapping: &config.CustomMapping{
			Response: config.ResponseMapping{
				OutputPath:    "result",
				ArtifactsPath: "artifacts",
			},
		},
	}

	rawResp := `{"result": "Report generated", "artifacts": [{"name": "chart.png", "mime_type": "image/png", "uri": "http://example.com/chart.png"}]}`
	httpResp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(rawResp)),
	}

	resp, err := adapter.TranslateResponse(context.Background(), agent, httpResp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(resp.Artifacts))
	}
	if resp.Artifacts[0].Name != "chart.png" || resp.Artifacts[0].MimeType != "image/png" {
		t.Errorf("unexpected artifact: %+v", resp.Artifacts[0])
	}
}

func TestCustomAdapterStreamTranslation(t *testing.T) {
	adapter := NewCustomAdapter()
	agent := &config.AgentConfig{
		ID:   "stream-bot",
		Type: "custom",
		Mapping: &config.CustomMapping{
			Stream: config.StreamMapping{
				DataPath:     "delta.text",
				DoneSentinel: "[DONE]",
			},
		},
	}

	sseData := "data: {\"delta\": {\"text\": \"Hello \"}}\n\ndata: {\"delta\": {\"text\": \"World!\"}}\n\ndata: [DONE]\n\n"
	httpResp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(sseData)),
	}

	emitter := &mockEmitter{}
	err := adapter.TranslateStream(context.Background(), agent, httpResp, emitter)
	if err != nil {
		t.Fatalf("stream translation failed: %v", err)
	}

	if len(emitter.events) != 4 {
		t.Fatalf("expected 4 events, got %d: %+v", len(emitter.events), emitter.events)
	}

	if emitter.events[0].Type != model.EventTaskStarted {
		t.Errorf("expected first event to be task_started, got %v", emitter.events[0].Type)
	}

	if emitter.events[1].Type != model.EventTokenDelta {
		t.Errorf("expected second event to be token_delta, got %v", emitter.events[1].Type)
	}
	d1, ok := emitter.events[1].Data.(map[string]any)
	if !ok || d1["delta"] != "Hello " {
		t.Errorf("expected delta 'Hello ', got %v", emitter.events[1].Data)
	}

	if emitter.events[2].Type != model.EventTokenDelta {
		t.Errorf("expected third event to be token_delta, got %v", emitter.events[2].Type)
	}
	d2, ok := emitter.events[2].Data.(map[string]any)
	if !ok || d2["delta"] != "World!" {
		t.Errorf("expected delta 'World!', got %v", emitter.events[2].Data)
	}

	if emitter.events[3].Type != model.EventTaskCompleted {
		t.Errorf("expected fourth event to be task_completed, got %v", emitter.events[3].Type)
	}
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry()
	custom := NewCustomAdapter()

	reg.Register(custom)

	got, err := reg.Get("custom")
	if err != nil {
		t.Fatalf("expected to get custom adapter: %v", err)
	}
	if got.Type() != "custom" {
		t.Errorf("expected type 'custom', got %s", got.Type())
	}

	_, err = reg.Get("nonexistent")
	if err == nil {
		t.Fatalf("expected error for nonexistent adapter, got nil")
	}
}
