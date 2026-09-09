package tests

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	"a2a-proxy/pkg/server"
	"a2a-proxy/tests/mock"
)

type testHarness struct {
	langGraphServer *mock.MockLangGraphServer
	crewAIServer    *mock.MockCrewAIServer
	autoGenServer   *mock.MockAutoGenServer
	openAIServer    *mock.MockOpenAIServer
	customServer    *mock.MockCustomServer
	fallbackServer  *mock.MockCustomServer
	failingServer    *mock.TransientFailureServer
	recoveringServer *mock.TransientFailureServer

	cfg        *config.Config
	metricsReg *metrics.Registry
	disp       *dispatcher.Dispatcher
	proxySrv   *httptest.Server
	client     *http.Client
}

func setupE2EHarness(t *testing.T) *testHarness {
	t.Helper()

	lgSrv := mock.NewMockLangGraphServer()
	crewSrv := mock.NewMockCrewAIServer()
	agSrv := mock.NewMockAutoGenServer()
	aiSrv := mock.NewMockOpenAIServer()
	custSrv := mock.NewMockCustomServer()

	// Fallback server: custom webhook returning JSON result or stream
	fbSrv := mock.NewMockCustomServer(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "text/event-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprint(w, "data: {\"text\": \"Fallback streamed chunk\"}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "completed",
			"output": "Fallback agent took over successfully",
		})
	})

	// Transient failure server: fails 10 times with 500
	failSrv := mock.NewTransientFailureServer(10)

	// Transient recovering server: fails 1 time with 500 then succeeds on retry
	recSrv := mock.NewTransientFailureServer(1)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:                "127.0.0.1",
			Port:                8080,
			ReadTimeoutSeconds:  10,
			WriteTimeoutSeconds: 10,
			IdleTimeoutSeconds:  10,
		},
		Security: config.SecurityConfig{
			Enabled: true,
			APIKeys: []config.APIKeyConfig{
				{
					Key:           "admin-key-secret",
					ClientID:      "admin-client",
					AllowedAgents: []string{"*"},
				},
				{
					Key:           "langgraph-only-key",
					ClientID:      "langgraph-client",
					AllowedAgents: []string{"agent-langgraph"},
				},
				{
					Key:           "custom-only-key",
					ClientID:      "custom-client",
					AllowedAgents: []string{"agent-custom"},
				},
				{
					Key:           "restricted-key",
					ClientID:      "restricted-client",
					AllowedAgents: []string{"agent-openai", "agent-crewai"},
				},
			},
		},
		Agents: []config.AgentConfig{
			{
				ID:             "agent-langgraph",
				Name:           "LangGraph Agent",
				Type:           "langgraph",
				Endpoint:       lgSrv.URL + "/runs/wait",
				Capabilities:   []string{"state_machine", "code_analysis"},
				Tags:           []string{"langgraph", "beta"},
				TimeoutSeconds: 5,
				Auth: config.AuthConfig{
					Type:  "bearer",
					Token: "lg-secret-token",
				},
				Options: map[string]any{
					"assistant_id": "test-assistant",
				},
			},
			{
				ID:             "agent-crewai",
				Name:           "CrewAI Agent",
				Type:           "crewai",
				Endpoint:       crewSrv.URL + "/kickoff",
				Capabilities:   []string{"multi_agent", "research"},
				Tags:           []string{"crewai"},
				TimeoutSeconds: 5,
				Auth: config.AuthConfig{
					Type:        "header",
					HeaderName:  "X-Crew-Key",
					HeaderValue: "crew-secret-value",
				},
			},
			{
				ID:             "agent-autogen",
				Name:           "AutoGen Agent",
				Type:           "autogen",
				Endpoint:       agSrv.URL + "/chat",
				Capabilities:   []string{"conversational", "reasoning"},
				Tags:           []string{"autogen"},
				TimeoutSeconds: 5,
				Auth: config.AuthConfig{
					Type:  "bearer",
					Token: "ag-secret-token",
				},
				Options: map[string]any{
					"sender": "proxy_user",
				},
			},
			{
				ID:             "agent-openai",
				Name:           "OpenAI Agent",
				Type:           "openai",
				Endpoint:       aiSrv.URL + "/v1/chat/completions",
				Capabilities:   []string{"completion", "summarization"},
				Tags:           []string{"openai", "gpt4"},
				TimeoutSeconds: 5,
				Auth: config.AuthConfig{
					Type:  "bearer",
					Token: "sk-mock-openai-key",
				},
				Options: map[string]any{
					"model": "gpt-4o",
				},
			},
			{
				ID:             "agent-custom",
				Name:           "Custom Agent",
				Type:           "custom",
				Endpoint:       custSrv.URL + "/webhook",
				Capabilities:   []string{"custom_tool", "data_extraction"},
				Tags:           []string{"custom"},
				TimeoutSeconds: 5,
				Auth: config.AuthConfig{
					Type:        "header",
					HeaderName:  "X-Custom-Auth",
					HeaderValue: "custom-token-abc",
				},
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{
						Method:       "POST",
						BodyTemplate: `{"input": {{toJson .Task.Input}}}`,
					},
					Response: config.ResponseMapping{
						OutputPath:    "output",
						StatusPath:    "status",
						ArtifactsPath: "artifacts",
					},
					Stream: config.StreamMapping{
						DataPath:     "text",
						DoneSentinel: "[DONE]",
					},
				},
			},
			{
				ID:               "agent-failing",
				Name:             "Failing Primary Agent",
				Type:             "custom",
				Endpoint:         failSrv.URL,
				Capabilities:     []string{"unreliable"},
				Retries:          1,
				FallbackAgentIDs: []string{"agent-fallback"},
				TimeoutSeconds:   5,
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{
						Method:       "POST",
						BodyTemplate: `{}`,
					},
				},
			},
			{
				ID:             "agent-fallback",
				Name:           "Fallback Backup Agent",
				Type:           "custom",
				Endpoint:       fbSrv.URL,
				Capabilities:   []string{"backup"},
				TimeoutSeconds: 5,
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{
						Method:       "POST",
						BodyTemplate: `{}`,
					},
					Response: config.ResponseMapping{
						OutputPath: "output",
						StatusPath: "status",
					},
					Stream: config.StreamMapping{
						DataPath:     "text",
						DoneSentinel: "[DONE]",
					},
				},
			},
			{
				ID:             "agent-recovering",
				Name:           "Transient Recovering Agent",
				Type:           "custom",
				Endpoint:       recSrv.URL,
				Capabilities:   []string{"recovering"},
				Retries:        2,
				TimeoutSeconds: 5,
				Mapping: &config.CustomMapping{
					Request:  config.RequestTemplate{Method: "POST", BodyTemplate: `{}`},
					Response: config.ResponseMapping{OutputPath: "output", StatusPath: "status"},
				},
			},
		},
	}

	reg := adapter.NewRegistry()
	reg.Register(adapter.NewLangGraphAdapter())
	reg.Register(adapter.NewCrewAIAdapter())
	reg.Register(adapter.NewAutoGenAdapter())
	reg.Register(adapter.NewOpenAIAdapter())
	reg.Register(adapter.NewCustomAdapter())

	metricsReg := metrics.NewRegistry()
	disp := dispatcher.New(cfg, reg)
	disp.SetBackoffPolicy(&dispatcher.BackoffPolicy{
		InitialInterval: time.Millisecond,
		Multiplier:      1.0,
		MaxInterval:     2 * time.Millisecond,
	})

	router := server.SetupRouter(cfg, disp, server.WithMetricsRegistry(metricsReg))
	proxySrv := httptest.NewServer(router)

	h := &testHarness{
		langGraphServer:  lgSrv,
		crewAIServer:     crewSrv,
		autoGenServer:    agSrv,
		openAIServer:     aiSrv,
		customServer:     custSrv,
		fallbackServer:   fbSrv,
		failingServer:    failSrv,
		recoveringServer: recSrv,
		cfg:              cfg,
		metricsReg:       metricsReg,
		disp:             disp,
		proxySrv:         proxySrv,
		client:           &http.Client{Timeout: 10 * time.Second},
	}

	t.Cleanup(func() {
		proxySrv.Close()
		lgSrv.Close()
		crewSrv.Close()
		agSrv.Close()
		aiSrv.Close()
		custSrv.Close()
		fbSrv.Close()
		failSrv.Close()
		recSrv.Close()
	})

	return h
}

