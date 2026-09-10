package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/adapter"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/dispatcher"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
)

func setupTestDispatcher() *dispatcher.Dispatcher {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:             "agent-1",
				Name:           "Test Agent",
				Type:           "openai",
				Endpoint:       "http://127.0.0.1:8080/v1/chat",
				Capabilities:   []string{"chat"},
				TimeoutSeconds: 5,
			},
		},
	}
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewOpenAIAdapter())
	return dispatcher.New(cfg, reg)
}

func TestDispatcherExecutor_ValidationAndPayloads(t *testing.T) {
	disp := setupTestDispatcher()
	exec := NewDispatcherExecutor(disp)
	ctx := context.Background()

	t.Run("nil job returns error", func(t *testing.T) {
		resp, err := exec.Execute(ctx, nil)
		if resp != nil {
			t.Fatalf("expected nil resp, got %v", resp)
		}
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var execErr *ExecutionError
		if !errors.As(err, &execErr) || execErr.Code != "INVALID_JOB" {
			t.Fatalf("expected INVALID_JOB error, got %v", err)
		}
	})

	t.Run("empty request payload returns error", func(t *testing.T) {
		job := &model.Job{
			ID:      "job-empty",
			Request: nil,
		}
		resp, err := exec.Execute(ctx, job)
		if resp != nil {
			t.Fatalf("expected nil resp, got %v", resp)
		}
		var execErr *ExecutionError
		if !errors.As(err, &execErr) || execErr.Code != "INVALID_PAYLOAD" {
			t.Fatalf("expected INVALID_PAYLOAD, got %v", err)
		}
	})

	t.Run("malformed json payload returns error", func(t *testing.T) {
		job := &model.Job{
			ID:      "job-malformed",
			Request: json.RawMessage(`{not-json`),
		}
		resp, err := exec.Execute(ctx, job)
		if resp != nil {
			t.Fatalf("expected nil resp, got %v", resp)
		}
		var execErr *ExecutionError
		if !errors.As(err, &execErr) || execErr.Code != "INVALID_PAYLOAD" {
			t.Fatalf("expected INVALID_PAYLOAD, got %v", err)
		}
	})

	t.Run("nil dispatcher returns non-retryable ExecutionError", func(t *testing.T) {
		nilExec := NewDispatcherExecutor(nil)
		resp, err := nilExec.Execute(ctx, &model.Job{ID: "job-1", Request: []byte(`{"input":"hello"}`)})
		if resp != nil {
			t.Fatalf("expected nil resp, got %v", resp)
		}
		var execErr *ExecutionError
		if !errors.As(err, &execErr) || execErr.Code != "NIL_DISPATCHER" {
			t.Fatalf("expected NIL_DISPATCHER error, got %v", err)
		}
		if execErr.Retryable {
			t.Error("expected non-retryable error")
		}
	})

	t.Run("streaming job is rejected", func(t *testing.T) {
		req := model.TaskRequest{
			Input:  "hello",
			Stream: true,
		}
		payload, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}
		job := &model.Job{
			ID:      "job-stream",
			Request: payload,
		}
		resp, err := exec.Execute(ctx, job)
		if resp != nil {
			t.Fatalf("expected nil resp, got %v", resp)
		}
		var execErr *ExecutionError
		if !errors.As(err, &execErr) || execErr.Code != "STREAMING_NOT_SUPPORTED" {
			t.Fatalf("expected STREAMING_NOT_SUPPORTED, got %v", err)
		}
		if execErr.Retryable {
			t.Error("streaming rejection must not be retryable")
		}
	})
}

func TestDispatcherExecutor_OverwritesRoutingAndTaskID(t *testing.T) {
	var capturedRequest *http.Request
	server := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedRequest = r
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-123",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "hello world",
					},
					"finish_reason": "stop",
				},
			},
		})
	})

	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ID:           "trusted-agent",
				Type:         "openai",
				Endpoint:     "http://downstream.test/v1/chat",
				Capabilities: []string{"chat"},
			},
		},
	}
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewOpenAIAdapter())
	disp := dispatcher.New(cfg, reg)

	// RoundTripper stub
	disp.SetHTTPClient(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			capturedRequest = req
			rec := &testResponseRecorder{
				header: make(http.Header),
			}
			server.ServeHTTP(rec, req)
			return rec.Result(), nil
		}),
	})

	exec := NewDispatcherExecutor(disp)

	// User untrusted payload attempts to forge task ID and agent ID
	untrustedReq := model.TaskRequest{
		ID:      "forged-task-id",
		AgentID: "untrusted-agent",
		Input:   "hello test",
	}
	payload, err := json.Marshal(untrustedReq)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	job := &model.Job{
		ID:          "trusted-job-123",
		TenantID:    "tenant-1",
		AgentID:     "trusted-agent",
		MaxAttempts: 1,
		Request:     payload,
	}

	resp, err := exec.Execute(context.Background(), job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	taskResp, ok := resp.(*model.TaskResponse)
	if !ok {
		t.Fatalf("expected *model.TaskResponse, got %T", resp)
	}

	if taskResp.TaskID != "trusted-job-123" {
		t.Errorf("expected TaskID to be overwritten with trusted job ID 'trusted-job-123', got %q", taskResp.TaskID)
	}
	if taskResp.AgentID != "trusted-agent" {
		t.Errorf("expected AgentID to be overwritten with trusted job agent 'trusted-agent', got %q", taskResp.AgentID)
	}
	if capturedRequest == nil {
		t.Fatal("expected downstream request to be captured")
	}
}

