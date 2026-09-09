package metrics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestMetricsRegistry_Basic(t *testing.T) {
	reg := NewRegistry()

	// Initial zero values check
	output := reg.Gather()
	if !strings.Contains(output, "a2a_active_streams 0") {
		t.Errorf("expected a2a_active_streams 0, got:\n%s", output)
	}
	if !strings.Contains(output, "a2a_requests_total 0") {
		t.Errorf("expected a2a_requests_total 0, got:\n%s", output)
	}
	if !strings.Contains(output, "a2a_retries_total 0") {
		t.Errorf("expected a2a_retries_total 0, got:\n%s", output)
	}
	if !strings.Contains(output, "a2a_fallback_triggered_total 0") {
		t.Errorf("expected a2a_fallback_triggered_total 0, got:\n%s", output)
	}

	// Increment requests
	reg.IncRequests("GET", "/a2a/v1/agents", 200)
	reg.IncRequests("POST", "/a2a/v1/tasks", 201)
	reg.IncRequests("POST", "/a2a/v1/tasks", 201)

	// Increment active streams
	reg.IncActiveStreams()
	reg.IncActiveStreams()
	if reg.GetActiveStreams() != 2 {
		t.Errorf("expected 2 active streams, got %d", reg.GetActiveStreams())
	}
	reg.DecActiveStreams()
	if reg.GetActiveStreams() != 1 {
		t.Errorf("expected 1 active stream, got %d", reg.GetActiveStreams())
	}

	// Durations
	reg.ObserveDuration("GET", "/a2a/v1/agents", 0.05)
	reg.ObserveDuration("POST", "/a2a/v1/tasks", 0.15)

	// Retries
	reg.IncRetries("agent-bot-1")
	reg.IncRetries("agent-bot-1")
	reg.IncRetries("agent-bot-2")
	if reg.GetRetries() != 3 {
		t.Errorf("expected 3 total retries, got %d", reg.GetRetries())
	}

	// Fallbacks
	reg.IncFallbackTriggered("agent-bot-1", "agent-bot-2")
	if reg.GetFallbackTriggered() != 1 {
		t.Errorf("expected 1 total fallback, got %d", reg.GetFallbackTriggered())
	}

	// Check output
	output = reg.Gather()
	if !strings.Contains(output, `a2a_requests_total{method="GET",path="/a2a/v1/agents",status="200"} 1`) {
		t.Errorf("missing GET /a2a/v1/agents request total in:\n%s", output)
	}
	if !strings.Contains(output, `a2a_requests_total{method="POST",path="/a2a/v1/tasks",status="201"} 2`) {
		t.Errorf("missing POST /a2a/v1/tasks request total in:\n%s", output)
	}
	if !strings.Contains(output, "a2a_active_streams 1") {
		t.Errorf("expected a2a_active_streams 1 in:\n%s", output)
	}
	if !strings.Contains(output, `a2a_retries_total{agent_id="agent-bot-1"} 2`) {
		t.Errorf("missing agent-bot-1 retries in:\n%s", output)
	}
	if !strings.Contains(output, `a2a_fallback_triggered_total{fallback="agent-bot-2",primary="agent-bot-1"} 1`) {
		t.Errorf("missing fallback metric in:\n%s", output)
	}

	// Test Reset
	reg.Reset()
	resetOutput := reg.Gather()
	if !strings.Contains(resetOutput, "a2a_active_streams 0") {
		t.Errorf("expected reset a2a_active_streams 0, got:\n%s", resetOutput)
	}
	if !strings.Contains(resetOutput, "a2a_requests_total 0") {
		t.Errorf("expected reset a2a_requests_total 0, got:\n%s", resetOutput)
	}
}

func TestMetricsRegistry_Concurrency(t *testing.T) {
	reg := NewRegistry()
	var wg sync.WaitGroup
	workers := 50
	iterations := 100

	wg.Add(workers * 4)

	for i := 0; i < workers; i++ {
		// Worker 1: IncRequests & ObserveDuration
		go func(workerID int) {
			defer wg.Done()
			path := fmt.Sprintf("/api/v1/invoke/agent-%d", workerID%5)
			for j := 0; j < iterations; j++ {
				reg.IncRequests("POST", path, 200)
				reg.ObserveDuration("POST", path, 0.01)
			}
		}(i)

		// Worker 2: ActiveStreams Inc/Dec
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				reg.IncActiveStreams()
				reg.DecActiveStreams()
			}
		}()

		// Worker 3: Retries & Fallbacks
		go func(workerID int) {
			defer wg.Done()
			agent := fmt.Sprintf("agent-%d", workerID%3)
			for j := 0; j < iterations; j++ {
				reg.IncRetries(agent)
				reg.IncFallbackTriggered(agent, "fallback-agent")
			}
		}(i)

		// Worker 4: Concurrent Gather/Handler reads
		go func() {
			defer wg.Done()
			for j := 0; j < iterations/5; j++ {
				_ = reg.Gather()
			}
		}()
	}

	wg.Wait()

	if reg.GetActiveStreams() != 0 {
		t.Errorf("expected active streams 0 after equal inc/dec, got %d", reg.GetActiveStreams())
	}
	expectedRequests := uint64(workers * iterations)
	if reg.totalRequests != expectedRequests {
		t.Errorf("expected %d total requests, got %d", expectedRequests, reg.totalRequests)
	}
}

func TestMetricsHandler(t *testing.T) {
	reg := NewRegistry()
	reg.IncRequestsTotal()
	reg.ObserveDurationSeconds(0.123)

	handler := reg.Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("unexpected content type: %s", ct)
	}
	if !strings.Contains(rec.Body.String(), "a2a_requests_total 1") {
		t.Errorf("expected global a2a_requests_total 1, got:\n%s", rec.Body.String())
	}
}
