package dispatcher

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/adapter"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/stream"
)

func TestCapabilityRouting(t *testing.T) {
	reg := adapter.NewRegistry()
	custom := adapter.NewCustomAdapter()
	reg.Register(custom)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"answer": "capability match success"}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:           "agent-search",
				Type:         "custom",
				Endpoint:     server.URL,
				Capabilities: []string{"web_search"},
				Mapping: &config.CustomMapping{
					Request:  config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
					Response: config.ResponseMapping{OutputPath: "answer"},
				},
			},
		},
	}

	disp := New(cfg, reg)
	task := &model.TaskRequest{
		RequiredCapability: "web_search",
		Input:              "search test",
	}

	resp, err := disp.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	if resp.AgentID != "agent-search" {
		t.Errorf("expected agent-search, got %s", resp.AgentID)
	}
	if resp.Output != "capability match success" {
		t.Errorf("unexpected output: %v", resp.Output)
	}
}

func TestFallbackOnFailure(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewCustomAdapter())

	// Primary server fails
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer primary.Close()

	// Fallback server succeeds
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"answer": "fallback succeeded"}`))
	}))
	defer fallback.Close()

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:               "primary",
				Type:             "custom",
				Endpoint:         primary.URL,
				Retries:          1,
				FallbackAgentIDs: []string{"backup"},
				Mapping: &config.CustomMapping{
					Request:  config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
					Response: config.ResponseMapping{OutputPath: "answer"},
				},
			},
			{
				ID:       "backup",
				Type:     "custom",
				Endpoint: fallback.URL,
				Mapping: &config.CustomMapping{
					Request:  config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
					Response: config.ResponseMapping{OutputPath: "answer"},
				},
			},
		},
	}

	disp := New(cfg, reg)
	task := &model.TaskRequest{AgentID: "primary", Input: "run"}

	resp, err := disp.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	if resp.AgentID != "backup" {
		t.Errorf("expected backup agent, got %s", resp.AgentID)
	}
	if resp.Output != "fallback succeeded" {
		t.Errorf("unexpected output: %v", resp.Output)
	}
}

func TestRetriesOnFailure(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewCustomAdapter())

	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&attempts, 1)
		if cur < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"answer": "retry succeeded"}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:       "retry-agent",
				Type:     "custom",
				Endpoint: server.URL,
				Retries:  2,
				Mapping: &config.CustomMapping{
					Request:  config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
					Response: config.ResponseMapping{OutputPath: "answer"},
				},
			},
		},
	}

	disp := New(cfg, reg)
	disp.SetBackoffPolicy(&BackoffPolicy{
		InitialInterval: time.Millisecond,
		Multiplier:      1.5,
		MaxInterval:     10 * time.Millisecond,
	})

	task := &model.TaskRequest{AgentID: "retry-agent", Input: "go"}
	resp, err := disp.Dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
	if resp.Output != "retry succeeded" {
		t.Errorf("expected 'retry succeeded', got %v", resp.Output)
	}
}

func TestTimeoutCancellation(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewCustomAdapter())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"answer": "too late"}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:             "slow-agent",
				Type:           "custom",
				Endpoint:       server.URL,
				TimeoutSeconds: 1,
				Retries:        0,
				Mapping: &config.CustomMapping{
					Request:  config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
					Response: config.ResponseMapping{OutputPath: "answer"},
				},
			},
		},
	}

	disp := New(cfg, reg)
	task := &model.TaskRequest{AgentID: "slow-agent", Input: "ping"}

	start := time.Now()
	_, err := disp.Dispatch(context.Background(), task)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed > 1400*time.Millisecond {
		t.Errorf("expected timeout around 1s, took %v", elapsed)
	}
}

type recordingEmitter struct {
	mu     sync.Mutex
	events []model.StreamEvent
}

func (r *recordingEmitter) Emit(eventType model.StreamEventType, data any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, model.StreamEvent{Type: eventType, Data: data})
	return nil
}

func (r *recordingEmitter) Events() []model.StreamEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := make([]model.StreamEvent, len(r.events))
	copy(copied, r.events)
	return copied
}

func TestDispatchStream(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewCustomAdapter())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}
		w.Write([]byte("data: {\"text\": \"Hello \"}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: {\"text\": \"World!\"}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:       "stream-agent",
				Type:     "custom",
				Endpoint: server.URL,
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
					Stream: config.StreamMapping{
						DataPath:     "text",
						DoneSentinel: "[DONE]",
					},
				},
			},
		},
	}

	disp := New(cfg, reg)
	task := &model.TaskRequest{AgentID: "stream-agent", Input: "stream test", Stream: true}
	emitter := &recordingEmitter{}

	err := disp.DispatchStream(context.Background(), task, emitter)
	if err != nil {
		t.Fatalf("dispatch stream failed: %v", err)
	}

	events := emitter.Events()
	if len(events) < 4 {
		t.Fatalf("expected at least 4 events, got %d", len(events))
	}

	if events[0].Type != model.EventTaskStarted {
		t.Errorf("expected first event task_started, got %s", events[0].Type)
	}
	if events[1].Type != model.EventTokenDelta {
		t.Errorf("expected token_delta, got %s", events[1].Type)
	}
	if events[len(events)-1].Type != model.EventTaskCompleted {
		t.Errorf("expected last event task_completed, got %s", events[len(events)-1].Type)
	}
}

func TestCycleDetectionInFallback(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewCustomAdapter())

	// Server A fails
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer serverA.Close()

	// Server B fails
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer serverB.Close()

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:               "agent-a",
				Type:             "custom",
				Endpoint:         serverA.URL,
				Retries:          0,
				FallbackAgentIDs: []string{"agent-b"},
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
				},
			},
			{
				ID:               "agent-b",
				Type:             "custom",
				Endpoint:         serverB.URL,
				Retries:          0,
				FallbackAgentIDs: []string{"agent-a"},
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
				},
			},
		},
	}

	disp := New(cfg, reg)
	task := &model.TaskRequest{AgentID: "agent-a", Input: "cycle test"}

	_, err := disp.Dispatch(context.Background(), task)
	if err == nil {
		t.Fatal("expected error on cycle failure, got nil")
	}
}

func TestGetAgentAndFindAgentByCapability(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:           "agent-1",
				Capabilities: []string{"code_exec", "analysis"},
			},
		},
	}
	reg := adapter.NewRegistry()
	disp := New(cfg, reg)

	// GetAgent existing
	a1, err := disp.GetAgent("agent-1")
	if err != nil {
		t.Fatalf("expected agent-1 found, got err: %v", err)
	}
	if a1.ID != "agent-1" {
		t.Errorf("expected ID agent-1, got %s", a1.ID)
	}

	// GetAgent missing
	_, err = disp.GetAgent("non-existent")
	if err == nil {
		t.Fatal("expected error for non-existent agent, got nil")
	}

	// FindAgentByCapability existing
	ac, err := disp.FindAgentByCapability("analysis")
	if err != nil {
		t.Fatalf("expected capability match, got err: %v", err)
	}
	if ac.ID != "agent-1" {
		t.Errorf("expected ID agent-1, got %s", ac.ID)
	}

	// FindAgentByCapability missing
	_, err = disp.FindAgentByCapability("flying")
	if err == nil {
		t.Fatal("expected error for missing capability, got nil")
	}
}

func TestDispatchStream_FallbackOnFailure(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewCustomAdapter())

	// Primary fails
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer primary.Close()

	// Fallback succeeds with stream
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}
		w.Write([]byte("data: {\"text\": \"Stream from fallback\"}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer fallback.Close()

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:               "primary-stream",
				Type:             "custom",
				Endpoint:         primary.URL,
				Retries:          0,
				FallbackAgentIDs: []string{"backup-stream"},
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
				},
			},
			{
				ID:       "backup-stream",
				Type:     "custom",
				Endpoint: fallback.URL,
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
					Stream: config.StreamMapping{
						DataPath:     "text",
						DoneSentinel: "[DONE]",
					},
				},
			},
		},
	}

	disp := New(cfg, reg)
	task := &model.TaskRequest{AgentID: "primary-stream", Input: "stream fallback test"}
	emitter := &recordingEmitter{}

	err := disp.DispatchStream(context.Background(), task, emitter)
	if err != nil {
		t.Fatalf("dispatch stream fallback failed: %v", err)
	}

	events := emitter.Events()
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(events))
	}
	if events[0].Type != model.EventTaskStarted {
		t.Errorf("expected task_started, got %s", events[0].Type)
	}
	if events[len(events)-1].Type != model.EventTaskCompleted {
		t.Errorf("expected task_completed, got %s", events[len(events)-1].Type)
	}
}

func TestContextCancellation(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewCustomAdapter())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:       "slow-agent",
				Type:     "custom",
				Endpoint: server.URL,
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
				},
			},
		},
	}

	disp := New(cfg, reg)
	task := &model.TaskRequest{AgentID: "slow-agent", Input: "test"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := disp.Dispatch(ctx, task)
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}

	emitter := &recordingEmitter{}
	err = disp.DispatchStream(ctx, task, emitter)
	if err == nil {
		t.Fatal("expected error on cancelled stream context, got nil")
	}
}

func TestRetryExhaustion_DownstreamUnavailable(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewCustomAdapter())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:       "always-fail",
				Type:     "custom",
				Endpoint: server.URL,
				Retries:  1,
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
				},
			},
		},
	}

	disp := New(cfg, reg)
	disp.SetBackoffPolicy(&BackoffPolicy{
		InitialInterval: time.Millisecond,
		Multiplier:      1,
		MaxInterval:     time.Millisecond,
	})

	task := &model.TaskRequest{AgentID: "always-fail", Input: "test"}
	_, err := disp.Dispatch(context.Background(), task)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	a2aErr, ok := err.(*model.A2AError)
	if !ok {
		t.Fatalf("expected *model.A2AError, got %T: %v", err, err)
	}
	if a2aErr.Code != "DOWNSTREAM_UNAVAILABLE" || a2aErr.Status != http.StatusBadGateway {
		t.Errorf("unexpected error details: %+v", a2aErr)
	}
}

func TestInvalidRequests(t *testing.T) {
	cfg := &config.Config{}
	reg := adapter.NewRegistry()
	disp := New(cfg, reg)

	// nil task
	_, err := disp.Dispatch(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil task, got nil")
	}

	// empty agentID and empty requiredCapability
	_, err = disp.Dispatch(context.Background(), &model.TaskRequest{})
	if err == nil {
		t.Fatal("expected error for empty task, got nil")
	}
}

type midStreamFailAdapter struct{}

func (a *midStreamFailAdapter) Type() string { return "mid_stream_fail" }
func (a *midStreamFailAdapter) TranslateRequest(ctx context.Context, agent *config.AgentConfig, task *model.TaskRequest) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, "POST", agent.Endpoint, nil)
}
func (a *midStreamFailAdapter) TranslateResponse(ctx context.Context, agent *config.AgentConfig, resp *http.Response) (*model.TaskResponse, error) {
	return &model.TaskResponse{Output: "ok"}, nil
}
func (a *midStreamFailAdapter) TranslateStream(ctx context.Context, agent *config.AgentConfig, resp *http.Response, emitter stream.Emitter) error {
	if err := emitter.Emit(model.EventTokenDelta, map[string]any{"delta": "partial chunk"}); err != nil {
		return err
	}
	return errors.New("mid-stream connection dropped")
}

func TestDispatchStream_MidStreamFailure_NoFallback(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register(&midStreamFailAdapter{})
	reg.Register(adapter.NewCustomAdapter())

	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer primaryServer.Close()

	var fallbackCalled atomic.Bool
	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackCalled.Store(true)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if ok {
			w.Write([]byte("data: {\"text\": \"fallback chunk\"}\n\n"))
			flusher.Flush()
			w.Write([]byte("data: [DONE]\n\n"))
			flusher.Flush()
		}
	}))
	defer fallbackServer.Close()

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:               "primary-agent",
				Type:             "mid_stream_fail",
				Endpoint:         primaryServer.URL,
				Retries:          0,
				FallbackAgentIDs: []string{"backup-agent"},
			},
			{
				ID:       "backup-agent",
				Type:     "custom",
				Endpoint: fallbackServer.URL,
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
					Stream: config.StreamMapping{
						DataPath:     "text",
						DoneSentinel: "[DONE]",
					},
				},
			},
		},
	}

	disp := New(cfg, reg)
	task := &model.TaskRequest{AgentID: "primary-agent", Input: "test", Stream: true}
	emitter := &recordingEmitter{}

	err := disp.DispatchStream(context.Background(), task, emitter)
	if err == nil {
		t.Fatal("expected error on mid-stream failure, got nil")
	}

	if !strings.Contains(err.Error(), "mid-stream connection dropped") {
		t.Errorf("expected 'mid-stream connection dropped' error, got: %v", err)
	}

	if fallbackCalled.Load() {
		t.Fatal("fallback agent was invoked after partial emission, expected NO fallback cascade")
	}

	events := emitter.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != model.EventTokenDelta {
		t.Errorf("expected EventTokenDelta, got %s", events[0].Type)
	}
}

func TestFallbackContextCancellation_Sync(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewCustomAdapter())

	// Primary fails with 500
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer primaryServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Fallback 1 cancels the parent context and returns 500
	fb1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fb1Server.Close()

	// Fallback 2 should never be reached
	var fb2Called atomic.Bool
	fb2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fb2Called.Store(true)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"answer": "never reached"}`))
	}))
	defer fb2Server.Close()

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:               "agent-1",
				Type:             "custom",
				Endpoint:         primaryServer.URL,
				Retries:          0,
				FallbackAgentIDs: []string{"agent-2", "agent-3"},
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
				},
			},
			{
				ID:       "agent-2",
				Type:     "custom",
				Endpoint: fb1Server.URL,
				Retries:  0,
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
				},
			},
			{
				ID:       "agent-3",
				Type:     "custom",
				Endpoint: fb2Server.URL,
				Retries:  0,
				Mapping: &config.CustomMapping{
					Request:  config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
					Response: config.ResponseMapping{OutputPath: "answer"},
				},
			},
		},
	}

	disp := New(cfg, reg)
	task := &model.TaskRequest{AgentID: "agent-1", Input: "test"}

	_, err := disp.Dispatch(ctx, task)
	if err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got: %v", err)
	}
	if fb2Called.Load() {
		t.Fatal("fallback agent-3 was invoked after context cancellation in fallback loop")
	}
}