func (h *testHarness) doRequest(method, path string, body any, apiKey string) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, h.proxySrv.URL+path, bodyReader)
	if err != nil {
		return nil, err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	return h.client.Do(req)
}

// TestE2ESynchronousTaskExecution_PresetsAndCustom tests POST /a2a/v1/tasks for all 5 agent types.
func TestE2ESynchronousTaskExecution_PresetsAndCustom(t *testing.T) {
	h := setupE2EHarness(t)

	tests := []struct {
		name           string
		agentID        string
		input          any
		context        map[string]any
		expectedStatus model.TaskStatus
		checkOutput    func(t *testing.T, output any)
		checkOutbound  func(t *testing.T)
	}{
		{
			name:           "LangGraph Preset Synchronous Execution",
			agentID:        "agent-langgraph",
			input:          "analyze repo state",
			context:        map[string]any{"thread_id": "thread-42"},
			expectedStatus: model.StatusCompleted,
			checkOutput: func(t *testing.T, output any) {
				s, ok := output.(string)
				if !ok || !strings.Contains(s, "LangGraph output for thread thread-42") {
					t.Errorf("unexpected langgraph output: %v", output)
				}
			},
			checkOutbound: func(t *testing.T) {
				if h.langGraphServer.LastHeader("Authorization") != "Bearer lg-secret-token" {
					t.Errorf("expected bearer token lg-secret-token, got %s", h.langGraphServer.LastHeader("Authorization"))
				}
				history := h.langGraphServer.GetThreadHistory("thread-42")
				if len(history) == 0 || !strings.Contains(history[0], "analyze repo state") {
					t.Errorf("expected thread history to record input, got: %v", history)
				}
			},
		},
		{
			name:           "CrewAI Preset Synchronous Execution",
			agentID:        "agent-crewai",
			input:          map[string]any{"topic": "quantum computing"},
			expectedStatus: model.StatusCompleted,
			checkOutput: func(t *testing.T, output any) {
				s, ok := output.(string)
				if !ok || !strings.Contains(s, "CrewAI task successfully finished") {
					t.Errorf("unexpected crewai output: %v", output)
				}
			},
			checkOutbound: func(t *testing.T) {
				if h.crewAIServer.LastHeader("X-Crew-Key") != "crew-secret-value" {
					t.Errorf("expected header X-Crew-Key crew-secret-value, got %s", h.crewAIServer.LastHeader("X-Crew-Key"))
				}
			},
		},
		{
			name:           "AutoGen Preset Synchronous Execution",
			agentID:        "agent-autogen",
			input:          "solve this math problem",
			expectedStatus: model.StatusCompleted,
			checkOutput: func(t *testing.T, output any) {
				s, ok := output.(string)
				if !ok || !strings.Contains(s, "AutoGen conversation reply message") {
					t.Errorf("unexpected autogen output: %v", output)
				}
			},
			checkOutbound: func(t *testing.T) {
				if h.autoGenServer.LastHeader("Authorization") != "Bearer ag-secret-token" {
					t.Errorf("expected bearer ag-secret-token, got %s", h.autoGenServer.LastHeader("Authorization"))
				}
			},
		},
		{
			name:           "OpenAI Preset Synchronous Execution",
			agentID:        "agent-openai",
			input:          "tell me a joke",
			expectedStatus: model.StatusCompleted,
			checkOutput: func(t *testing.T, output any) {
				s, ok := output.(string)
				if !ok || !strings.Contains(s, "OpenAI mock completed successfully") {
					t.Errorf("unexpected openai output: %v", output)
				}
			},
			checkOutbound: func(t *testing.T) {
				if h.openAIServer.LastHeader("Authorization") != "Bearer sk-mock-openai-key" {
					t.Errorf("expected bearer sk-mock-openai-key, got %s", h.openAIServer.LastHeader("Authorization"))
				}
			},
		},
		{
			name:           "Custom Webhook Agent Synchronous Execution",
			agentID:        "agent-custom",
			input:          map[string]any{"query": "extract metadata"},
			expectedStatus: model.StatusCompleted,
			checkOutput: func(t *testing.T, output any) {
				s, ok := output.(string)
				if !ok || !strings.Contains(s, "Custom webhook mock output") {
					t.Errorf("unexpected custom webhook output: %v", output)
				}
			},
			checkOutbound: func(t *testing.T) {
				if h.customServer.LastHeader("X-Custom-Auth") != "custom-token-abc" {
					t.Errorf("expected header X-Custom-Auth custom-token-abc, got %s", h.customServer.LastHeader("X-Custom-Auth"))
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			taskReq := model.TaskRequest{
				ID:      fmt.Sprintf("e2e-task-%s", tc.agentID),
				AgentID: tc.agentID,
				Input:   tc.input,
				Context: tc.context,
			}

			resp, err := h.doRequest("POST", "/a2a/v1/tasks", taskReq, "admin-key-secret")
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				bodyBytes, _ := io.ReadAll(resp.Body)
				t.Fatalf("expected status 200, got %d (body: %s)", resp.StatusCode, string(bodyBytes))
			}

			var taskResp model.TaskResponse
			if err := json.NewDecoder(resp.Body).Decode(&taskResp); err != nil {
				t.Fatalf("decode failed: %v", err)
			}

			if taskResp.AgentID != tc.agentID {
				t.Errorf("expected agent ID %s, got %s", tc.agentID, taskResp.AgentID)
			}
			if taskResp.Status != tc.expectedStatus {
				t.Errorf("expected status %s, got %s", tc.expectedStatus, taskResp.Status)
			}
			if tc.checkOutput != nil {
				tc.checkOutput(t, taskResp.Output)
			}
			if tc.checkOutbound != nil {
				tc.checkOutbound(t)
			}

			// Verify task can be retrieved by ID via GET /a2a/v1/tasks/{task_id}
			getResp, err := h.doRequest("GET", "/a2a/v1/tasks/"+taskReq.ID, nil, "admin-key-secret")
			if err != nil {
				t.Fatalf("get task failed: %v", err)
			}
			defer getResp.Body.Close()

			if getResp.StatusCode != http.StatusOK {
				t.Fatalf("expected status 200 on task retrieval, got %d", getResp.StatusCode)
			}
			var retrieved model.TaskResponse
			if err := json.NewDecoder(getResp.Body).Decode(&retrieved); err != nil {
				t.Fatalf("decode retrieved task failed: %v", err)
			}
			if retrieved.TaskID != taskReq.ID {
				t.Errorf("expected retrieved TaskID %s, got %s", taskReq.ID, retrieved.TaskID)
			}
		})
	}
}

