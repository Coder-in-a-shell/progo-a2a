package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/config"
)

func TestConsoleRoutes(t *testing.T) {
	cfg := &config.Config{Security: config.SecurityConfig{Enabled: true, APIKeys: []config.APIKeyConfig{{Key: "test-key", ClientID: "operator", AllowedAgents: []string{"*"}}}}}
	router, _, _ := setupTestRouter(cfg)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantType   string
		wantBody   string
		wantCSP    bool
	}{
		{name: "console redirects to trailing slash", path: "/console", wantStatus: http.StatusMovedPermanently},
		{name: "index is public", path: "/console/", wantStatus: http.StatusOK, wantType: "text/html", wantBody: "ProGoA2A Control", wantCSP: true},
		{name: "stylesheet is public", path: "/console/app.css", wantStatus: http.StatusOK, wantType: "text/css", wantBody: "--route:", wantCSP: true},
		{name: "javascript is public", path: "/console/app.js", wantStatus: http.StatusOK, wantType: "text/javascript", wantBody: "sessionStorage", wantCSP: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("GET %s status = %d, want %d; body=%s", tc.path, rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantType != "" && !strings.Contains(rec.Header().Get("Content-Type"), tc.wantType) {
				t.Errorf("GET %s content type = %q, want %q", tc.path, rec.Header().Get("Content-Type"), tc.wantType)
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("GET %s body does not contain %q", tc.path, tc.wantBody)
			}
			if tc.wantCSP && !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
				t.Errorf("GET %s missing restrictive CSP", tc.path)
			}
		})
	}

	protected := httptest.NewRecorder()
	router.ServeHTTP(protected, httptest.NewRequest(http.MethodGet, "/a2a/v1/agents", nil))
	if protected.Code != http.StatusUnauthorized {
		t.Fatalf("protected API status = %d, want 401", protected.Code)
	}

	bootstrap := httptest.NewRecorder()
	router.ServeHTTP(bootstrap, httptest.NewRequest(http.MethodGet, "/console/api/bootstrap", nil))
	if bootstrap.Code != http.StatusOK || !strings.Contains(bootstrap.Body.String(), `"auth_required":true`) {
		t.Fatalf("bootstrap response = %d %s, want auth_required=true", bootstrap.Code, bootstrap.Body.String())
	}
	if strings.Contains(bootstrap.Body.String(), "test-key") || strings.Contains(bootstrap.Body.String(), "operator") {
		t.Fatalf("bootstrap leaked security configuration: %s", bootstrap.Body.String())
	}
}
