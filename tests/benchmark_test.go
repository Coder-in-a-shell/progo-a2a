package tests

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"a2a-proxy/pkg/adapter"
	"a2a-proxy/pkg/config"
	"a2a-proxy/pkg/dispatcher"
	"a2a-proxy/pkg/metrics"
	"a2a-proxy/pkg/model"
	"a2a-proxy/pkg/server"
	"a2a-proxy/tests/mock"
)

type benchmarkDiscardEmitter struct{}

func (b *benchmarkDiscardEmitter) Emit(eventType model.StreamEventType, data any) error {
	return nil
}

func BenchmarkSynchronousDispatch(b *testing.B) {
	// Setup mock backends with recording disabled to prevent unbounded memory growth during b.N iterations
	mockCfg := mock.MockServerConfig{DisableRecording: true}
	lgSrv := mock.NewMockLangGraphServer(mockCfg)
	defer lgSrv.Close()
	aiSrv := mock.NewMockOpenAIServer(mockCfg)
	defer aiSrv.Close()
	custSrv := mock.NewMockCustomServerWithConfig(mockCfg)
	defer custSrv.Close()

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:       "agent-openai",
				Type:     "openai",
				Endpoint: aiSrv.URL + "/v1/chat/completions",
			},
			{
				ID:       "agent-langgraph",
				Type:     "langgraph",
				Endpoint: lgSrv.URL + "/runs/wait",
			},
			{
				ID:       "agent-custom",
				Type:     "custom",
				Endpoint: custSrv.URL + "/webhook",
				Mapping: &config.CustomMapping{
					Request:  config.RequestTemplate{Method: "POST", BodyTemplate: `{"input": {{toJson .Task.Input}}}`},
					Response: config.ResponseMapping{OutputPath: "output", StatusPath: "status"},
				},
			},
		},
	}

	reg := adapter.NewRegistry()
	reg.Register(adapter.NewOpenAIAdapter())
	reg.Register(adapter.NewLangGraphAdapter())
	reg.Register(adapter.NewCustomAdapter())

	disp := dispatcher.New(cfg, reg)
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := server.SetupRouter(cfg, disp, server.WithMetricsRegistry(metrics.NewRegistry()), server.WithLogger(discardLogger))

	ctx := context.Background()

	b.Run("DirectDispatcher/OpenAI", func(b *testing.B) {
		task := &model.TaskRequest{
			AgentID: "agent-openai",
			Input:   "Benchmark synchronous input query",
		}
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := disp.Dispatch(ctx, task)
			if err != nil {
				b.Fatalf("dispatch error: %v", err)
			}
			if resp == nil || resp.Status != model.StatusCompleted {
				b.Fatalf("unexpected response: %v", resp)
			}
		}
	})

	b.Run("DirectDispatcher/LangGraph", func(b *testing.B) {
		task := &model.TaskRequest{
			AgentID: "agent-langgraph",
			Input:   "Benchmark langgraph query",
		}
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := disp.Dispatch(ctx, task)
			if err != nil {
				b.Fatalf("dispatch error: %v", err)
			}
			if resp == nil || resp.Status != model.StatusCompleted {
				b.Fatalf("unexpected response: %v", resp)
			}
		}
	})

	b.Run("DirectDispatcher/Custom", func(b *testing.B) {
		task := &model.TaskRequest{
			AgentID: "agent-custom",
			Input:   "Benchmark custom query",
		}
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			resp, err := disp.Dispatch(ctx, task)
			if err != nil {
				b.Fatalf("dispatch error: %v", err)
			}
			if resp == nil || resp.Status != model.StatusCompleted {
				b.Fatalf("unexpected response: %v", resp)
			}
		}
	})

	b.Run("FullRouter/A2ATasksEndpoint", func(b *testing.B) {
		taskBody := []byte(`{"agent_id":"agent-openai","input":"Benchmark full HTTP stack"}`)
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest("POST", "/a2a/v1/tasks", bytes.NewReader(taskBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("expected 200, got %d", rec.Code)
			}
		}
	})
}

