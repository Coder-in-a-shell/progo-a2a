package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/adapter"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/dispatcher"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/storage"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/stream"
)

type customMockTaskStore struct {
	mu           sync.Mutex
	saveCalls    int
	loadCalls    int
	savedResp    *model.TaskResponse
	savedAliases []string
	saveErr      error
	loadErr      error
	loadResp     *model.TaskResponse
}

func (m *customMockTaskStore) Save(ctx context.Context, resp *model.TaskResponse, aliases ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveCalls++
	m.savedResp = resp
	m.savedAliases = append([]string(nil), aliases...)
	return m.saveErr
}

func (m *customMockTaskStore) Load(ctx context.Context, id string) (*model.TaskResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadCalls++
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	if m.loadResp != nil {
		return m.loadResp, nil
	}
	return nil, storage.ErrTaskNotFound
}

type canonicalAdapter struct {
	canonicalID string
}

var _ adapter.Adapter = (*canonicalAdapter)(nil)

func (c *canonicalAdapter) Type() string { return "canonical-mock" }
func (c *canonicalAdapter) TranslateRequest(ctx context.Context, agent *config.AgentConfig, task *model.TaskRequest) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, "POST", "http://agent.internal", bytes.NewBufferString(`{}`))
}
func (c *canonicalAdapter) TranslateResponse(ctx context.Context, agent *config.AgentConfig, resp *http.Response) (*model.TaskResponse, error) {
	return &model.TaskResponse{
		TaskID:    c.canonicalID,
		AgentID:   agent.ID,
		Status:    model.StatusCompleted,
		Output:    map[string]any{"res": "ok"},
		Timestamp: time.Now().UTC(),
	}, nil
}
func (c *canonicalAdapter) TranslateStream(ctx context.Context, agent *config.AgentConfig, resp *http.Response, emitter stream.Emitter) error {
	return nil
}

func TestA2ADispatch_InjectedCustomStoreReceivesAtomicCanonicalAndAlias(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Type: "canonical-mock"},
		},
	}
	reg := adapter.NewRegistry()
	reg.Register(&canonicalAdapter{canonicalID: "downstream-task-456"})
	disp := dispatcher.New(cfg, reg)
	disp.SetHTTPClient(&http.Client{Transport: &mockTransport{}})

	handler := NewA2AHandler(cfg, disp)
	store := &customMockTaskStore{}
	handler.SetTaskStore(store)

	taskReq := model.TaskRequest{
		ID:      "req-alias-123",
		AgentID: "bot-1",
		Input:   "hello",
	}
	body, _ := json.Marshal(taskReq)
	req := httptest.NewRequest("POST", "/a2a/v1/tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.DispatchTask(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if store.saveCalls != 1 {
		t.Fatalf("expected 1 save call, got %d", store.saveCalls)
	}
	if store.savedResp == nil || store.savedResp.TaskID != "downstream-task-456" {
		t.Fatalf("expected saved canonical TaskID downstream-task-456, got %+v", store.savedResp)
	}
	if len(store.savedAliases) != 1 || store.savedAliases[0] != "req-alias-123" {
		t.Fatalf("expected alias req-alias-123, got %v", store.savedAliases)
	}
}

func TestA2ADispatch_PersistenceFailureReturns503(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Type: "mock"},
		},
	}
	reg := adapter.NewRegistry()
	reg.Register(&mockAdapter{adapterType: "mock"})
	disp := dispatcher.New(cfg, reg)
	disp.SetHTTPClient(&http.Client{Transport: &mockTransport{}})

	handler := NewA2AHandler(cfg, disp)
	store := &customMockTaskStore{
		saveErr: errors.New("storage backend disk full"),
	}
	handler.SetTaskStore(store)

	taskReq := model.TaskRequest{
		ID:      "task-req-1",
		AgentID: "bot-1",
		Input:   "hello",
	}
	body, _ := json.Marshal(taskReq)
	req := httptest.NewRequest("POST", "/a2a/v1/tasks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.DispatchTask(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}

	bodyBytes := append([]byte(nil), rec.Body.Bytes()...)
	var errEnv model.A2AErrorEnvelope
	if err := json.Unmarshal(bodyBytes, &errEnv); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errEnv.Error.Code != "TASK_STORAGE_UNAVAILABLE" {
		t.Fatalf("expected code TASK_STORAGE_UNAVAILABLE, got %s", errEnv.Error.Code)
	}
	if strings.Contains(string(bodyBytes), "disk full") {
		t.Fatal("storage backend error leaked to the client")
	}
}