// TestE2EUnifiedRESTInvocation tests direct invocation via POST /api/v1/invoke/{agent_id}.
func TestE2EUnifiedRESTInvocation(t *testing.T) {
	h := setupE2EHarness(t)

	// Direct REST invoke for OpenAI agent
	body := map[string]any{"prompt": "Say hello from unified REST"}
	resp, err := h.doRequest("POST", "/api/v1/invoke/agent-openai", body, "admin-key-secret")
	if err != nil {
		t.Fatalf("invoke failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(b))
	}

	var taskResp model.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&taskResp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if taskResp.AgentID != "agent-openai" {
		t.Errorf("expected agent-openai, got %s", taskResp.AgentID)
	}
	if taskResp.Status != model.StatusCompleted {
		t.Errorf("expected COMPLETED status, got %s", taskResp.Status)
	}
	if outStr, ok := taskResp.Output.(string); !ok || !strings.Contains(outStr, "OpenAI mock completed successfully") {
		t.Errorf("unexpected output: %v", taskResp.Output)
	}

	// Verify the invoked task was cached in taskStore
	getResp, err := h.doRequest("GET", "/a2a/v1/tasks/"+taskResp.TaskID, nil, "admin-key-secret")
	if err != nil {
		t.Fatalf("get task failed: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 on retrieving task created via REST, got %d", getResp.StatusCode)
	}

	// Non-existent agent returns 404
	errResp, err := h.doRequest("POST", "/api/v1/invoke/agent-nonexistent", body, "admin-key-secret")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer errResp.Body.Close()
	if errResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent agent, got %d", errResp.StatusCode)
	}
}

