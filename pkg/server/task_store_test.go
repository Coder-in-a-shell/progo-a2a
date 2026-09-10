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