func TestRESTInvoke_PersistenceFailureReturns503AndSaveCalledOnce(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Type: "mock"},
		},
	}
	reg := adapter.NewRegistry()
	reg.Register(&mockAdapter{adapterType: "mock"})
	disp := dispatcher.New(cfg, reg)
	disp.SetHTTPClient(&http.Client{Transport: &mockTransport{}})

	store := &customMockTaskStore{
		saveErr: errors.New("db write timeout"),
	}
	router := SetupRouter(cfg, disp, WithTaskStore(store))

	req := httptest.NewRequest("POST", "/api/v1/invoke/bot-1", strings.NewReader(`{"input":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}

	bodyBytes := append([]byte(nil), rec.Body.Bytes()...)
	var errEnv model.A2AErrorEnvelope
	if err := json.Unmarshal(bodyBytes, &errEnv); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errEnv.Error.Code != "TASK_STORAGE_UNAVAILABLE" {
		t.Fatalf("expected code TASK_STORAGE_UNAVAILABLE, got %s", errEnv.Error.Code)
	}
	if strings.Contains(string(bodyBytes), "db write timeout") {
		t.Fatal("storage backend error leaked to the client")
	}
	if store.saveCalls != 1 {
		t.Fatalf("expected Save to be called exactly once, got %d", store.saveCalls)
	}
}

func TestGetTask_NotFoundAndBackendFailure(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Type: "mock"},
		},
	}
	reg := adapter.NewRegistry()
	disp := dispatcher.New(cfg, reg)

	t.Run("ErrTaskNotFound maps to 404", func(t *testing.T) {
		handler := NewA2AHandler(cfg, disp)
		store := &customMockTaskStore{
			loadErr: storage.ErrTaskNotFound,
		}
		handler.SetTaskStore(store)

		req := httptest.NewRequest("GET", "/a2a/v1/tasks/absent-task", nil)
		req.SetPathValue("task_id", "absent-task")
		rec := httptest.NewRecorder()

		handler.GetTask(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
		}
		bodyBytes := append([]byte(nil), rec.Body.Bytes()...)
		var errEnv model.A2AErrorEnvelope
		if err := json.Unmarshal(bodyBytes, &errEnv); err != nil {
			t.Fatalf("decode error: %v", err)
		}
		if errEnv.Error.Code != "TASK_NOT_FOUND" {
			t.Fatalf("expected code TASK_NOT_FOUND, got %s", errEnv.Error.Code)
		}
	})

	t.Run("Other store error maps to 503", func(t *testing.T) {
		handler := NewA2AHandler(cfg, disp)
		store := &customMockTaskStore{
			loadErr: errors.New("db connection dropped"),
		}
		handler.SetTaskStore(store)

		req := httptest.NewRequest("GET", "/a2a/v1/tasks/any-task", nil)
		req.SetPathValue("task_id", "any-task")
		rec := httptest.NewRecorder()

		handler.GetTask(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
		}
		bodyBytes := append([]byte(nil), rec.Body.Bytes()...)
		var errEnv model.A2AErrorEnvelope
		if err := json.Unmarshal(bodyBytes, &errEnv); err != nil {
			t.Fatalf("decode error: %v", err)
		}
		if errEnv.Error.Code != "TASK_STORAGE_UNAVAILABLE" {
			t.Fatalf("expected code TASK_STORAGE_UNAVAILABLE, got %s", errEnv.Error.Code)
		}
		if strings.Contains(string(bodyBytes), "connection dropped") {
			t.Fatal("storage backend error leaked to the client")
		}
	})
}

func TestGetTask_RBACEnforced(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "agent-a", Name: "Agent A"},
			{ID: "agent-b", Name: "Agent B"},
		},
	}
	disp := dispatcher.New(cfg, adapter.NewRegistry())

	handler := NewA2AHandler(cfg, disp)
	store := &customMockTaskStore{
		loadResp: &model.TaskResponse{
			TaskID:  "task-rbac-1",
			AgentID: "agent-a",
			Status:  model.StatusCompleted,
		},
	}
	handler.SetTaskStore(store)

	// Unauthorized client (allowed: agent-b, task belongs to agent-a)
	req1 := httptest.NewRequest("GET", "/a2a/v1/tasks/task-rbac-1", nil)
	req1.SetPathValue("task_id", "task-rbac-1")
	req1 = requestWithCredentials(req1, "agent-b")
	rec1 := httptest.NewRecorder()

	handler.GetTask(rec1, req1)

	if rec1.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec1.Code, rec1.Body.String())
	}
	var errEnv model.A2AErrorEnvelope
	if err := json.NewDecoder(rec1.Body).Decode(&errEnv); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errEnv.Error.Code != "FORBIDDEN" {
		t.Fatalf("expected code FORBIDDEN, got %s", errEnv.Error.Code)
	}

	// Authorized client (allowed: agent-a)
	req2 := httptest.NewRequest("GET", "/a2a/v1/tasks/task-rbac-1", nil)
	req2.SetPathValue("task_id", "task-rbac-1")
	req2 = requestWithCredentials(req2, "agent-a")
	rec2 := httptest.NewRecorder()

	handler.GetTask(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestSetupRouter_WithTaskStore_RESTAndA2AShareStore(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Type: "mock"},
		},
	}
	reg := adapter.NewRegistry()
	reg.Register(&mockAdapter{adapterType: "mock"})
	disp := dispatcher.New(cfg, reg)
	disp.SetHTTPClient(&http.Client{Transport: &mockTransport{}})

	customStore := storage.NewMemoryTaskStore(100)
	router := SetupRouter(cfg, disp, WithTaskStore(customStore))

	// REST invoke
	req := httptest.NewRequest("POST", "/api/v1/invoke/bot-1", strings.NewReader(`{"input":"hello from rest"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("REST invoke failed with %d: %s", rec.Code, rec.Body.String())
	}

	var restResp model.TaskResponse
	if err := json.NewDecoder(rec.Body).Decode(&restResp); err != nil {
		t.Fatalf("decode rest response: %v", err)
	}
	if restResp.TaskID == "" {
		t.Fatalf("expected non-empty task ID from rest invoke")
	}

	// Verify task exists directly in the injected store
	storedDirectly, err := customStore.Load(context.Background(), restResp.TaskID)
	if err != nil {
		t.Fatalf("task was not stored in injected store: %v", err)
	}
	if storedDirectly.TaskID != restResp.TaskID {
		t.Fatalf("task ID mismatch: expected %s, got %s", restResp.TaskID, storedDirectly.TaskID)
	}

	// Verify A2A GET reads that same task from the injected store via router
	getReq := httptest.NewRequest("GET", "/a2a/v1/tasks/"+restResp.TaskID, nil)
	getRec := httptest.NewRecorder()

	router.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("A2A GET failed with %d: %s", getRec.Code, getRec.Body.String())
	}

	var a2aResp model.TaskResponse
	if err := json.NewDecoder(getRec.Body).Decode(&a2aResp); err != nil {
		t.Fatalf("decode a2a get response: %v", err)
	}
	if a2aResp.TaskID != restResp.TaskID {
		t.Fatalf("a2a task ID mismatch: expected %s, got %s", restResp.TaskID, a2aResp.TaskID)
	}
}