// TestE2ESSEStreamingExecution tests real-time SSE streaming for A2A and REST endpoints.
func TestE2ESSEStreamingExecution(t *testing.T) {
	h := setupE2EHarness(t)

	// Case 1: Standard A2A Streaming via /a2a/v1/tasks/stream with OpenAI agent
	t.Run("A2A Streaming OpenAI", func(t *testing.T) {
		taskReq := model.TaskRequest{
			ID:      "stream-openai-task",
			AgentID: "agent-openai",
			Input:   "streaming prompt",
			Stream:  true,
		}

		resp, err := h.doRequest("POST", "/a2a/v1/tasks/stream", taskReq, "admin-key-secret")
		if err != nil {
			t.Fatalf("stream request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(b))
		}

		if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("expected text/event-stream, got %s", resp.Header.Get("Content-Type"))
		}

		events := parseSSEEvents(t, resp.Body)
		if len(events) < 3 {
			t.Fatalf("expected at least 3 SSE events, got %d", len(events))
		}

		// First event must be task_started
		if events[0].Event != string(model.EventTaskStarted) {
			t.Errorf("expected first event task_started, got %s", events[0].Event)
		}

		// Intermediate events should contain token_delta
		var assembledText strings.Builder
		for _, ev := range events {
			if ev.Event == string(model.EventTokenDelta) {
				var dataMap map[string]any
				if err := json.Unmarshal([]byte(ev.Data), &dataMap); err == nil {
					if d, ok := dataMap["delta"].(string); ok {
						assembledText.WriteString(d)
					}
				}
			}
		}

		if assembledText.String() != "Hello from OpenAI mock!" {
			t.Errorf("expected full streamed string 'Hello from OpenAI mock!', got '%s'", assembledText.String())
		}

		// Last event must be task_completed
		lastEvent := events[len(events)-1]
		if lastEvent.Event != string(model.EventTaskCompleted) {
			t.Errorf("expected last event task_completed, got %s", lastEvent.Event)
		}
	})

	// Case 2: Unified REST Streaming via /api/v1/stream/{agent_id} with LangGraph agent
	t.Run("REST Streaming LangGraph", func(t *testing.T) {
		body := map[string]any{
			"input": "stream analysis",
			"context": map[string]any{
				"thread_id": "thread-stream-99",
			},
		}

		resp, err := h.doRequest("POST", "/api/v1/stream/agent-langgraph", body, "admin-key-secret")
		if err != nil {
			t.Fatalf("stream request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(b))
		}

		events := parseSSEEvents(t, resp.Body)
		if len(events) < 4 {
			t.Fatalf("expected at least 4 SSE events for LangGraph, got %d", len(events))
		}

		// Verify event sequence contains task_started, step_progress, token_delta, task_completed
		var seenStarted, seenProgress, seenDelta, seenCompleted bool
		for _, ev := range events {
			switch ev.Event {
			case string(model.EventTaskStarted):
				seenStarted = true
			case string(model.EventStepProgress):
				seenProgress = true
			case string(model.EventTokenDelta):
				seenDelta = true
			case string(model.EventTaskCompleted):
				seenCompleted = true
			}
		}

		if !seenStarted {
			t.Errorf("missing task_started event")
		}
		if !seenProgress {
			t.Errorf("missing step_progress event from LangGraph values")
		}
		if !seenDelta {
			t.Errorf("missing token_delta event from LangGraph messages")
		}
		if !seenCompleted {
			t.Errorf("missing task_completed event")
		}
	})

	// Case 3: Custom Webhook Streaming via /a2a/v1/tasks/stream
	t.Run("A2A Streaming Custom Webhook", func(t *testing.T) {
		taskReq := model.TaskRequest{
			ID:      "stream-custom-task",
			AgentID: "agent-custom",
			Input:   "stream custom data",
			Stream:  true,
		}

		resp, err := h.doRequest("POST", "/a2a/v1/tasks/stream", taskReq, "admin-key-secret")
		if err != nil {
			t.Fatalf("stream request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(b))
		}

		events := parseSSEEvents(t, resp.Body)
		if len(events) < 3 {
			t.Fatalf("expected at least 3 events, got %d", len(events))
		}
		if events[0].Event != string(model.EventTaskStarted) {
			t.Errorf("expected first event task_started, got %s", events[0].Event)
		}
		if events[len(events)-1].Event != string(model.EventTaskCompleted) {
			t.Errorf("expected last event task_completed, got %s", events[len(events)-1].Event)
		}
	})
}

// TestE2EAuthenticationAndRBAC verifies 401s on missing/invalid keys and 403s on unpermitted agents.
func TestE2EAuthenticationAndRBAC(t *testing.T) {
	h := setupE2EHarness(t)

	// 1. Missing Authorization header -> 401
	respNoAuth, err := h.doRequest("POST", "/a2a/v1/tasks", map[string]any{"agent_id": "agent-openai"}, "")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer respNoAuth.Body.Close()
	if respNoAuth.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 on missing auth, got %d", respNoAuth.StatusCode)
	}

	// 2. Invalid API key -> 401
	respBadKey, err := h.doRequest("POST", "/a2a/v1/tasks", map[string]any{"agent_id": "agent-openai"}, "invalid-key")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer respBadKey.Body.Close()
	if respBadKey.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 on bad key, got %d", respBadKey.StatusCode)
	}

	// 3. Restricted API key accessing unpermitted agent on /a2a/v1/tasks -> 403
	// langgraph-only-key only allows "agent-langgraph"
	respForbiddenA2A, err := h.doRequest("POST", "/a2a/v1/tasks", map[string]any{"agent_id": "agent-openai", "input": "test"}, "langgraph-only-key")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer respForbiddenA2A.Body.Close()
	if respForbiddenA2A.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for unpermitted agent on A2A dispatch, got %d", respForbiddenA2A.StatusCode)
	}

	// 4. Restricted API key accessing unpermitted agent on /api/v1/invoke -> 403
	respForbiddenREST, err := h.doRequest("POST", "/api/v1/invoke/agent-openai", map[string]any{"prompt": "test"}, "langgraph-only-key")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer respForbiddenREST.Body.Close()
	if respForbiddenREST.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for unpermitted agent on REST invoke, got %d", respForbiddenREST.StatusCode)
	}

	// 5. Restricted API key accessing unpermitted agent on /a2a/v1/tasks/stream -> 403
	respForbiddenStreamA2A, err := h.doRequest("POST", "/a2a/v1/tasks/stream", map[string]any{"agent_id": "agent-openai", "input": "test"}, "langgraph-only-key")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer respForbiddenStreamA2A.Body.Close()
	if respForbiddenStreamA2A.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for unpermitted streaming agent on A2A, got %d", respForbiddenStreamA2A.StatusCode)
	}

	// 6. Restricted API key accessing unpermitted agent on /api/v1/stream -> 403
	respForbiddenStreamREST, err := h.doRequest("POST", "/api/v1/stream/agent-openai", map[string]any{"prompt": "test"}, "langgraph-only-key")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer respForbiddenStreamREST.Body.Close()
	if respForbiddenStreamREST.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for unpermitted streaming agent on REST, got %d", respForbiddenStreamREST.StatusCode)
	}

	// 7. Restricted API key accessing permitted agent -> 200 OK
	respAllowed, err := h.doRequest("POST", "/a2a/v1/tasks", map[string]any{"agent_id": "agent-langgraph", "input": "allowed run"}, "langgraph-only-key")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer respAllowed.Body.Close()
	if respAllowed.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(respAllowed.Body)
		t.Errorf("expected 200 for permitted agent, got %d: %s", respAllowed.StatusCode, string(b))
	}

	// 8. Wildcard admin key accessing any agent -> 200 OK
	respAdmin, err := h.doRequest("POST", "/a2a/v1/tasks", map[string]any{"agent_id": "agent-openai", "input": "admin run"}, "admin-key-secret")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer respAdmin.Body.Close()
	if respAdmin.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(respAdmin.Body)
		t.Errorf("expected 200 for wildcard key, got %d: %s", respAdmin.StatusCode, string(b))
	}

	// 9. Public endpoints (healthz, readyz, metrics) accessible without authentication
	for _, p := range []string{"/healthz", "/readyz", "/metrics"} {
		res, err := h.doRequest("GET", p, nil, "")
		if err != nil {
			t.Fatalf("request to public endpoint %s failed: %v", p, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("expected 200 for public endpoint %s without auth, got %d", p, res.StatusCode)
		}
	}
}

