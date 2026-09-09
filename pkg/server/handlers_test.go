package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"a2a-proxy/pkg/adapter"
	"a2a-proxy/pkg/config"
	"a2a-proxy/pkg/dispatcher"
	"a2a-proxy/pkg/metrics"
	"a2a-proxy/pkg/model"
	"a2a-proxy/pkg/stream"
)

type mockAdapter struct {
	adapterType string
}

func (m *mockAdapter) Type() string {
	if m.adapterType != "" {
		return m.adapterType
	}
	return "mock"
}

func (m *mockAdapter) TranslateRequest(ctx context.Context, agent *config.AgentConfig, task *model.TaskRequest) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", "http://mock-agent.internal/task", bytes.NewBufferString(`{}`))
	if err != nil {
		return nil, err
	}
	return req, nil
}

func (m *mockAdapter) TranslateResponse(ctx context.Context, agent *config.AgentConfig, resp *http.Response) (*model.TaskResponse, error) {
	return &model.TaskResponse{
		AgentID:   agent.ID,
		Status:    model.StatusCompleted,
		Output:    map[string]any{"response": "mocked response"},
		Timestamp: time.Now().UTC(),
	}, nil
}

func (m *mockAdapter) TranslateStream(ctx context.Context, agent *config.AgentConfig, resp *http.Response, emitter stream.Emitter) error {
	_ = emitter.Emit(model.EventTaskStarted, map[string]string{"task_id": "mock-stream-id"})
	_ = emitter.Emit(model.EventTokenDelta, map[string]string{"delta": "hello stream"})
	_ = emitter.Emit(model.EventTaskCompleted, map[string]string{"status": "COMPLETED"})
	return nil
}

type mockTransport struct {
	fn func(req *http.Request) (*http.Response, error)
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if m.fn != nil {
		return m.fn(req)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
	}, nil
}

func setupTestRouter(cfg *config.Config) (http.Handler, *dispatcher.Dispatcher, *metrics.Registry) {
	reg := adapter.NewRegistry()
	reg.Register(&mockAdapter{adapterType: "mock"})

	disp := dispatcher.New(cfg, reg)
	disp.SetHTTPClient(&http.Client{
		Transport: &mockTransport{},
	})

	metricsReg := metrics.NewRegistry()
	router := SetupRouter(cfg, disp, WithMetricsRegistry(metricsReg))
	return router, disp, metricsReg
}

func TestA2AAgentsListHandler(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Description: "First bot", Capabilities: []string{"c1"}, Tags: []string{"fast"}},
			{ID: "bot-2", Name: "Bot 2", Description: "Second bot", Capabilities: []string{"c2"}},
		},
	}
	router, _, _ := setupTestRouter(cfg)

	req := httptest.NewRequest("GET", "/a2a/v1/agents", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp model.AgentListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(resp.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(resp.Agents))
	}
	if resp.Agents[0].ID != "bot-1" || resp.Agents[0].Name != "Bot 1" {
		t.Errorf("agent 0 mismatch: %+v", resp.Agents[0])
	}
	if resp.Agents[1].ID != "bot-2" {
		t.Errorf("agent 1 mismatch: %+v", resp.Agents[1])
	}
}

func TestA2AAgentGetHandler(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Description: "First bot", Capabilities: []string{"c1"}},
		},
	}
	router, _, _ := setupTestRouter(cfg)

	// Case 1: Existing agent
	req1 := httptest.NewRequest("GET", "/a2a/v1/agents/bot-1", nil)
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}

	var card model.AgentCard
	if err := json.NewDecoder(rec1.Body).Decode(&card); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if card.ID != "bot-1" || card.Name != "Bot 1" {
		t.Errorf("agent card mismatch: %+v", card)
	}

	// Case 2: Non-existent agent -> 404
	req2 := httptest.NewRequest("GET", "/a2a/v1/agents/bot-unknown", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec2.Code)
	}

	var errEnv model.A2AErrorEnvelope
	if err := json.NewDecoder(rec2.Body).Decode(&errEnv); err != nil {
		t.Fatalf("decode error failed: %v", err)
	}
	if errEnv.Error.Code != "AGENT_NOT_FOUND" {
		t.Errorf("expected AGENT_NOT_FOUND, got %s", errEnv.Error.Code)
	}
}