func TestTaskStore_NilOptionsPreserveDefaultBehavior(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Type: "mock"},
		},
	}
	reg := adapter.NewRegistry()
	disp := dispatcher.New(cfg, reg)

	t.Run("SetTaskStore(nil) is ignored", func(t *testing.T) {
		handler := NewA2AHandler(cfg, disp)
		handler.SetTaskStore(nil)

		// StoreTask should still work with default memory store
		handler.StoreTask(&model.TaskResponse{
			TaskID:  "task-default-1",
			AgentID: "bot-1",
			Status:  model.StatusCompleted,
		})

		req := httptest.NewRequest("GET", "/a2a/v1/tasks/task-default-1", nil)
		req.SetPathValue("task_id", "task-default-1")
		rec := httptest.NewRecorder()

		handler.GetTask(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("WithTaskStore(nil) is ignored by SetupRouter", func(t *testing.T) {
		regWithAdapter := adapter.NewRegistry()
		regWithAdapter.Register(&mockAdapter{adapterType: "mock"})
		dispWithClient := dispatcher.New(cfg, regWithAdapter)
		dispWithClient.SetHTTPClient(&http.Client{Transport: &mockTransport{}})

		router := SetupRouter(cfg, dispWithClient, WithTaskStore(nil))

		req := httptest.NewRequest("POST", "/api/v1/invoke/bot-1", strings.NewReader(`{"input":"test"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 with default store, got %d: %s", rec.Code, rec.Body.String())
		}
	})
}

type mockHealthCheckStore struct {
	customMockTaskStore
	mu        sync.Mutex
	pingCalls int
	pingErr   error
	pingDelay time.Duration
}

var _ storage.HealthChecker = (*mockHealthCheckStore)(nil)
var _ storage.TaskStore = (*mockHealthCheckStore)(nil)

func (m *mockHealthCheckStore) Ping(ctx context.Context) error {
	m.mu.Lock()
	m.pingCalls++
	delay := m.pingDelay
	err := m.pingErr
	m.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (m *mockHealthCheckStore) PingCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pingCalls
}

func setupHealthTestRouter(store storage.TaskStore) (http.Handler, *mockHealthCheckStore) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{ID: "bot-1", Name: "Bot 1", Type: "mock"},
		},
	}
	reg := adapter.NewRegistry()
	reg.Register(&mockAdapter{adapterType: "mock"})
	disp := dispatcher.New(cfg, reg)
	disp.SetHTTPClient(&http.Client{Transport: &mockTransport{}})

	var hcStore *mockHealthCheckStore
	var opts []RouterOption
	if store != nil {
		opts = append(opts, WithTaskStore(store))
		if hc, ok := store.(*mockHealthCheckStore); ok {
			hcStore = hc
		}
	}
	router := SetupRouter(cfg, disp, opts...)
	return router, hcStore
}

func TestHealthAndReadiness_LivenessNeverPings(t *testing.T) {
	store := &mockHealthCheckStore{}
	router, _ := setupHealthTestRouter(store)

	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from healthz, got %d", rec.Code)
	}
	if store.PingCount() != 0 {
		t.Errorf("expected liveness /healthz never to ping storage, but ping was called %d times", store.PingCount())
	}

	// Make multiple calls to ensure it remains pure liveness
	for i := 0; i < 3; i++ {
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: expected 200, got %d", i, rec.Code)
		}
	}
	if store.PingCount() != 0 {
		t.Errorf("expected 0 ping calls after repeated healthz, got %d", store.PingCount())
	}
}

func TestHealthAndReadiness_ReadinessPingsOnce(t *testing.T) {
	store := &mockHealthCheckStore{}
	router, _ := setupHealthTestRouter(store)

	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from readyz, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.PingCount() != 1 {
		t.Errorf("expected readiness /readyz to ping storage exactly once, got %d", store.PingCount())
	}

	var respBody map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if respBody["status"] != "ready" {
		t.Errorf("expected status 'ready', got %v", respBody["status"])
	}
}

func TestHealthAndReadiness_StorageFailureReturnsGeneric503(t *testing.T) {
	sensitiveErrorMsg := "connection to postgres://admin:super_secret_db_pass@db.internal:5432 failed: disk corrupt"
	store := &mockHealthCheckStore{
		pingErr: errors.New(sensitiveErrorMsg),
	}
	router, _ := setupHealthTestRouter(store)

	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 from failed storage readyz, got %d: %s", rec.Code, rec.Body.String())
	}

	bodyStr := rec.Body.String()
	if strings.Contains(bodyStr, "super_secret_db_pass") {
		t.Fatalf("storage ping error leaked sensitive credential: %s", bodyStr)
	}
	if strings.Contains(bodyStr, "disk corrupt") {
		t.Fatalf("storage ping error leaked internal message: %s", bodyStr)
	}

	var respBody map[string]any
	if err := json.Unmarshal([]byte(bodyStr), &respBody); err != nil {
		t.Fatalf("failed to decode 503 response JSON: %v", err)
	}
	if respBody["status"] != "not ready" {
		t.Errorf("expected status 'not ready', got %v", respBody["status"])
	}
	if respBody["error"] != "storage unavailable" {
		t.Errorf("expected generic error 'storage unavailable', got %v", respBody["error"])
	}
}

func TestHealthAndReadiness_CanceledAndSlowPingRespectsTimeout(t *testing.T) {
	t.Run("slow ping exceeding 2s timeout returns 503 without leaking", func(t *testing.T) {
		// Set pingDelay greater than 2 seconds (e.g. 2500ms)
		store := &mockHealthCheckStore{
			pingDelay: 2500 * time.Millisecond,
		}
		router, _ := setupHealthTestRouter(store)

		start := time.Now()
		req := httptest.NewRequest("GET", "/readyz", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		elapsed := time.Since(start)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 from timed out readyz, got %d", rec.Code)
		}

		// Elapsed should be around 2 seconds (bounded by 2s child context timeout)
		if elapsed < 1800*time.Millisecond || elapsed > 3500*time.Millisecond {
			t.Logf("elapsed duration: %v (expected ~2s)", elapsed)
		}

		var respBody map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&respBody); err != nil {
			t.Fatalf("failed to decode JSON: %v", err)
		}
		if respBody["error"] != "storage unavailable" {
			t.Errorf("expected generic error 'storage unavailable', got %v", respBody["error"])
		}
	})

	t.Run("canceled request context returns 503", func(t *testing.T) {
		store := &mockHealthCheckStore{}
		router, _ := setupHealthTestRouter(store)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // pre-cancel context

		req := httptest.NewRequestWithContext(ctx, "GET", "/readyz", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 from canceled readyz, got %d", rec.Code)
		}
	})
}

func TestHealthAndReadiness_MemoryAndDefaultBehaviorUnchanged(t *testing.T) {
	t.Run("memory store has no added readiness dependency", func(t *testing.T) {
		memStore := storage.NewMemoryTaskStore(100)
		router, _ := setupHealthTestRouter(memStore)

		// healthz
		reqHealth := httptest.NewRequest("GET", "/healthz", nil)
		recHealth := httptest.NewRecorder()
		router.ServeHTTP(recHealth, reqHealth)
		if recHealth.Code != http.StatusOK {
			t.Errorf("expected 200 from healthz with memory store, got %d", recHealth.Code)
		}

		// readyz
		reqReady := httptest.NewRequest("GET", "/readyz", nil)
		recReady := httptest.NewRecorder()
		router.ServeHTTP(recReady, reqReady)
		if recReady.Code != http.StatusOK {
			t.Errorf("expected 200 from readyz with memory store, got %d", recReady.Code)
		}
		var readyBody map[string]any
		if err := json.NewDecoder(recReady.Body).Decode(&readyBody); err != nil {
			t.Fatalf("failed to decode readyz body: %v", err)
		}
		if readyBody["status"] != "ready" {
			t.Errorf("expected status 'ready', got %v", readyBody["status"])
		}
	})

	t.Run("nil task store maintains default health and readiness", func(t *testing.T) {
		router, _ := setupHealthTestRouter(nil)

		// healthz
		reqHealth := httptest.NewRequest("GET", "/healthz", nil)
		recHealth := httptest.NewRecorder()
		router.ServeHTTP(recHealth, reqHealth)
		if recHealth.Code != http.StatusOK {
			t.Errorf("expected 200 from healthz without store, got %d", recHealth.Code)
		}

		// readyz
		reqReady := httptest.NewRequest("GET", "/readyz", nil)
		recReady := httptest.NewRecorder()
		router.ServeHTTP(recReady, reqReady)
		if recReady.Code != http.StatusOK {
			t.Errorf("expected 200 from readyz without store, got %d", recReady.Code)
		}
	})
}
