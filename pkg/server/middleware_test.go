package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/metrics"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/stream"
)

func TestAuthMiddleware(t *testing.T) {
	cfg := &config.SecurityConfig{
		Enabled: true,
		APIKeys: []config.APIKeyConfig{
			{
				Key:           "key-writer",
				ClientID:      "writer-client",
				AllowedAgents: []string{"writer"},
			},
			{
				Key:           "key-admin",
				ClientID:      "admin-client",
				AllowedAgents: []string{"*"},
			},
		},
	}

	var capturedCreds ClientCredentials
	var capturedOk bool

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedCreds, capturedOk = GetClientCredentials(r.Context())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	handler := AuthMiddleware(cfg)(nextHandler)

	// Case 1: Missing auth header -> 401
	req1 := httptest.NewRequest("GET", "/a2a/v1/tasks?agent_id=writer", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec1.Code)
	}
	var errEnv1 model.A2AErrorEnvelope
	if err := json.Unmarshal(rec1.Body.Bytes(), &errEnv1); err != nil {
		t.Errorf("expected JSON error envelope, got: %v", err)
	}
	if errEnv1.Error.Code != "UNAUTHORIZED" {
		t.Errorf("expected code UNAUTHORIZED, got %s", errEnv1.Error.Code)
	}

	// Case 2: Valid auth key (Bearer) -> 200
	req2 := httptest.NewRequest("GET", "/a2a/v1/tasks?agent_id=writer", nil)
	req2.Header.Set("Authorization", "Bearer key-writer")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec2.Code)
	}
	if !capturedOk || capturedCreds.ClientID != "writer-client" {
		t.Errorf("expected writer-client credentials, got %+v (ok=%v)", capturedCreds, capturedOk)
	}
	if cid := GetClientID(req2.Context()); cid != "" {
		// inside nextHandler capturedOk verifies it, check outside returns expected helper behavior
	}

	// Case 3: Valid key but forbidden agent -> 403
	req3 := httptest.NewRequest("GET", "/a2a/v1/tasks?agent_id=admin-agent", nil)
	req3.Header.Set("Authorization", "Bearer key-writer")
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec3.Code)
	}
	var errEnv3 model.A2AErrorEnvelope
	if err := json.Unmarshal(rec3.Body.Bytes(), &errEnv3); err != nil {
		t.Errorf("expected JSON error envelope, got: %v", err)
	}
	if errEnv3.Error.Code != "FORBIDDEN" {
		t.Errorf("expected code FORBIDDEN, got %s", errEnv3.Error.Code)
	}

	// Case 4: Invalid auth key -> 401
	req4 := httptest.NewRequest("GET", "/a2a/v1/tasks?agent_id=writer", nil)
	req4.Header.Set("Authorization", "Bearer bad-key")
	rec4 := httptest.NewRecorder()
	handler.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec4.Code)
	}

	// Case 5: X-API-Key header -> 200
	req5 := httptest.NewRequest("GET", "/a2a/v1/tasks?agent_id=writer", nil)
	req5.Header.Set("X-API-Key", "key-writer")
	rec5 := httptest.NewRecorder()
	handler.ServeHTTP(rec5, req5)
	if rec5.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec5.Code)
	}

	// Case 6: Wildcard agent permission -> 200
	req6 := httptest.NewRequest("GET", "/a2a/v1/tasks?agent_id=any-agent", nil)
	req6.Header.Set("Authorization", "Bearer key-admin")
	rec6 := httptest.NewRecorder()
	handler.ServeHTTP(rec6, req6)
	if rec6.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec6.Code)
	}
	if !capturedOk || capturedCreds.ClientID != "admin-client" {
		t.Errorf("expected admin-client credentials, got %+v (ok=%v)", capturedCreds, capturedOk)
	}

	// Case 7: Path-based agent_id: /api/v1/invoke/writer -> 200
	req7 := httptest.NewRequest("POST", "/api/v1/invoke/writer", nil)
	req7.Header.Set("Authorization", "Bearer key-writer")
	rec7 := httptest.NewRecorder()
	handler.ServeHTTP(rec7, req7)
	if rec7.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec7.Code)
	}

	// Case 8: Path-based agent_id forbidden: /api/v1/invoke/admin-agent -> 403
	req8 := httptest.NewRequest("POST", "/api/v1/invoke/admin-agent", nil)
	req8.Header.Set("Authorization", "Bearer key-writer")
	rec8 := httptest.NewRecorder()
	handler.ServeHTTP(rec8, req8)
	if rec8.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec8.Code)
	}

	// Case 9: Path-based agent_id with stream: /api/v1/stream/writer -> 200
	req9 := httptest.NewRequest("POST", "/api/v1/stream/writer", nil)
	req9.Header.Set("Authorization", "Bearer key-writer")
	rec9 := httptest.NewRecorder()
	handler.ServeHTTP(rec9, req9)
	if rec9.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec9.Code)
	}

	// Case 10: Path-based agent_id: /a2a/v1/agents/writer -> 200
	req10 := httptest.NewRequest("GET", "/a2a/v1/agents/writer", nil)
	req10.Header.Set("Authorization", "Bearer key-writer")
	rec10 := httptest.NewRecorder()
	handler.ServeHTTP(rec10, req10)
	if rec10.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec10.Code)
	}

	// Case 11: Path-based agent_id forbidden: /a2a/v1/agents/admin-agent -> 403
	req11 := httptest.NewRequest("GET", "/a2a/v1/agents/admin-agent", nil)
	req11.Header.Set("Authorization", "Bearer key-writer")
	rec11 := httptest.NewRecorder()
	handler.ServeHTTP(rec11, req11)
	if rec11.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec11.Code)
	}

	// Case 12: Disabled security -> 200 even without auth header
	disabledCfg := &config.SecurityConfig{
		Enabled: false,
	}
	disabledHandler := AuthMiddleware(disabledCfg)(nextHandler)
	req12 := httptest.NewRequest("GET", "/a2a/v1/tasks?agent_id=writer", nil)
	rec12 := httptest.NewRecorder()
	disabledHandler.ServeHTTP(rec12, req12)
	if rec12.Code != http.StatusOK {
		t.Errorf("expected 200 with disabled auth, got %d", rec12.Code)
	}

	// Case 13: Nil config -> 200 bypass
	nilHandler := AuthMiddleware(nil)(nextHandler)
	req13 := httptest.NewRequest("GET", "/a2a/v1/tasks", nil)
	rec13 := httptest.NewRecorder()
	nilHandler.ServeHTTP(rec13, req13)
	if rec13.Code != http.StatusOK {
		t.Errorf("expected 200 with nil config, got %d", rec13.Code)
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	var capturedID string
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = GetRequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	handler := RequestIDMiddleware(nextHandler)

	// Case 1: Generates new ID when none provided
	req1 := httptest.NewRequest("GET", "/a2a/v1/agents", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec1.Code)
	}
	resHeaderID1 := rec1.Header().Get("X-Request-ID")
	if resHeaderID1 == "" {
		t.Errorf("expected X-Request-ID header in response")
	}
	if capturedID != resHeaderID1 {
		t.Errorf("expected context ID (%s) to match response header ID (%s)", capturedID, resHeaderID1)
	}
	if len(resHeaderID1) != 36 {
		t.Errorf("expected UUID v4 length 36, got %d (%s)", len(resHeaderID1), resHeaderID1)
	}

	// Case 2: Preserves incoming X-Request-ID
	req2 := httptest.NewRequest("GET", "/a2a/v1/agents", nil)
	req2.Header.Set("X-Request-ID", "custom-trace-id-12345")
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	resHeaderID2 := rec2.Header().Get("X-Request-ID")
	if resHeaderID2 != "custom-trace-id-12345" {
		t.Errorf("expected 'custom-trace-id-12345', got '%s'", resHeaderID2)
	}
	if capturedID != "custom-trace-id-12345" {
		t.Errorf("expected context ID 'custom-trace-id-12345', got '%s'", capturedID)
	}
}