// TestE2ECapabilityBasedRouting tests resolving agents by capability instead of agent_id.
func TestE2ECapabilityBasedRouting(t *testing.T) {
	h := setupE2EHarness(t)

	// Case 1: Route by capability "completion" -> resolves to agent-openai
	taskReq1 := model.TaskRequest{
		ID:                 "cap-task-1",
		RequiredCapability: "completion",
		Input:              "route me by capability",
	}
	resp1, err := h.doRequest("POST", "/a2a/v1/tasks", taskReq1, "admin-key-secret")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp1.Body)
		t.Fatalf("expected 200, got %d: %s", resp1.StatusCode, string(b))
	}

	var taskResp1 model.TaskResponse
	if err := json.NewDecoder(resp1.Body).Decode(&taskResp1); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if taskResp1.AgentID != "agent-openai" {
		t.Errorf("expected capability 'completion' to route to agent-openai, got %s", taskResp1.AgentID)
	}

	// Case 2: Route by capability "multi_agent" -> resolves to agent-crewai
	taskReq2 := model.TaskRequest{
		ID:                 "cap-task-2",
		RequiredCapability: "multi_agent",
		Input:              map[string]any{"crew": "start"},
	}
	resp2, err := h.doRequest("POST", "/a2a/v1/tasks", taskReq2, "admin-key-secret")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected 200, got %d: %s", resp2.StatusCode, string(b))
	}
	var taskResp2 model.TaskResponse
	if err := json.NewDecoder(resp2.Body).Decode(&taskResp2); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if taskResp2.AgentID != "agent-crewai" {
		t.Errorf("expected capability 'multi_agent' to route to agent-crewai, got %s", taskResp2.AgentID)
	}

	// Case 3: Capability resolving to forbidden agent -> 403
	// langgraph-only-key only allows agent-langgraph; capability "completion" resolves to agent-openai
	resp3, err := h.doRequest("POST", "/a2a/v1/tasks", taskReq1, "langgraph-only-key")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp3.Body.Close()

	if resp3.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 when capability resolves to forbidden agent, got %d", resp3.StatusCode)
	}

	// Case 4: Non-existent capability -> 404
	taskReqMissing := model.TaskRequest{
		RequiredCapability: "teleportation",
		Input:              "beam me up",
	}
	resp4, err := h.doRequest("POST", "/a2a/v1/tasks", taskReqMissing, "admin-key-secret")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp4.Body.Close()

	if resp4.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown capability, got %d", resp4.StatusCode)
	}
}