func TestA2ATaskDispatchHandler(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Type: "mock", Capabilities: []string{"c1"}},
		},
	}
	router, _, _ := setupTestRouter(cfg)

	// Case 1: Dispatch with custom task ID
	taskReq1 := model.TaskRequest{
		ID:      "custom-task-999",
		AgentID: "bot-1",
		Input:   "hello world",
	}
	bodyBytes, _ := json.Marshal(taskReq1)
	req1 := httptest.NewRequest("POST", "/a2a/v1/tasks", bytes.NewReader(bodyBytes))
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec1.Code, rec1.Body.String())
	}

	var resp1 model.TaskResponse
	if err := json.NewDecoder(rec1.Body).Decode(&resp1); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp1.TaskID != "custom-task-999" && resp1.TaskID != "mock-task-id" {
		t.Errorf("unexpected task ID: %s", resp1.TaskID)
	}
	if resp1.AgentID != "bot-1" {
		t.Errorf("expected agent bot-1, got %s", resp1.AgentID)
	}

	// Case 2: Dispatch without task ID (auto-generates one)
	taskReq2 := model.TaskRequest{
		AgentID: "bot-1",
		Input:   "hello again",
	}
	bodyBytes2, _ := json.Marshal(taskReq2)
	req2 := httptest.NewRequest("POST", "/a2a/v1/tasks", bytes.NewReader(bodyBytes2))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}
	var resp2 model.TaskResponse
	if err := json.NewDecoder(rec2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp2.TaskID == "" {
		t.Errorf("expected non-empty auto-generated task ID")
	}

	// Case 3: Invalid JSON body -> 400
	req3 := httptest.NewRequest("POST", "/a2a/v1/tasks", strings.NewReader("invalid json"))
	req3.Header.Set("Content-Type", "application/json")
	rec3 := httptest.NewRecorder()
	router.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec3.Code)
	}
}

func TestA2ATaskRBAC(t *testing.T) {
	cfg := &config.Config{
		Security: config.SecurityConfig{
			Enabled: true,
			APIKeys: []config.APIKeyConfig{
				{
					Key:           "key-bot1-only",
					ClientID:      "client-1",
					AllowedAgents: []string{"bot-1"},
				},
			},
		},
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Type: "mock", Capabilities: []string{"cap-1"}},
			{ID: "bot-2", Name: "Bot 2", Type: "mock", Capabilities: []string{"cap-2"}},
		},
	}
	router, _, _ := setupTestRouter(cfg)

	// Case 1: Allowed agent in body -> 200
	allowedReq := model.TaskRequest{
		AgentID: "bot-1",
		Input:   "allowed task",
	}
	bodyAllowed, _ := json.Marshal(allowedReq)
	req1 := httptest.NewRequest("POST", "/a2a/v1/tasks", bytes.NewReader(bodyAllowed))
	req1.Header.Set("Authorization", "Bearer key-bot1-only")
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec1.Code, rec1.Body.String())
	}

	// Case 2: Forbidden agent in body -> 403
	forbiddenReq := model.TaskRequest{
		AgentID: "bot-2",
		Input:   "forbidden task",
	}
	bodyForbidden, _ := json.Marshal(forbiddenReq)
	req2 := httptest.NewRequest("POST", "/a2a/v1/tasks", bytes.NewReader(bodyForbidden))
	req2.Header.Set("Authorization", "Bearer key-bot1-only")
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec2.Code)
	}

	var errEnv model.A2AErrorEnvelope
	if err := json.NewDecoder(rec2.Body).Decode(&errEnv); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if errEnv.Error.Code != "FORBIDDEN" {
		t.Errorf("expected FORBIDDEN, got %s", errEnv.Error.Code)
	}

	// Case 3: Required capability resolving to forbidden agent -> 403
	capReq := model.TaskRequest{
		RequiredCapability: "cap-2", // resolves to bot-2
		Input:              "forbidden cap task",
	}
	bodyCap, _ := json.Marshal(capReq)
	req3 := httptest.NewRequest("POST", "/a2a/v1/tasks", bytes.NewReader(bodyCap))
	req3.Header.Set("Authorization", "Bearer key-bot1-only")
	rec3 := httptest.NewRecorder()
	router.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for forbidden capability agent, got %d", rec3.Code)
	}
}