func TestRecoveryMiddleware(t *testing.T) {
	// Case 1: Panic in handler is recovered and returns 500 JSON
	panickingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("database connection lost")
	})

	handler := RecoveryMiddleware(panickingHandler)
	req := httptest.NewRequest("GET", "/a2a/v1/panic", nil)
	rec := httptest.NewRecorder()

	// Ensure the test itself does not crash
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
	var errEnv model.A2AErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &errEnv); err != nil {
		t.Fatalf("failed to decode json error: %v", err)
	}
	if errEnv.Error.Status != http.StatusInternalServerError {
		t.Errorf("expected status 500 in body, got %d", errEnv.Error.Status)
	}

	// Case 2: Normal handler passes through untouched
	normalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("ok"))
	})
	normalRec := httptest.NewRecorder()
	RecoveryMiddleware(normalHandler).ServeHTTP(normalRec, req)
	if normalRec.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", normalRec.Code)
	}
}

func TestLoggingMiddleware_PreservesFlusher(t *testing.T) {
	var loggedBuffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&loggedBuffer, nil))

	var flusherTested bool
	var streamCreated bool

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify response writer implements http.Flusher
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("expected wrapped response writer to implement http.Flusher")
		}
		flusherTested = true
		flusher.Flush()

		// Verify stream.NewSSEResponseWriter succeeds with this wrapped writer
		sseWriter, err := stream.NewSSEResponseWriter(w)
		if err != nil {
			t.Fatalf("stream.NewSSEResponseWriter failed: %v", err)
		}
		streamCreated = true
		_ = sseWriter.Emit(model.EventTokenDelta, map[string]string{"chunk": "hello"})
	})

	middleware := LoggingMiddlewareWithLogger(logger)(nextHandler)

	req := httptest.NewRequest("POST", "/a2a/v1/tasks/stream", nil)
	ctx := context.WithValue(req.Context(), RequestIDKey, "test-req-999")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	middleware.ServeHTTP(rec, req)

	if !flusherTested {
		t.Errorf("flusher was not tested")
	}
	if !streamCreated {
		t.Errorf("sse stream writer was not created")
	}

	logOutput := loggedBuffer.String()
	if !strings.Contains(logOutput, "test-req-999") {
		t.Errorf("expected log output to contain trace_id 'test-req-999', got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "/a2a/v1/tasks/stream") {
		t.Errorf("expected log output to contain path, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "duration_ms") {
		t.Errorf("expected log output to contain duration_ms, got: %s", logOutput)
	}
}

func TestResponseWriterWrapper_DefaultStatusOK(t *testing.T) {
	var capturedCode int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write without WriteHeader
		_, _ = w.Write([]byte("hello world"))
	})

	wrappedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &responseWriterWrapper{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}
		handler.ServeHTTP(rw, r)
		capturedCode = rw.statusCode
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	wrappedHandler.ServeHTTP(rec, req)

	if capturedCode != http.StatusOK {
		t.Errorf("expected capturedCode 200, got %d", capturedCode)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected rec.Code 200, got %d", rec.Code)
	}
}

