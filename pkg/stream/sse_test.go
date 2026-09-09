package stream

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"a2a-proxy/pkg/model"
)

func TestSSEHeadersAndInitialFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	writer, err := NewSSEResponseWriter(rec)
	if err != nil {
		t.Fatalf("failed to create SSE writer: %v", err)
	}
	if writer == nil {
		t.Fatalf("expected non-nil SSEResponseWriter")
	}

	if rec.Code != http.StatusOK {
		t.Errorf("expected status code %d, got %d", http.StatusOK, rec.Code)
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("expected Cache-Control no-cache, got %s", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("Connection") != "keep-alive" {
		t.Errorf("expected Connection keep-alive, got %s", rec.Header().Get("Connection"))
	}
	if rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Errorf("expected X-Accel-Buffering no, got %s", rec.Header().Get("X-Accel-Buffering"))
	}
	if !rec.Flushed {
		t.Errorf("expected initial flush to be called")
	}
}

func TestSSEFormatting(t *testing.T) {
	rec := httptest.NewRecorder()
	writer, err := NewSSEResponseWriter(rec)
	if err != nil {
		t.Fatalf("failed to create SSE writer: %v", err)
	}

	if err := writer.Emit(model.EventTaskStarted, map[string]any{"task_id": "123"}); err != nil {
		t.Fatalf("unexpected error emitting task_started: %v", err)
	}
	if err := writer.Emit(model.EventTokenDelta, map[string]any{"delta": "world"}); err != nil {
		t.Fatalf("unexpected error emitting token_delta: %v", err)
	}
	if err := writer.Emit(model.EventStepProgress, map[string]any{"step": 1}); err != nil {
		t.Fatalf("unexpected error emitting step_progress: %v", err)
	}
	if err := writer.Emit(model.EventTaskCompleted, map[string]any{"status": "COMPLETED"}); err != nil {
		t.Fatalf("unexpected error emitting task_completed: %v", err)
	}
	if err := writer.Emit(model.EventTaskError, map[string]any{"error": "something failed"}); err != nil {
		t.Fatalf("unexpected error emitting task_error: %v", err)
	}

	body := rec.Body.String()

	expectedParts := []string{
		"event: task_started\ndata: {\"task_id\":\"123\"}\n\n",
		"event: token_delta\ndata: {\"delta\":\"world\"}\n\n",
		"event: step_progress\ndata: {\"step\":1}\n\n",
		"event: task_completed\ndata: {\"status\":\"COMPLETED\"}\n\n",
		"event: task_error\ndata: {\"error\":\"something failed\"}\n\n",
	}

	for _, part := range expectedParts {
		if !strings.Contains(body, part) {
			t.Errorf("expected output to contain %q, but got body:\n%s", part, body)
		}
	}
}

type nonFlusherWriter struct {
	header http.Header
}

func (n *nonFlusherWriter) Header() http.Header {
	if n.header == nil {
		n.header = make(http.Header)
	}
	return n.header
}

func (n *nonFlusherWriter) Write(b []byte) (int, error) {
	return len(b), nil
}

func (n *nonFlusherWriter) WriteHeader(statusCode int) {}

func TestNonFlusherError(t *testing.T) {
	w := &nonFlusherWriter{}
	writer, err := NewSSEResponseWriter(w)
	if err == nil {
		t.Fatalf("expected error for non-flusher ResponseWriter, got nil")
	}
	if writer != nil {
		t.Fatalf("expected nil writer on error, got %v", writer)
	}
	if !strings.Contains(err.Error(), "does not implement http.Flusher") {
		t.Errorf("expected error message to mention 'does not implement http.Flusher', got: %v", err)
	}
}

func TestSSEConcurrencySafety(t *testing.T) {
	rec := httptest.NewRecorder()
	writer, err := NewSSEResponseWriter(rec)
	if err != nil {
		t.Fatalf("failed to create SSE writer: %v", err)
	}

	const goroutines = 20
	const eventsPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < eventsPerGoroutine; j++ {
				err := writer.Emit(model.EventTokenDelta, map[string]any{
					"worker": workerID,
					"seq":    j,
				})
				if err != nil {
					t.Errorf("worker %d seq %d failed to emit: %v", workerID, j, err)
				}
			}
		}(i)
	}

	wg.Wait()

	body := rec.Body.String()
	events := strings.Split(strings.TrimSpace(body), "\n\n")
	if len(events) != goroutines*eventsPerGoroutine {
		t.Fatalf("expected %d events, got %d", goroutines*eventsPerGoroutine, len(events))
	}

	for idx, event := range events {
		lines := strings.Split(event, "\n")
		if len(lines) != 2 {
			t.Fatalf("event #%d malformed (expected 2 lines): %q", idx, event)
		}
		if lines[0] != "event: token_delta" {
			t.Errorf("event #%d has bad event line: %s", idx, lines[0])
		}
		if !strings.HasPrefix(lines[1], "data: ") {
			t.Errorf("event #%d has bad data line: %s", idx, lines[1])
		}
		dataJSON := strings.TrimPrefix(lines[1], "data: ")
		var payload map[string]any
		if err := json.Unmarshal([]byte(dataJSON), &payload); err != nil {
			t.Errorf("event #%d invalid JSON %q: %v", idx, dataJSON, err)
		}
	}
}

func TestSSEMarshalError(t *testing.T) {
	rec := httptest.NewRecorder()
	writer, err := NewSSEResponseWriter(rec)
	if err != nil {
		t.Fatalf("failed to create SSE writer: %v", err)
	}

	// Channels cannot be marshaled to JSON
	err = writer.Emit(model.EventTaskStarted, make(chan int))
	if err == nil {
		t.Errorf("expected error when emitting unmarshalable data, got nil")
	}
}