func BenchmarkStreamingDispatch(b *testing.B) {
	mockCfg := mock.MockServerConfig{DisableRecording: true}
	aiSrv := mock.NewMockOpenAIServer(mockCfg)
	defer aiSrv.Close()
	custSrv := mock.NewMockCustomServerWithConfig(mockCfg)
	defer custSrv.Close()

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:       "agent-openai",
				Type:     "openai",
				Endpoint: aiSrv.URL + "/v1/chat/completions",
			},
			{
				ID:       "agent-custom",
				Type:     "custom",
				Endpoint: custSrv.URL + "/webhook",
				Mapping: &config.CustomMapping{
					Request: config.RequestTemplate{Method: "POST", BodyTemplate: `{"input": {{toJson .Task.Input}}}`},
					Stream:  config.StreamMapping{DataPath: "text", DoneSentinel: "[DONE]"},
				},
			},
		},
	}

	reg := adapter.NewRegistry()
	reg.Register(adapter.NewOpenAIAdapter())
	reg.Register(adapter.NewCustomAdapter())

	disp := dispatcher.New(cfg, reg)
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := server.SetupRouter(cfg, disp, server.WithMetricsRegistry(metrics.NewRegistry()), server.WithLogger(discardLogger))
	emitter := &benchmarkDiscardEmitter{}
	ctx := context.Background()

	b.Run("DirectDispatcherStream/OpenAI", func(b *testing.B) {
		task := &model.TaskRequest{
			AgentID: "agent-openai",
			Input:   "streaming benchmark prompt",
			Stream:  true,
		}
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := disp.DispatchStream(ctx, task, emitter); err != nil {
				b.Fatalf("dispatch stream error: %v", err)
			}
		}
	})

	b.Run("DirectDispatcherStream/Custom", func(b *testing.B) {
		task := &model.TaskRequest{
			AgentID: "agent-custom",
			Input:   "streaming benchmark prompt",
			Stream:  true,
		}
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := disp.DispatchStream(ctx, task, emitter); err != nil {
				b.Fatalf("dispatch stream error: %v", err)
			}
		}
	})

	b.Run("FullRouterStream/A2ATasksStreamEndpoint", func(b *testing.B) {
		taskBody := []byte(`{"agent_id":"agent-openai","input":"Benchmark stream HTTP"}`)
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest("POST", "/a2a/v1/tasks/stream", bytes.NewReader(taskBody))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("expected 200, got %d", rec.Code)
			}
		}
	})
}