type fakeHijackerWriter struct {
	*httptest.ResponseRecorder
}

func (f *fakeHijackerWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("fake hijacked")
}

func TestResponseWriterWrapper_Hijacker(t *testing.T) {
	// Not supported case
	plainWrapper := &responseWriterWrapper{
		ResponseWriter: httptest.NewRecorder(),
	}
	_, _, err1 := plainWrapper.Hijack()
	if !errors.Is(err1, http.ErrNotSupported) {
		t.Errorf("expected ErrNotSupported, got %v", err1)
	}

	// Supported case
	hijackWrapper := &responseWriterWrapper{
		ResponseWriter: &fakeHijackerWriter{ResponseRecorder: httptest.NewRecorder()},
	}
	_, _, err2 := hijackWrapper.Hijack()
	if err2 == nil || err2.Error() != "fake hijacked" {
		t.Errorf("expected 'fake hijacked', got %v", err2)
	}
}

func TestMetricsMiddleware(t *testing.T) {
	reg := metrics.NewRegistry()
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mw := MetricsMiddleware(reg)(nextHandler)
	req := httptest.NewRequest("GET", "/a2a/v1/agents", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	output := reg.Gather()
	if !strings.Contains(output, `a2a_requests_total{method="GET",path="/a2a/v1/agents",status="200"} 1`) {
		t.Errorf("expected recorded request metric, got:\n%s", output)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	reg := metrics.NewRegistry()
	reg.IncRequests("GET", "/a2a/v1/tasks", 200)
	reg.ObserveDuration("GET", "/a2a/v1/tasks", 0.045)
	reg.IncActiveStreams()
	reg.IncRetries("agent-1")
	reg.IncFallbackTriggered("agent-1", "agent-2")

	handler := reg.Handler()

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/plain") {
		t.Errorf("expected text/plain content type, got: %s", contentType)
	}

	body := rec.Body.String()
	expectedMetrics := []string{
		"a2a_requests_total",
		"a2a_request_duration_seconds",
		"a2a_active_streams",
		"a2a_retries_total",
		"a2a_fallback_triggered_total",
	}

	for _, m := range expectedMetrics {
		if !strings.Contains(body, m) {
			t.Errorf("metrics output missing expected metric %q. Output:\n%s", m, body)
		}
	}

	// Verify active stream decrement
	reg.DecActiveStreams()
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req)
	body2 := rec2.Body.String()
	if !strings.Contains(body2, "a2a_active_streams 0") {
		t.Errorf("expected a2a_active_streams 0 after dec, got:\n%s", body2)
	}
}