// TestE2ERetryAndFallbackFailover tests fallback failover when primary agent returns 500.
func TestE2ERetryAndFallbackFailover(t *testing.T) {
	h := setupE2EHarness(t)

	// Case 1: Synchronous Failover
	// agent-failing retries once against failingServer (which returns 500), then cascades to agent-fallback
	taskReq := model.TaskRequest{
		ID:      "task-fallback-sync",
		AgentID: "agent-failing",
		Input:   "execute primary or fallback",
	}

	resp, err := h.doRequest("POST", "/a2a/v1/tasks", taskReq, "admin-key-secret")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 from fallback agent, got %d: %s", resp.StatusCode, string(b))
	}

	var taskResp model.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&taskResp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	// The response should originate from agent-fallback
	if taskResp.AgentID != "agent-fallback" {
		t.Errorf("expected response from agent-fallback, got %s", taskResp.AgentID)
	}
	if taskResp.Status != model.StatusCompleted {
		t.Errorf("expected status COMPLETED, got %s", taskResp.Status)
	}
	if outStr, ok := taskResp.Output.(string); !ok || !strings.Contains(outStr, "Fallback agent took over successfully") {
		t.Errorf("unexpected output from fallback: %v", taskResp.Output)
	}

	// Verify failing server was called 2 times (attempt 0 + 1 retry)
	if h.failingServer.Attempts() < 2 {
		t.Errorf("expected at least 2 attempts on failing agent, got %d", h.failingServer.Attempts())
	}

	// Case 2: Streaming Failover
	// Streaming task against agent-failing should cascade to agent-fallback before stream starts
	streamTaskReq := model.TaskRequest{
		ID:      "task-fallback-stream",
		AgentID: "agent-failing",
		Input:   "stream fallback test",
		Stream:  true,
	}

	streamResp, err := h.doRequest("POST", "/a2a/v1/tasks/stream", streamTaskReq, "admin-key-secret")
	if err != nil {
		t.Fatalf("stream fallback request failed: %v", err)
	}
	defer streamResp.Body.Close()

	if streamResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(streamResp.Body)
		t.Fatalf("expected 200 on streaming fallback, got %d: %s", streamResp.StatusCode, string(b))
	}

	events := parseSSEEvents(t, streamResp.Body)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 SSE events from streaming fallback, got %d", len(events))
	}

	var foundFallbackChunk bool
	for _, ev := range events {
		if strings.Contains(ev.Data, "Fallback streamed chunk") {
			foundFallbackChunk = true
			break
		}
	}
	if !foundFallbackChunk {
		t.Errorf("expected 'Fallback streamed chunk' in stream events, got: %+v", events)
	}

	// Case 3: Transient Retry Recovery
	recReq := model.TaskRequest{
		ID:      "task-retry-success",
		AgentID: "agent-recovering",
		Input:   "transient test",
	}

	recResp, err := h.doRequest("POST", "/a2a/v1/tasks", recReq, "admin-key-secret")
	if err != nil {
		t.Fatalf("recovering request failed: %v", err)
	}
	defer recResp.Body.Close()

	if recResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(recResp.Body)
		t.Fatalf("expected 200 from recovering agent, got %d: %s", recResp.StatusCode, string(b))
	}

	if h.recoveringServer.Attempts() != 2 {
		t.Errorf("expected 2 attempts before success on recovering server, got %d", h.recoveringServer.Attempts())
	}
}