func TestFallbackContextCancellation_Stream(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewCustomAdapter())

	// Primary fails with 500
	primaryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer primaryServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Fallback 1 cancels parent context and returns 500
	fb1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fb1Server.Close()

	// Fallback 2 should never be reached
	var fb2Called atomic.Bool
	fb2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fb2Called.Store(true)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer fb2Server.Close()

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:               "agent-1",
				Type:             "custom",
				Endpoint:         primaryServer.URL,
				Retries:          0,
				FallbackAgentIDs: []string{"agent-2", "agent-3"},
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
				},
			},
			{
				ID:       "agent-2",
				Type:     "custom",
				Endpoint: fb1Server.URL,
				Retries:  0,
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
				},
			},
			{
				ID:       "agent-3",
				Type:     "custom",
				Endpoint: fb2Server.URL,
				Retries:  0,
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
				},
			},
		},
	}

	disp := New(cfg, reg)
	task := &model.TaskRequest{AgentID: "agent-1", Input: "test", Stream: true}
	emitter := &recordingEmitter{}

	err := disp.DispatchStream(ctx, task, emitter)
	if err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got: %v", err)
	}
	if fb2Called.Load() {
		t.Fatal("fallback agent-3 was invoked after context cancellation in streaming fallback loop")
	}
}

func TestTransportPoolingConfiguration(t *testing.T) {
	cfg := &config.Config{}
	reg := adapter.NewRegistry()
	disp := New(cfg, reg)

	if disp.client == nil {
		t.Fatal("expected http client, got nil")
	}
	transport, ok := disp.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", disp.client.Transport)
	}
	if transport.MaxIdleConns != 100 {
		t.Errorf("expected MaxIdleConns=100, got %d", transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != 100 {
		t.Errorf("expected MaxIdleConnsPerHost=100, got %d", transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != 90*time.Second {
		t.Errorf("expected IdleConnTimeout=90s, got %v", transport.IdleConnTimeout)
	}
}

func TestBackoffPolicy_RandomizedJitter(t *testing.T) {
	bp := &BackoffPolicy{
		InitialInterval: 100 * time.Millisecond,
		Multiplier:      2.0,
		MaxInterval:     2 * time.Second,
	}

	for i := 0; i < 50; i++ {
		dur := bp.Duration(0)
		// 100ms * [0.8, 1.2] = [80ms, 120ms]
		if dur < 80*time.Millisecond || dur > 120*time.Millisecond {
			t.Fatalf("jittered duration %v out of expected range [80ms, 120ms]", dur)
		}
	}
}