func TestClientCredentials_Allows(t *testing.T) {
	creds := ClientCredentials{
		ClientID:      "test-client",
		AllowedAgents: []string{"agent-1", "agent-2"},
	}

	if !creds.Allows("agent-1") {
		t.Errorf("expected agent-1 to be allowed")
	}
	if !creds.Allows("agent-2") {
		t.Errorf("expected agent-2 to be allowed")
	}
	if creds.Allows("agent-3") {
		t.Errorf("expected agent-3 to be disallowed")
	}

	wildcardCreds := ClientCredentials{
		ClientID:      "wildcard-client",
		AllowedAgents: []string{"*"},
	}
	if !wildcardCreds.Allows("any-agent") {
		t.Errorf("expected any-agent to be allowed with wildcard")
	}

	if !IsAgentAllowed([]string{"bot-a"}, "bot-a") {
		t.Errorf("expected IsAgentAllowed to return true")
	}
	if IsAgentAllowed([]string{"bot-a"}, "bot-b") {
		t.Errorf("expected IsAgentAllowed to return false")
	}
}

func TestRecoveryMiddleware_ErrAbortHandler(t *testing.T) {
	abortingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	})

	handler := RecoveryMiddleware(abortingHandler)
	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic with ErrAbortHandler, got nil")
		}
		if r != http.ErrAbortHandler {
			t.Fatalf("expected ErrAbortHandler, got %v", r)
		}
	}()

	handler.ServeHTTP(rec, req)
}

func TestMetricsMiddleware_PatternNormalization(t *testing.T) {
	reg := metrics.NewRegistry()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /a2a/v1/agents/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mw := MetricsMiddleware(reg)(mux)
	req := httptest.NewRequest("GET", "/a2a/v1/agents/agent-xyz", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	output := reg.Gather()
	if !strings.Contains(output, `path="/a2a/v1/agents/{id}"`) {
		t.Errorf("expected metric path to be normalized pattern /a2a/v1/agents/{id}, got:\n%s", output)
	}
	if strings.Contains(output, `agent-xyz`) {
		t.Errorf("expected metric not to contain dynamic ID agent-xyz, got:\n%s", output)
	}
}

func TestAuthMiddleware_PublicEndpointsBypass(t *testing.T) {
	cfg := &config.SecurityConfig{
		Enabled: true,
		APIKeys: []config.APIKeyConfig{
			{Key: "secret", ClientID: "c1", AllowedAgents: []string{"*"}},
		},
	}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := AuthMiddleware(cfg)(next)

	for _, p := range []string{"/healthz", "/readyz", "/metrics"} {
		called = false
		req := httptest.NewRequest("GET", p, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("path %s expected 200 bypass, got %d", p, rec.Code)
		}
		if !called {
			t.Errorf("path %s handler was not called", p)
		}
	}
}

func TestResponseWriterWrapper_Unwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapper := &responseWriterWrapper{
		ResponseWriter: rec,
		statusCode:     http.StatusOK,
	}

	if unwrapped := wrapper.Unwrap(); unwrapped != rec {
		t.Fatalf("expected unwrapped writer to be original rec, got %v", unwrapped)
	}

	// Verify http.NewResponseController can inspect unwrapped ResponseWriter
	rc := http.NewResponseController(wrapper)
	if rc == nil {
		t.Fatal("expected non-nil ResponseController")
	}
}

