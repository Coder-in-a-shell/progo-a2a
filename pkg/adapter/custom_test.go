package adapter

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"

	"a2a-proxy/pkg/config"
	"a2a-proxy/pkg/model"
	"a2a-proxy/pkg/stream"
)

type mockEmitter struct {
	mu     sync.Mutex
	events []model.StreamEvent
}

var _ stream.Emitter = (*mockEmitter)(nil)

func (m *mockEmitter) Emit(eventType model.StreamEventType, data any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, model.StreamEvent{
		Type: eventType,
		Data: data,
	})
	return nil
}

func (m *mockEmitter) Events() []model.StreamEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]model.StreamEvent, len(m.events))
	copy(copied, m.events)
	return copied
}

func TestNormalizeJSONPath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"$", ""},
		{"$.result.answer", "result.answer"},
		{"result.answer", "result.answer"},
		{"$.choices[0].message.content", "choices.0.message.content"},
		{"choices[0].message.content", "choices.0.message.content"},
		{"$[0].text", "0.text"},
		{"[0].text", "0.text"},
		{"$[0]", "0"},
		{"[0]", "0"},
		{"items[0][1].val", "items.0.1.val"},
		{"$.items.[0].val", "items.0.val"},
		{"$['choices'][0]['message']", "choices.0.message"},
		{`$["choices"][0]["message"]`, "choices.0.message"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := normalizeJSONPath(tc.input)
			if got != tc.expected {
				t.Errorf("normalizeJSONPath(%q) = %q, expected %q", tc.input, got, tc.expected)
			}
		})
	}
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

func TestCustomAdapterRequestTranslation_EmptyBody(t *testing.T) {
	adapter := NewCustomAdapter()
	agent := &config.AgentConfig{
		ID:       "get-bot",
		Type:     "custom",
		Endpoint: "http://example.com/api/status",
		Mapping: &config.CustomMapping{
			Request: config.RequestTemplate{
				Method:       "GET",
				BodyTemplate: "",
			},
		},
	}

	task := &model.TaskRequest{
		ID: "t-get",
	}

	httpReq, err := adapter.TranslateRequest(context.Background(), agent, task)
	if err != nil {
		t.Fatalf("request translation failed: %v", err)
	}

	if httpReq.Method != "GET" {
		t.Errorf("expected GET, got %s", httpReq.Method)
	}
	if httpReq.Body != nil && httpReq.Body != http.NoBody {
		bodyBytes, _ := io.ReadAll(httpReq.Body)
		if len(bodyBytes) > 0 {
			t.Errorf("expected empty/nil body, got %q", string(bodyBytes))
		}
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

	if httpReq.Method != "POST" {
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

func TestCustomAdapter_TemplateCaching(t *testing.T) {
	adapter := NewCustomAdapter()
	tmplStr := `{"id": {{ .Task.ID | toJson }}}`

	tmpl1, err1 := adapter.getTemplate(tmplStr)
	if err1 != nil {
		t.Fatalf("failed to get template 1: %v", err1)
	}

	tmpl2, err2 := adapter.getTemplate(tmplStr)
	if err2 != nil {
		t.Fatalf("failed to get template 2: %v", err2)
	}

	if tmpl1 != tmpl2 {
		t.Errorf("expected same pointer from template cache, got %p and %p", tmpl1, tmpl2)
	}

	// Concurrent usage test
	var wg sync.WaitGroup
	agent := &config.AgentConfig{
		ID:       "cache-bot",
		Type:     "custom",
		Endpoint: "http://example.com/api",
		Mapping: &config.CustomMapping{
			Request: config.RequestTemplate{
				BodyTemplate: tmplStr,
			},
		},
	}

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			task := &model.TaskRequest{ID: fmt.Sprintf("task-%d", idx)}
			req, err := adapter.TranslateRequest(context.Background(), agent, task)
			if err != nil {
				t.Errorf("concurrent TranslateRequest failed: %v", err)
				return
			}
			b, _ := io.ReadAll(req.Body)
			expected := fmt.Sprintf(`{"id": "task-%d"}`, idx)
			if string(b) != expected {
				t.Errorf("expected %s, got %s", expected, string(b))
			}
		}(i)
	}
	wg.Wait()
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

func TestCustomAdapterResponseTranslation_StandardJSONPath(t *testing.T) {
	adapter := NewCustomAdapter()

	// Test case 1: $.result.answer
	agent1 := &config.AgentConfig{
		ID:   "bot-result",
		Type: "custom",
		Mapping: &config.CustomMapping{
			Response: config.ResponseMapping{
				OutputPath: "$.result.answer",
			},
		},
	}
	rawResp1 := `{"result": {"answer": "42"}}`
	httpResp1 := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(rawResp1)),
	}
	resp1, err := adapter.TranslateResponse(context.Background(), agent1, httpResp1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp1.Output != "42" {
		t.Errorf("expected '42', got %v", resp1.Output)
	}

	// Test case 2: $.choices[0].message.content
	agent2 := &config.AgentConfig{
		ID:   "bot-openai",
		Type: "custom",
		Mapping: &config.CustomMapping{
			Response: config.ResponseMapping{
				OutputPath: "$.choices[0].message.content",
			},
		},
	}
	rawResp2 := `{"choices": [{"message": {"content": "Hello from model"}}]}`
	httpResp2 := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(rawResp2)),
	}
	resp2, err := adapter.TranslateResponse(context.Background(), agent2, httpResp2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp2.Output != "Hello from model" {
		t.Errorf("expected 'Hello from model', got %v", resp2.Output)
	}
}