func TestA2ATaskStreamHandler(t *testing.T) {
	cfg := &config.Config{
		Security: config.SecurityConfig{
			Enabled: true,
			APIKeys: []config.APIKeyConfig{
				{
					Key:           "key-stream",
					ClientID:      "stream-client",
					AllowedAgents: []string{"bot-1"},
				},
			},
		},
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Type: "mock"},
			{ID: "bot-2", Name: "Bot 2", Type: "mock"},
		},
	}
	router, _, _ := setupTestRouter(cfg)

	// Case 1: Stream allowed agent
	taskReq := model.TaskRequest{
		AgentID: "bot-1",
		Input:   "stream this",
	}
	body, _ := json.Marshal(taskReq)
	req1 := httptest.NewRequest("POST", "/a2a/v1/tasks/stream", bytes.NewReader(body))
	req1.Header.Set("Authorization", "Bearer key-stream")
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec1.Code, rec1.Body.String())
	}
	contentType := rec1.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		t.Errorf("expected text/event-stream, got %s", contentType)
	}
	bodyStr := rec1.Body.String()
	if !strings.Contains(bodyStr, "event: task_started") || !strings.Contains(bodyStr, "event: token_delta") {
		t.Errorf("expected SSE events, got: %s", bodyStr)
	}

	// Case 2: Stream forbidden agent -> 403
	forbiddenTaskReq := model.TaskRequest{
		AgentID: "bot-2",
		Input:   "stream this",
	}
	bodyForbidden, _ := json.Marshal(forbiddenTaskReq)
	req2 := httptest.NewRequest("POST", "/a2a/v1/tasks/stream", bytes.NewReader(bodyForbidden))
	req2.Header.Set("Authorization", "Bearer key-stream")
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for forbidden streaming agent, got %d", rec2.Code)
	}
}

func TestA2ATaskGetHandler(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Type: "mock"},
		},
	}
	router, _, _ := setupTestRouter(cfg)

	// Dispatch task first
	taskReq := model.TaskRequest{
		ID:      "persisted-task-123",
		AgentID: "bot-1",
		Input:   "run something",
	}
	b, _ := json.Marshal(taskReq)
	postReq := httptest.NewRequest("POST", "/a2a/v1/tasks", bytes.NewReader(b))
	postRec := httptest.NewRecorder()
	router.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("dispatch failed: %d", postRec.Code)
	}

	// Retrieve by task_id
	getReq := httptest.NewRequest("GET", "/a2a/v1/tasks/persisted-task-123", nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getRec.Code)
	}
	var resp model.TaskResponse
	if err := json.NewDecoder(getRec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.TaskID != "persisted-task-123" && resp.TaskID != "mock-task-id" {
		t.Errorf("unexpected task ID: %s", resp.TaskID)
	}

	// Non-existent task -> 404
	getUnknownReq := httptest.NewRequest("GET", "/a2a/v1/tasks/unknown-task-id", nil)
	getUnknownRec := httptest.NewRecorder()
	router.ServeHTTP(getUnknownRec, getUnknownReq)

	if getUnknownRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", getUnknownRec.Code)
	}
}

func TestRESTInvokeHandler(t *testing.T) {
	cfg := &config.Config{
		Security: config.SecurityConfig{
			Enabled: true,
			APIKeys: []config.APIKeyConfig{
				{
					Key:           "key-rest",
					ClientID:      "rest-client",
					AllowedAgents: []string{"bot-1"},
				},
			},
		},
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Type: "mock"},
			{ID: "bot-2", Name: "Bot 2", Type: "mock"},
		},
	}
	router, _, _ := setupTestRouter(cfg)

	// Case 1: Valid direct REST invoke
	req1 := httptest.NewRequest("POST", "/api/v1/invoke/bot-1", strings.NewReader(`{"query":"how are you"}`))
	req1.Header.Set("Authorization", "Bearer key-rest")
	req1.Header.Set("Content-Type", "application/json")
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec1.Code, rec1.Body.String())
	}
	var resp model.TaskResponse
	if err := json.NewDecoder(rec1.Body).Decode(&resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if resp.AgentID != "bot-1" {
		t.Errorf("expected bot-1, got %s", resp.AgentID)
	}

	// Case 2: Forbidden agent -> 403
	req2 := httptest.NewRequest("POST", "/api/v1/invoke/bot-2", strings.NewReader(`{"query":"test"}`))
	req2.Header.Set("Authorization", "Bearer key-rest")
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec2.Code)
	}

	// Case 3: Non-existent agent (with wildcard key)
	cfgWildcard := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "bot-1", Type: "mock"},
		},
	}
	routerWild, _, _ := setupTestRouter(cfgWildcard)
	req3 := httptest.NewRequest("POST", "/api/v1/invoke/unknown-bot", strings.NewReader(`{"query":"test"}`))
	rec3 := httptest.NewRecorder()
	routerWild.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec3.Code)
	}
}