// TestE2EPrometheusMetricsVerification verifies /metrics endpoint counters and gauges.
func TestE2EPrometheusMetricsVerification(t *testing.T) {
	h := setupE2EHarness(t)

	// Make 2 successful requests
	resp1, err := h.doRequest("POST", "/a2a/v1/tasks", model.TaskRequest{AgentID: "agent-openai", Input: "m1"}, "admin-key-secret")
	if err != nil {
		t.Fatalf("request 1 failed: %v", err)
	}
	resp1.Body.Close()

	resp2, err := h.doRequest("POST", "/a2a/v1/tasks", model.TaskRequest{AgentID: "agent-crewai", Input: "m2"}, "admin-key-secret")
	if err != nil {
		t.Fatalf("request 2 failed: %v", err)
	}
	resp2.Body.Close()

	// Trigger 1 fallback
	resp3, err := h.doRequest("POST", "/a2a/v1/tasks", model.TaskRequest{AgentID: "agent-failing", Input: "trigger fallback"}, "admin-key-secret")
	if err != nil {
		t.Fatalf("request 3 failed: %v", err)
	}
	resp3.Body.Close()

	// Fetch /metrics
	metricsResp, err := h.doRequest("GET", "/metrics", nil, "")
	if err != nil {
		t.Fatalf("metrics request failed: %v", err)
	}
	defer metricsResp.Body.Close()

	if metricsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /metrics, got %d", metricsResp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(metricsResp.Body)
	if err != nil {
		t.Fatalf("read metrics body failed: %v", err)
	}
	metricsText := string(bodyBytes)

	// 1. Verify a2a_requests_total counter
	if !strings.Contains(metricsText, "a2a_requests_total") {
		t.Errorf("expected 'a2a_requests_total' metric, got:\n%s", metricsText)
	}

	// 2. Verify a2a_fallback_triggered_total counter
	if !strings.Contains(metricsText, "a2a_fallback_triggered_total") {
		t.Errorf("expected 'a2a_fallback_triggered_total' metric, got:\n%s", metricsText)
	}
	if !strings.Contains(metricsText, "agent-failing") || !strings.Contains(metricsText, "agent-fallback") {
		t.Errorf("expected fallback labels for agent-failing -> agent-fallback, got:\n%s", metricsText)
	}

	// 3. Verify active streams gauge is present and zero
	if !strings.Contains(metricsText, "a2a_active_streams 0") {
		t.Errorf("expected 'a2a_active_streams 0', got:\n%s", metricsText)
	}

	// 4. Verify request duration metric
	if !strings.Contains(metricsText, "a2a_request_duration_seconds") {
		t.Errorf("expected 'a2a_request_duration_seconds' metric, got:\n%s", metricsText)
	}
}

type sseEvent struct {
	Event string
	Data  string
}

func parseSSEEvents(t *testing.T, r io.Reader) []sseEvent {
	t.Helper()
	var events []sseEvent
	scanner := bufio.NewScanner(r)

	var currentEvent, currentData string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if currentEvent != "" || currentData != "" {
				events = append(events, sseEvent{Event: currentEvent, Data: currentData})
				currentEvent = ""
				currentData = ""
			}
			continue
		}

		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			d := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if currentData != "" {
				currentData += "\n" + d
			} else {
				currentData = d
			}
		}
	}

	if currentEvent != "" || currentData != "" {
		events = append(events, sseEvent{Event: currentEvent, Data: currentData})
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error reading SSE stream: %v", err)
	}

	return events
}