func BenchmarkAdapterTranslation(b *testing.B) {
	ctx := context.Background()

	// 1. LangGraph Adapter Translation
	b.Run("LangGraph", func(b *testing.B) {
		ad := adapter.NewLangGraphAdapter()
		agent := &config.AgentConfig{
			ID:       "langgraph-agent",
			Endpoint: "http://upstream.local/runs/wait",
			Options:  map[string]any{"assistant_id": "test-assistant"},
		}
		task := &model.TaskRequest{
			ID:      "task-bench-1",
			AgentID: "langgraph-agent",
			Input:   "benchmark payload analysis",
			Context: map[string]any{"thread_id": "thread-bench"},
		}
		respJSON := `{"output":"State execution completed successfully","thread_id":"thread-bench"}`

		b.Run("TranslateRequest", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req, err := ad.TranslateRequest(ctx, agent, task)
				if err != nil || req == nil {
					b.Fatalf("translate request failed: %v", err)
				}
			}
		})

		b.Run("TranslateResponse", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				httpResp := &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(respJSON)),
				}
				taskResp, err := ad.TranslateResponse(ctx, agent, httpResp)
				if err != nil || taskResp == nil {
					b.Fatalf("translate response failed: %v", err)
				}
			}
		})
	})

	// 2. CrewAI Adapter Translation
	b.Run("CrewAI", func(b *testing.B) {
		ad := adapter.NewCrewAIAdapter()
		agent := &config.AgentConfig{
			ID:       "crewai-agent",
			Endpoint: "http://upstream.local/kickoff",
			Auth:     config.AuthConfig{Type: "header", HeaderName: "X-Crew-Token", HeaderValue: "token-123"},
		}
		task := &model.TaskRequest{
			ID:      "task-bench-2",
			AgentID: "crewai-agent",
			Input:   map[string]any{"topic": "artificial intelligence"},
		}
		respJSON := `{"result":"Crew mission accomplished","status":"SUCCESS","tasks_output":[{"output":"Done"}]}`

		b.Run("TranslateRequest", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req, err := ad.TranslateRequest(ctx, agent, task)
				if err != nil || req == nil {
					b.Fatalf("translate request failed: %v", err)
				}
			}
		})

		b.Run("TranslateResponse", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				httpResp := &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(respJSON)),
				}
				taskResp, err := ad.TranslateResponse(ctx, agent, httpResp)
				if err != nil || taskResp == nil {
					b.Fatalf("translate response failed: %v", err)
				}
			}
		})
	})

	// 3. AutoGen Adapter Translation
	b.Run("AutoGen", func(b *testing.B) {
		ad := adapter.NewAutoGenAdapter()
		agent := &config.AgentConfig{
			ID:       "autogen-agent",
			Endpoint: "http://upstream.local/chat",
			Options:  map[string]any{"sender": "proxy_user", "recipient": "assistant_bot"},
		}
		task := &model.TaskRequest{
			ID:      "task-bench-3",
			AgentID: "autogen-agent",
			Input:   "Can you help me solve this?",
		}
		respJSON := `{"reply":"Here is the solution to your question","chat_history":[{"content":"Done"}]}`

		b.Run("TranslateRequest", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req, err := ad.TranslateRequest(ctx, agent, task)
				if err != nil || req == nil {
					b.Fatalf("translate request failed: %v", err)
				}
			}
		})

		b.Run("TranslateResponse", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				httpResp := &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(respJSON)),
				}
				taskResp, err := ad.TranslateResponse(ctx, agent, httpResp)
				if err != nil || taskResp == nil {
					b.Fatalf("translate response failed: %v", err)
				}
			}
		})
	})

	// 4. OpenAI Adapter Translation
	b.Run("OpenAI", func(b *testing.B) {
		ad := adapter.NewOpenAIAdapter()
		agent := &config.AgentConfig{
			ID:       "openai-agent",
			Endpoint: "http://upstream.local/v1/chat/completions",
			Options:  map[string]any{"model": "gpt-4o"},
		}
		task := &model.TaskRequest{
			ID:      "task-bench-4",
			AgentID: "openai-agent",
			Input:   "Write a haiku about benchmarks",
		}
		respJSON := `{
			"id":"chatcmpl-bench",
			"object":"chat.completion",
			"choices":[{"index":0,"message":{"role":"assistant","content":"Fast code running swift\nAllocations kept so low\nBenchmarks show the way"}}]
		}`

		b.Run("TranslateRequest", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req, err := ad.TranslateRequest(ctx, agent, task)
				if err != nil || req == nil {
					b.Fatalf("translate request failed: %v", err)
				}
			}
		})

		b.Run("TranslateResponse", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				httpResp := &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(respJSON)),
				}
				taskResp, err := ad.TranslateResponse(ctx, agent, httpResp)
				if err != nil || taskResp == nil {
					b.Fatalf("translate response failed: %v", err)
				}
			}
		})
	})

	// 5. Custom Adapter Translation
	b.Run("Custom", func(b *testing.B) {
		ad := adapter.NewCustomAdapter()
		agent := &config.AgentConfig{
			ID:       "custom-agent",
			Endpoint: "http://upstream.local/webhook",
			Mapping: &config.CustomMapping{
				Request: config.RequestTemplate{
					Method:       "POST",
					BodyTemplate: `{"data": {{toJson .Task.Input}}, "agent_id": "{{.Agent.ID}}"}`,
					Headers:      map[string]string{"Content-Type": "application/json"},
				},
				Response: config.ResponseMapping{
					OutputPath:    "output.result",
					StatusPath:    "output.state",
					ArtifactsPath: "artifacts",
				},
			},
		}
		task := &model.TaskRequest{
			ID:      "task-bench-5",
			AgentID: "custom-agent",
			Input:   map[string]any{"data_id": 12345, "action": "process"},
		}
		respJSON := `{"output":{"result":"Processed 12345","state":"completed"},"artifacts":[{"name":"file.txt"}]}`

		b.Run("TranslateRequest", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req, err := ad.TranslateRequest(ctx, agent, task)
				if err != nil || req == nil {
					b.Fatalf("translate request failed: %v", err)
				}
			}
		})

		b.Run("TranslateResponse", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				httpResp := &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(respJSON)),
				}
				taskResp, err := ad.TranslateResponse(ctx, agent, httpResp)
				if err != nil || taskResp == nil {
					b.Fatalf("translate response failed: %v", err)
				}
			}
		})
	})
}