func TestDispatcherExecutor_UsesSingleDownstreamAttempt(t *testing.T) {
	cfg := &config.Config{Agents: []config.AgentConfig{{
		ID: "agent-1", Type: "openai", Endpoint: "http://downstream.test/v1/chat",
		Retries: 10,
	}}}
	reg := adapter.NewRegistry()
	reg.Register(adapter.NewOpenAIAdapter())
	disp := dispatcher.New(cfg, reg)
	attempts := 0
	disp.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("network unavailable")
	})})

	payload, err := json.Marshal(model.TaskRequest{Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewDispatcherExecutor(disp).Execute(context.Background(), &model.Job{
		ID: "job-1", AgentID: "agent-1", MaxAttempts: 3, Request: payload,
	})
	if err == nil {
		t.Fatal("expected downstream error")
	}
	if attempts != 1 {
		t.Fatalf("durable execution made %d downstream attempts, want exactly 1", attempts)
	}
	var execErr *ExecutionError
	if !errors.As(err, &execErr) || execErr.Code != "DOWNSTREAM_UNAVAILABLE" || !execErr.Retryable {
		t.Fatalf("unexpected execution error: %#v", err)
	}
}

func TestSanitizeFailure_NeverLeaksRawErrors(t *testing.T) {
	secretURL := "https://api.openai.com/v1/secret-key-12345/chat"
	rawErr := errors.New("dial tcp 10.0.0.1:443: connect: connection refused to " + secretURL)

	code, msg, retryable := SanitizeFailure(rawErr, 1)
	if strings.Contains(msg, secretURL) {
		t.Errorf("sanitized message leaked URL: %s", msg)
	}
	if strings.Contains(msg, "connection refused") {
		t.Errorf("sanitized message leaked raw error text: %s", msg)
	}
	if code != "INTERNAL_ERROR" {
		t.Errorf("expected code INTERNAL_ERROR, got %s", code)
	}
	if msg != "internal execution failure" {
		t.Errorf("expected stable generic message, got %s", msg)
	}
	if retryable {
		t.Error("maxAttempts=1 must not be retryable")
	}

	// Downstream timeout with maxAttempts > 1 is retryable
	timeoutErr := &model.A2AError{
		Code:    "DOWNSTREAM_TIMEOUT",
		Message: "dial timeout to " + secretURL,
	}
	code, msg, retryable = SanitizeFailure(timeoutErr, 3)
	if code != "DOWNSTREAM_TIMEOUT" {
		t.Errorf("expected code DOWNSTREAM_TIMEOUT, got %s", code)
	}
	if strings.Contains(msg, secretURL) {
		t.Errorf("sanitized timeout message leaked URL: %s", msg)
	}
	if !retryable {
		t.Error("expected retryable=true when maxAttempts > 1 for timeout")
	}

	// Downstream timeout with maxAttempts = 1 is NOT retryable
	code, msg, retryable = SanitizeFailure(timeoutErr, 1)
	if retryable {
		t.Error("expected retryable=false when maxAttempts <= 1")
	}
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type testResponseRecorder struct {
	header http.Header
	body   []byte
	status int
}

func (r *testResponseRecorder) Header() http.Header {
	return r.header
}

func (r *testResponseRecorder) Write(b []byte) (int, error) {
	r.body = append(r.body, b...)
	return len(b), nil
}

func (r *testResponseRecorder) WriteHeader(status int) {
	r.status = status
}

func (r *testResponseRecorder) Result() *http.Response {
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     r.header,
		Body:       ioNopCloser(r.body),
	}
}

type nopCloser struct {
	*strings.Reader
}

func (nopCloser) Close() error { return nil }

func ioNopCloser(b []byte) nopCloser {
	return nopCloser{Reader: strings.NewReader(string(b))}
}
