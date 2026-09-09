package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/adapter"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
	"github.com/Coder-in-a-shell/progo-a2a/pkg/dispatcher"
)

func TestOversizedRequestBodiesReturn413(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{{
			ID:       "agent-a",
			Type:     "openai",
			Endpoint: "http://example.invalid",
		}},
	}
	disp := dispatcher.New(cfg, adapter.NewRegistry())
	handler := SetupRouter(cfg, disp)
	body := `{"input":"` + strings.Repeat("a", maxRequestBodyBytes) + `"}`

	for _, path := range []string{"/a2a/v1/tasks", "/a2a/v1/tasks/stream", "/api/v1/invoke/agent-a", "/api/v1/stream/agent-a"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
			}
		})
	}
}