func TestCustomAdapterResponseTranslation_StatusMapping(t *testing.T) {
	adapter := NewCustomAdapter()

	testCases := []struct {
		statusStr      string
		expectedStatus model.TaskStatus
	}{
		{"canceled", model.StatusCanceled},
		{"cancelled", model.StatusCanceled},
		{"CANCELED", model.StatusCanceled},
		{"CANCELLED", model.StatusCanceled},
		{"failed", model.StatusFailed},
		{"error", model.StatusFailed},
		{"running", model.StatusInProgress},
		{"in_progress", model.StatusInProgress},
		{"pending", model.StatusPending},
		{"completed", model.StatusCompleted},
		{"success", model.StatusCompleted},
		{"done", model.StatusCompleted},
	}

	for _, tc := range testCases {
		t.Run(tc.statusStr, func(t *testing.T) {
			agent := &config.AgentConfig{
				ID:   "status-bot",
				Type: "custom",
				Mapping: &config.CustomMapping{
					Response: config.ResponseMapping{
						StatusPath: "$.task.status",
					},
				},
			}
			rawResp := fmt.Sprintf(`{"task": {"status": %q}}`, tc.statusStr)
			httpResp := &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewBufferString(rawResp)),
			}
			resp, err := adapter.TranslateResponse(context.Background(), agent, httpResp)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Status != tc.expectedStatus {
				t.Errorf("status %q: expected %v, got %v", tc.statusStr, tc.expectedStatus, resp.Status)
			}
		})
	}
}

func TestCustomAdapterResponseTranslation_ErrorStatusCode(t *testing.T) {
	adapter := NewCustomAdapter()
	agent := &config.AgentConfig{
		ID:   "error-bot",
		Type: "custom",
		Mapping: &config.CustomMapping{
			Response: config.ResponseMapping{
				ErrorPath: "$.error.message",
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
				ArtifactsPath: "$.artifacts",
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
				DataPath:     "$.delta.text",
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

	events := emitter.Events()
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %+v", len(events), events)
	}

	if events[0].Type != model.EventTaskStarted {
		t.Errorf("expected first event to be task_started, got %v", events[0].Type)
	}

	if events[1].Type != model.EventTokenDelta {
		t.Errorf("expected second event to be token_delta, got %v", events[1].Type)
	}
	d1, ok := events[1].Data.(map[string]any)
	if !ok || d1["delta"] != "Hello " {
		t.Errorf("expected delta 'Hello ', got %v", events[1].Data)
	}

	if events[2].Type != model.EventTokenDelta {
		t.Errorf("expected third event to be token_delta, got %v", events[2].Type)
	}
	d2, ok := events[2].Data.(map[string]any)
	if !ok || d2["delta"] != "World!" {
		t.Errorf("expected delta 'World!', got %v", events[2].Data)
	}

	if events[3].Type != model.EventTaskCompleted {
		t.Errorf("expected fourth event to be task_completed, got %v", events[3].Type)
	}
}

func TestCustomAdapterStreamTranslation_UpstreamHTTPError(t *testing.T) {
	adapter := NewCustomAdapter()
	agent := &config.AgentConfig{
		ID:   "error-stream-bot",
		Type: "custom",
	}

	errPayload := `{"error": "internal server error"}`
	httpResp := &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(bytes.NewBufferString(errPayload)),
	}

	emitter := &mockEmitter{}
	err := adapter.TranslateStream(context.Background(), agent, httpResp, emitter)
	if err == nil {
		t.Fatalf("expected stream translation to fail on status 500")
	}

	expectedErr := "upstream returned status 500: " + errPayload
	if err.Error() != expectedErr {
		t.Errorf("expected error %q, got %q", expectedErr, err.Error())
	}

	events := emitter.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d: %+v", len(events), events)
	}

	if events[0].Type != model.EventTaskError {
		t.Errorf("expected EventTaskError, got %v", events[0].Type)
	}
	data, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any event data, got %T", events[0].Data)
	}
	if data["error"] != expectedErr {
		t.Errorf("expected event error %q, got %q", expectedErr, data["error"])
	}
	if data["agent_id"] != "error-stream-bot" {
		t.Errorf("expected agent_id 'error-stream-bot', got %v", data["agent_id"])
	}
}

func TestCustomAdapterStreamTranslation_LargeLine(t *testing.T) {
	adapter := NewCustomAdapter()
	agent := &config.AgentConfig{
		ID:   "large-stream-bot",
		Type: "custom",
		Mapping: &config.CustomMapping{
			Stream: config.StreamMapping{
				DataPath:     "$.delta.text",
				DoneSentinel: "[DONE]",
			},
		},
	}

	// Create an SSE line with 120KB payload (> 64KB default bufio.MaxScanTokenSize)
	largeText := string(bytes.Repeat([]byte("a"), 120*1024))
	sseData := fmt.Sprintf("data: {\"delta\": {\"text\": %q}}\n\ndata: [DONE]\n\n", largeText)
	httpResp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(sseData)),
	}

	emitter := &mockEmitter{}
	err := adapter.TranslateStream(context.Background(), agent, httpResp, emitter)
	if err != nil {
		t.Fatalf("expected stream to handle large line (>64KB), got error: %v", err)
	}

	events := emitter.Events()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[1].Type != model.EventTokenDelta {
		t.Errorf("expected token delta, got %v", events[1].Type)
	}
	data := events[1].Data.(map[string]any)
	if data["delta"] != largeText {
		t.Errorf("large text mismatch")
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
