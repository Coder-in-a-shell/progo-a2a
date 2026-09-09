package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path"
	"runtime/debug"
	"strings"
	"time"

	"a2a-proxy/pkg/config"
	"a2a-proxy/pkg/metrics"
	"a2a-proxy/pkg/model"
)

type contextKey string

const (
	// RequestIDKey is the context key for the correlation request/trace ID.
	RequestIDKey contextKey = "request_id"
	// ClientCredentialsKey is the context key for authenticated client credentials.
	ClientCredentialsKey contextKey = "client_credentials"
)

// ClientCredentials holds authenticated API client identity and authorizations.
type ClientCredentials struct {
	ClientID      string
	AllowedAgents []string
}

// GetRequestID extracts the request/trace ID from context, or returns an empty string.
func GetRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(RequestIDKey).(string); ok {
		return id
	}
	return ""
}

// GetClientCredentials extracts the client credentials from context if present.
func GetClientCredentials(ctx context.Context) (ClientCredentials, bool) {
	if ctx == nil {
		return ClientCredentials{}, false
	}
	creds, ok := ctx.Value(ClientCredentialsKey).(ClientCredentials)
	return creds, ok
}

// GetClientID returns the client ID from context or empty string.
func GetClientID(ctx context.Context) string {
	if creds, ok := GetClientCredentials(ctx); ok {
		return creds.ClientID
	}
	return ""
}

// generateRequestID produces an RFC 4122 compliant UUID v4 string.
func generateRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// RequestIDMiddleware reads incoming X-Request-ID or generates a new unique ID,
// attaching it to both request context and response header.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = generateRequestID()
		}

		ctx := context.WithValue(r.Context(), RequestIDKey, reqID)
		w.Header().Set("X-Request-ID", reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RecoveryMiddleware recovers from panics, logs structured slog.Error, and returns HTTP 500 A2AError.
func RecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				traceID := GetRequestID(r.Context())
				stack := string(debug.Stack())
				slog.Error("panic recovered",
					"error", fmt.Sprintf("%v", rec),
					"trace_id", traceID,
					"stack", stack,
				)

				writeA2AError(w, "INTERNAL_ERROR", "Internal server error", "", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// responseWriterWrapper wraps http.ResponseWriter to capture status code and preserve http.Flusher.
type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

var _ http.ResponseWriter = (*responseWriterWrapper)(nil)
var _ http.Flusher = (*responseWriterWrapper)(nil)

func (rw *responseWriterWrapper) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

func (rw *responseWriterWrapper) Write(b []byte) (int, error) {
	if !rw.written {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(b)
}

// Flush ensures wrapped response writer preserves http.Flusher for SSE streaming.
func (rw *responseWriterWrapper) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack preserves http.Hijacker if supported by underlying response writer.
func (rw *responseWriterWrapper) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// LoggingMiddleware logs request details using slog.Default().
func LoggingMiddleware(next http.Handler) http.Handler {
	return LoggingMiddlewareWithLogger(slog.Default())(next)
}

// LoggingMiddlewareWithLogger returns a logging middleware using the provided slog.Logger.
func LoggingMiddlewareWithLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wrapped := &responseWriterWrapper{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
			}

			next.ServeHTTP(wrapped, r)

			duration := time.Since(start)
			traceID := GetRequestID(r.Context())
			if traceID == "" {
				traceID = r.Header.Get("X-Request-ID")
			}

			logger.Info("http request",
				"trace_id", traceID,
				"method", r.Method,
				"path", r.URL.Path,
				"status", wrapped.statusCode,
				"duration_ms", duration.Milliseconds(),
			)
		})
	}
}

// MetricsMiddleware tracks requests and request durations in the specified metrics registry.
func MetricsMiddleware(reg *metrics.Registry) func(http.Handler) http.Handler {
	if reg == nil {
		reg = metrics.DefaultRegistry
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wrapped, ok := w.(*responseWriterWrapper)
			if !ok {
				wrapped = &responseWriterWrapper{
					ResponseWriter: w,
					statusCode:     http.StatusOK,
				}
				w = wrapped
			}

			next.ServeHTTP(w, r)

			duration := time.Since(start)
			reg.IncRequests(r.Method, r.URL.Path, wrapped.statusCode)
			reg.ObserveDuration(r.Method, r.URL.Path, duration.Seconds())
		})
	}
}

// AuthMiddleware enforces API key authentication and agent-level RBAC.
func AuthMiddleware(cfg *config.SecurityConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If security is not enabled or config is nil, bypass auth check
			if cfg == nil || !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			// Read key from Authorization: Bearer <key> or X-API-Key: <key>
			apiKey := extractAPIKey(r)
			if apiKey == "" {
				writeA2AError(w, "UNAUTHORIZED", "Missing or invalid API key", "", http.StatusUnauthorized)
				return
			}

			// Match key against configured API keys
			var matchedKey *config.APIKeyConfig
			for i := range cfg.APIKeys {
				if cfg.APIKeys[i].Key == apiKey {
					matchedKey = &cfg.APIKeys[i]
					break
				}
			}

			if matchedKey == nil {
				writeA2AError(w, "UNAUTHORIZED", "Invalid API key", "", http.StatusUnauthorized)
				return
			}

			// Attach credentials to context
			creds := ClientCredentials{
				ClientID:      matchedKey.ClientID,
				AllowedAgents: matchedKey.AllowedAgents,
			}
			ctx := context.WithValue(r.Context(), ClientCredentialsKey, creds)

			// Check agent RBAC if agent_id is present in query or path
			agentID := extractAgentID(r)
			if agentID != "" {
				if !isAgentAllowed(agentID, matchedKey.AllowedAgents) {
					writeA2AError(w, "FORBIDDEN", fmt.Sprintf("Access to agent '%s' is forbidden", agentID), agentID, http.StatusForbidden)
					return
				}
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractAPIKey(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			key := strings.TrimSpace(authHeader[7:])
			if key != "" {
				return key
			}
		}
	}

	apiKey := strings.TrimSpace(r.Header.Get("X-API-Key"))
	return apiKey
}

func extractAgentID(r *http.Request) string {
	// Check query string ?agent_id=...
	if id := r.URL.Query().Get("agent_id"); id != "" {
		return id
	}

	// Check Go 1.22+ PathValue if matched by ServeMux
	if id := r.PathValue("agent_id"); id != "" {
		return id
	}

	// Parse path segments
	cleanPath := path.Clean(r.URL.Path)
	parts := strings.Split(strings.Trim(cleanPath, "/"), "/")

	// Pattern: /api/v1/invoke/{agent_id} or /api/v1/stream/{agent_id}
	if len(parts) >= 4 && parts[0] == "api" && parts[1] == "v1" && (parts[2] == "invoke" || parts[2] == "stream") {
		return parts[3]
	}

	// Pattern: /a2a/v1/agents/{id}
	if len(parts) >= 4 && parts[0] == "a2a" && parts[1] == "v1" && parts[2] == "agents" {
		return parts[3]
	}

	return ""
}

func isAgentAllowed(agentID string, allowedAgents []string) bool {
	for _, a := range allowedAgents {
		if a == "*" || a == agentID {
			return true
		}
	}
	return false
}

func writeA2AError(w http.ResponseWriter, code, message, agentID string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	errEnv := model.NewA2AError(code, message, agentID, status)
	_ = json.NewEncoder(w).Encode(errEnv)
}