func TestRESTStreamHandler(t *testing.T) {
	cfg := &config.Config{
		Security: config.SecurityConfig{
			Enabled: true,
			APIKeys: []config.APIKeyConfig{
				{
					Key:           "key-rest-stream",
					ClientID:      "rest-client",
					AllowedAgents: []string{"bot-1"},
				},
			},
		},
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Type: "mock"},
			{ID: "bot-2", Name: "Bot 2", Type: "mock"},
		},
	}
	router, _, _ := setupTestRouter(cfg)

	// Case 1: Valid direct REST stream
	req1 := httptest.NewRequest("POST", "/api/v1/stream/bot-1", strings.NewReader(`{"input":"stream prompt"}`))
	req1.Header.Set("Authorization", "Bearer key-rest-stream")
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec1.Code, rec1.Body.String())
	}
	if !strings.Contains(rec1.Header().Get("Content-Type"), "text/event-stream") {
		t.Errorf("expected text/event-stream")
	}
	if !strings.Contains(rec1.Body.String(), "event: token_delta") {
		t.Errorf("expected token_delta in stream body: %s", rec1.Body.String())
	}

	// Case 2: Forbidden agent -> 403
	req2 := httptest.NewRequest("POST", "/api/v1/stream/bot-2", strings.NewReader(`{"input":"stream prompt"}`))
	req2.Header.Set("Authorization", "Bearer key-rest-stream")
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec2.Code)
	}
}

func TestHealthAndReadinessHandlers(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "bot-1", Type: "mock"},
		},
	}
	router, _, _ := setupTestRouter(cfg)

	// Case 1: Liveness /healthz
	reqHealth := httptest.NewRequest("GET", "/healthz", nil)
	recHealth := httptest.NewRecorder()
	router.ServeHTTP(recHealth, reqHealth)

	if recHealth.Code != http.StatusOK {
		t.Fatalf("expected 200 from healthz, got %d", recHealth.Code)
	}
	var healthData map[string]any
	if err := json.NewDecoder(recHealth.Body).Decode(&healthData); err != nil {
		t.Fatalf("decode healthz failed: %v", err)
	}
	if healthData["status"] != "healthy" {
		t.Errorf("expected status healthy, got %v", healthData["status"])
	}

	// Case 2: Readiness /readyz when ready
	reqReady := httptest.NewRequest("GET", "/readyz", nil)
	recReady := httptest.NewRecorder()
	router.ServeHTTP(recReady, reqReady)

	if recReady.Code != http.StatusOK {
		t.Fatalf("expected 200 from readyz, got %d", recReady.Code)
	}
	var readyData map[string]any
	if err := json.NewDecoder(recReady.Body).Decode(&readyData); err != nil {
		t.Fatalf("decode readyz failed: %v", err)
	}
	if readyData["status"] != "ready" {
		t.Errorf("expected status ready, got %v", readyData["status"])
	}

	// Case 3: Readiness /readyz when no adapters registered
	emptyReg := adapter.NewRegistry()
	emptyDisp := dispatcher.New(cfg, emptyReg)
	unreadyRouter := SetupRouter(cfg, emptyDisp)

	recUnready := httptest.NewRecorder()
	unreadyRouter.ServeHTTP(recUnready, reqReady)

	if recUnready.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 from unready readyz, got %d", recUnready.Code)
	}
}

func TestMetricsEndpointWired(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "bot-1", Type: "mock"},
		},
	}
	router, _, _ := setupTestRouter(cfg)

	// Make a request first to trigger metrics
	req1 := httptest.NewRequest("GET", "/a2a/v1/agents", nil)
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)

	// Check /metrics endpoint
	reqMetrics := httptest.NewRequest("GET", "/metrics", nil)
	recMetrics := httptest.NewRecorder()
	router.ServeHTTP(recMetrics, reqMetrics)

	if recMetrics.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recMetrics.Code)
	}
	if !strings.Contains(recMetrics.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("expected text/plain content type")
	}
	body := recMetrics.Body.String()
	if !strings.Contains(body, "a2a_requests_total") {
		t.Errorf("expected a2a_requests_total in metrics output")
	}
}

func TestServerLifecycle(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:                "127.0.0.1",
			Port:                port,
			ReadTimeoutSeconds:  5,
			WriteTimeoutSeconds: 5,
		},
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Type: "mock"},
		},
	}

	reg := adapter.NewRegistry()
	reg.Register(&mockAdapter{adapterType: "mock"})
	disp := dispatcher.New(cfg, reg)

	srv := NewServer(cfg, disp)

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	// Wait briefly for server to accept connections
	time.Sleep(50 * time.Millisecond)

	// Send request to server
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("request to server failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		t.Errorf("shutdown error: %v", err)
	}

	serverErr := <-errCh
	if serverErr != nil && serverErr != http.ErrServerClosed {
		t.Errorf("expected ErrServerClosed, got %v", serverErr)
	}
}
