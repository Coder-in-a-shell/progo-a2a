package config

import (
	"os"
	"strings"
	"testing"

	"github.com/Coder-in-a-shell/progo-a2a/pkg/model"
)

func TestLoadConfigWithEnvExpansion(t *testing.T) {
	t.Setenv("TEST_AGENT_KEY", "secret-agent-key-999")

	yamlContent := `
server:
  host: "127.0.0.1"
  port: 9090
security:
  enabled: true
  api_keys:
    - key: "master-key"
      client_id: "test-client"
      allowed_agents: ["*"]
agents:
  - id: "agent-1"
    name: "Test Agent"
    type: "langgraph"
    endpoint: "http://localhost:8080/runs"
    capabilities: ["chat"]
    timeout_seconds: 45
    auth:
      type: "bearer"
      token: "${TEST_AGENT_KEY}"
`
	tmpFile, err := os.CreateTemp("", "a2a-test-*.yaml")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write([]byte(yamlContent)); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	tmpFile.Close()

	cfg, err := Load(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Server.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Server.Port)
	}
	if len(cfg.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(cfg.Agents))
	}
	if cfg.Agents[0].Auth.Token != "secret-agent-key-999" {
		t.Errorf("expected expanded token, got %s", cfg.Agents[0].Auth.Token)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	yamlContent := `
agents:
  - id: "agent-default"
    type: "openai"
    endpoint: "http://localhost:8000"
`
	tmpFile, err := os.CreateTemp("", "a2a-test-defaults-*.yaml")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write([]byte(yamlContent)); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	tmpFile.Close()

	cfg, err := Load(tmpFile.Name())
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Server.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("expected default host 0.0.0.0, got %s", cfg.Server.Host)
	}
	if cfg.Server.ReadTimeoutSeconds != 30 {
		t.Errorf("expected default read timeout 30, got %d", cfg.Server.ReadTimeoutSeconds)
	}
	if cfg.Server.WriteTimeoutSeconds != 120 {
		t.Errorf("expected default write timeout 120, got %d", cfg.Server.WriteTimeoutSeconds)
	}
	if cfg.Server.IdleTimeoutSeconds != 60 {
		t.Errorf("expected default idle timeout 60, got %d", cfg.Server.IdleTimeoutSeconds)
	}
	if cfg.Agents[0].TimeoutSeconds != 60 {
		t.Errorf("expected default agent timeout 60, got %d", cfg.Agents[0].TimeoutSeconds)
	}
}

func TestValidationErrors(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Config
		errContains string
	}{
		{
			name: "invalid port 0",
			cfg: Config{
				Server: ServerConfig{Port: 0},
			},
			errContains: "server port must be between 1 and 65535",
		},
		{
			name: "invalid port 70000",
			cfg: Config{
				Server: ServerConfig{Port: 70000},
			},
			errContains: "server port must be between 1 and 65535",
		},
		{
			name: "empty agent id",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "", Type: "openai", Endpoint: "http://localhost:8000"},
				},
			},
			errContains: "agent id cannot be empty",
		},
		{
			name: "duplicate agent id",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "dup-id", Type: "openai", Endpoint: "http://localhost:8000"},
					{ID: "dup-id", Type: "langgraph", Endpoint: "http://localhost:8001"},
				},
			},
			errContains: "duplicate agent id: dup-id",
		},
		{
			name: "empty agent endpoint",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: ""},
				},
			},
			errContains: "agent agent-1 endpoint cannot be empty",
		},
		{
			name: "whitespace-only agent endpoint",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "   "},
				},
			},
			errContains: "agent agent-1 endpoint cannot be empty",
		},
		{
			name: "endpoint with whitespace",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000/ runs"},
				},
			},
			errContains: "agent agent-1 endpoint is malformed: contains whitespace",
		},
		{
			name: "endpoint malformed parse error",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://[::1:8000"},
				},
			},
			errContains: "agent agent-1 endpoint is malformed",
		},
		{
			name: "endpoint relative URL",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "/relative/path"},
				},
			},
			errContains: "agent agent-1 endpoint must be an absolute http or https URL",
		},
		{
			name: "endpoint unsupported scheme ftp",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "ftp://localhost:8000/runs"},
				},
			},
			errContains: "agent agent-1 endpoint must be an absolute http or https URL",
		},
		{
			name: "endpoint missing hostname",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://"},
				},
			},
			errContains: "agent agent-1 endpoint must have a non-empty hostname",
		},
		{
			name: "endpoint with userinfo",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://user:pass@localhost:8000/runs"},
				},
			},
			errContains: "agent agent-1 endpoint must not contain user info",
		},
		{
			name: "endpoint with fragment",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000/runs#section"},
				},
			},
			errContains: "agent agent-1 endpoint must not contain a fragment",
		},
		{
			name: "endpoint with empty fragment",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000/runs#"},
				},
			},
			errContains: "agent agent-1 endpoint must not contain a fragment",
		},
		{
			name: "empty agent type",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "", Endpoint: "http://localhost:8000"},
				},
			},
			errContains: "agent agent-1 type cannot be empty",
		},
		{
			name: "unsupported agent type",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "unknown", Endpoint: "http://localhost:8000"},
				},
			},
			errContains: "has unsupported type \"unknown\"",
		},
		{
			name: "negative retries",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Retries: -1},
				},
			},
			errContains: "retries must be between 0 and 10, got -1",
		},
		{
			name: "retries above 10",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Retries: 11},
				},
			},
			errContains: "retries must be between 0 and 10, got 11",
		},
		{
			name: "negative timeout",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", TimeoutSeconds: -1},
				},
			},
			errContains: "timeout_seconds cannot be negative, got -1",
		},
		{
			name: "auth none with token",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "none", Token: "secret"}},
				},
			},
			errContains: "auth type 'none' must not contain credentials",
		},
		{
			name: "auth none with header name",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "none", HeaderName: "X-Key"}},
				},
			},
			errContains: "auth type 'none' must not contain credentials",
		},
		{
			name: "auth none with header value",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "none", HeaderValue: "val"}},
				},
			},
			errContains: "auth type 'none' must not contain credentials",
		},
		{
			name: "omitted auth type with token",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Token: "secret"}},
				},
			},
			errContains: "auth type 'none' must not contain credentials",
		},
		{
			name: "auth bearer empty token",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "bearer", Token: ""}},
				},
			},
			errContains: "auth type 'bearer' requires a non-empty token",
		},
		{
			name: "auth bearer whitespace token",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "bearer", Token: "   "}},
				},
			},
			errContains: "auth type 'bearer' requires a non-empty token",
		},
		{
			name: "auth bearer with header name",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "bearer", Token: "tok", HeaderName: "X-Key"}},
				},
			},
			errContains: "auth type 'bearer' must not contain header credentials",
		},
		{
			name: "auth bearer with header value",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "bearer", Token: "tok", HeaderValue: "val"}},
				},
			},
			errContains: "auth type 'bearer' must not contain header credentials",
		},
		{
			name: "auth header with bearer token",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "header", HeaderName: "X-Key", HeaderValue: "val", Token: "tok"}},
				},
			},
			errContains: "auth type 'header' must not contain a bearer token",
		},
		{
			name: "auth header newline in name",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "header", HeaderName: "X-Header\nName", HeaderValue: "val"}},
				},
			},
			errContains: "auth type 'header' requires an RFC-valid header_name",
		},
		{
			name: "auth header control char in name",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "header", HeaderName: "X\x01Header", HeaderValue: "val"}},
				},
			},
			errContains: "auth type 'header' requires an RFC-valid header_name",
		},
		{
			name: "auth header separator in name",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "header", HeaderName: "X:Header", HeaderValue: "val"}},
				},
			},
			errContains: "auth type 'header' requires an RFC-valid header_name",
		},
		{
			name: "auth header space in name",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "header", HeaderName: "X Header", HeaderValue: "val"}},
				},
			},
			errContains: "auth type 'header' requires an RFC-valid header_name",
		},
		{
			name: "auth header non-ASCII in name",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "header", HeaderName: "X-Héader", HeaderValue: "val"}},
				},
			},
			errContains: "auth type 'header' requires an RFC-valid header_name",
		},
		{
			name: "auth header empty name",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "header", HeaderName: "", HeaderValue: "val"}},
				},
			},
			errContains: "auth type 'header' requires an RFC-valid header_name",
		},
		{
			name: "auth header empty value",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "header", HeaderName: "X-Key", HeaderValue: ""}},
				},
			},
			errContains: "auth type 'header' requires a non-empty header_value",
		},
		{
			name: "auth header whitespace value",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "header", HeaderName: "X-Key", HeaderValue: "   "}},
				},
			},
			errContains: "auth type 'header' requires a non-empty header_value",
		},
		{
			name: "unsupported auth type",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "basic", Token: "secret"}},
				},
			},
			errContains: "unsupported auth type \"basic\"",
		},
		{
			name: "custom agent without mapping",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-custom", Type: "custom", Endpoint: "http://localhost:8000", Mapping: nil},
				},
			},
			errContains: "agent agent-custom is of type 'custom' but mapping is missing",
		},
		{
			name: "custom agent unsupported method",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{
						ID:       "agent-custom",
						Type:     "custom",
						Endpoint: "http://localhost:8000",
						Mapping: &CustomMapping{
							Request:  RequestTemplate{Method: "HEAD", BodyTemplate: "{}"},
							Response: ResponseMapping{OutputPath: "$.result"},
						},
					},
				},
			},
			errContains: "custom mapping request.method must be one of GET, POST, PUT, PATCH, DELETE",
		},
		{
			name: "custom agent empty body template",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{
						ID:       "agent-custom",
						Type:     "custom",
						Endpoint: "http://localhost:8000",
						Mapping: &CustomMapping{
							Request:  RequestTemplate{Method: "POST", BodyTemplate: "   "},
							Response: ResponseMapping{OutputPath: "$.result"},
						},
					},
				},
			},
			errContains: "custom mapping request.body_template cannot be empty",
		},
		{
			name: "custom agent empty output path",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{
						ID:       "agent-custom",
						Type:     "custom",
						Endpoint: "http://localhost:8000",
						Mapping: &CustomMapping{
							Request:  RequestTemplate{Method: "POST", BodyTemplate: "{}"},
							Response: ResponseMapping{OutputPath: "   "},
						},
					},
				},
			},
			errContains: "custom mapping response.output_path cannot be empty",
		},
		{
			name: "fallback agent non-existent",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{
						ID:               "agent-1",
						Type:             "openai",
						Endpoint:         "http://localhost:8000",
						FallbackAgentIDs: []string{"non-existent-agent"},
					},
				},
			},
			errContains: "references non-existent fallback agent non-existent-agent",
		},
		{
			name: "security enabled without keys",
			cfg: Config{
				Server:   ServerConfig{Port: 8080},
				Security: SecurityConfig{Enabled: true},
			},
			errContains: "security is enabled but no API keys are configured",
		},
		{
			name: "empty security key",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Security: SecurityConfig{Enabled: true, APIKeys: []APIKeyConfig{
					{Key: "", ClientID: "client", AllowedAgents: []string{"*"}},
				}},
			},
			errContains: "security API key 0 is empty",
		},
		{
			name: "duplicate security API key",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Security: SecurityConfig{Enabled: true, APIKeys: []APIKeyConfig{
					{Key: "dup-key", ClientID: "client-1", AllowedAgents: []string{"*"}},
					{Key: "dup-key", ClientID: "client-2", AllowedAgents: []string{"*"}},
				}},
			},
			errContains: "duplicate security API key at index 1",
		},
		{
			name: "security key references unknown agent",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Security: SecurityConfig{Enabled: true, APIKeys: []APIKeyConfig{
					{Key: "secret", ClientID: "client", AllowedAgents: []string{"missing"}},
				}},
			},
			errContains: "references non-existent allowed agent missing",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(&tc.cfg)
			if err == nil {
				t.Fatalf("expected validation error containing %q, got nil", tc.errContains)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("expected error to contain %q, got %q", tc.errContains, err.Error())
			}
		})
	}
}

func TestAdversarialValidationRejections(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Config
		errContains string
	}{
		{
			name: "adversarial malformed URL",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "adv-1", Type: "openai", Endpoint: "http://[::1:9090"},
				},
			},
			errContains: "endpoint is malformed",
		},
		{
			name: "adversarial URL userinfo",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "adv-2", Type: "openai", Endpoint: "http://admin:pass@127.0.0.1:8000/v1"},
				},
			},
			errContains: "must not contain user info",
		},
		{
			name: "adversarial URL fragments",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "adv-3", Type: "openai", Endpoint: "https://example.com/v1#hash"},
				},
			},
			errContains: "must not contain a fragment",
		},
		{
			name: "adversarial unsupported scheme",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "adv-4", Type: "openai", Endpoint: "ftp://example.com/v1"},
				},
			},
			errContains: "must be an absolute http or https URL",
		},
		{
			name: "adversarial newline-containing header name",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{
						ID:       "adv-5",
						Type:     "openai",
						Endpoint: "https://example.com",
						Auth: AuthConfig{
							Type:        "header",
							HeaderName:  "X-Injected\nHeader",
							HeaderValue: "val",
						},
					},
				},
			},
			errContains: "requires an RFC-valid header_name",
		},
		{
			name: "adversarial 11 retries",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "adv-6", Type: "openai", Endpoint: "https://example.com", Retries: 11},
				},
			},
			errContains: "retries must be between 0 and 10",
		},
		{
			name: "adversarial negative retry count",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "adv-7", Type: "openai", Endpoint: "https://example.com", Retries: -5},
				},
			},
			errContains: "retries must be between 0 and 10",
		},
		{
			name: "adversarial negative timeout",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "adv-8", Type: "openai", Endpoint: "https://example.com", TimeoutSeconds: -10},
				},
			},
			errContains: "timeout_seconds cannot be negative",
		},
		{
			name: "adversarial unsupported adapter type",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "adv-9", Type: "unsupported-adapter", Endpoint: "https://example.com"},
				},
			},
			errContains: "has unsupported type \"unsupported-adapter\"",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(&tc.cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errContains)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("expected error to contain %q, got %q", tc.errContains, err.Error())
			}
		})
	}
}

func TestAdversarialEmptyExpandedEnvBearerToken(t *testing.T) {
	t.Setenv("UNSET_EXPANDED_TOKEN", "")

	yamlContent := `
server:
  port: 8080
agents:
  - id: "agent-adv-env"
    type: "openai"
    endpoint: "https://api.openai.com/v1"
    auth:
      type: "bearer"
      token: "${UNSET_EXPANDED_TOKEN}"
`
	tmpFile, err := os.CreateTemp("", "a2a-test-adv-env-*.yaml")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write([]byte(yamlContent)); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	tmpFile.Close()

	_, err = Load(tmpFile.Name())
	if err == nil {
		t.Fatal("expected Load to fail for empty expanded bearer token, got nil")
	}
	if !strings.Contains(err.Error(), "auth type 'bearer' requires a non-empty token") {
		t.Errorf("expected error to contain 'auth type 'bearer' requires a non-empty token', got %q", err.Error())
	}
}

func TestAcceptedBoundaryCases(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{
			name: "accepted boundary retries 0",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Retries: 0},
				},
			},
		},
		{
			name: "accepted boundary retries 10",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Retries: 10},
				},
			},
		},
		{
			name: "accepted boundary timeout 0",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", TimeoutSeconds: 0},
				},
			},
		},
		{
			name: "accepted adapter type langgraph",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "langgraph", Endpoint: "http://localhost:8000"},
				},
			},
		},
		{
			name: "accepted adapter type crewai",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "crewai", Endpoint: "http://localhost:8000"},
				},
			},
		},
		{
			name: "accepted adapter type autogen",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "autogen", Endpoint: "http://localhost:8000"},
				},
			},
		},
		{
			name: "accepted adapter type openai",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000"},
				},
			},
		},
		{
			name: "accepted adapter type custom",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{
						ID:       "agent-1",
						Type:     "custom",
						Endpoint: "http://localhost:8000",
						Mapping: &CustomMapping{
							Request:  RequestTemplate{Method: "POST", BodyTemplate: "{}"},
							Response: ResponseMapping{OutputPath: "$.data"},
						},
					},
				},
			},
		},
		{
			name: "accepted endpoint http",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://127.0.0.1:8080/runs"},
				},
			},
		},
		{
			name: "accepted endpoint https",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "https://api.openai.com/v1/chat"},
				},
			},
		},
		{
			name: "accepted auth omitted none",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000"},
				},
			},
		},
		{
			name: "accepted auth explicit none",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "none"}},
				},
			},
		},
		{
			name: "accepted auth bearer",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "bearer", Token: "valid-bearer-token"}},
				},
			},
		},
		{
			name: "accepted auth header",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000", Auth: AuthConfig{Type: "header", HeaderName: "X-Custom-Auth", HeaderValue: "custom-val"}},
				},
			},
		},
		{
			name: "accepted custom adapter case-insensitive methods",
			cfg: Config{
				Server: ServerConfig{Port: 8080},
				Agents: []AgentConfig{
					{
						ID:       "c-get",
						Type:     "custom",
						Endpoint: "http://localhost:8000",
						Mapping: &CustomMapping{
							Request:  RequestTemplate{Method: "get", BodyTemplate: "{}"},
							Response: ResponseMapping{OutputPath: "$.out"},
						},
					},
					{
						ID:       "c-put",
						Type:     "custom",
						Endpoint: "http://localhost:8000",
						Mapping: &CustomMapping{
							Request:  RequestTemplate{Method: "pUt", BodyTemplate: "{}"},
							Response: ResponseMapping{OutputPath: "$.out"},
						},
					},
					{
						ID:       "c-patch",
						Type:     "custom",
						Endpoint: "http://localhost:8000",
						Mapping: &CustomMapping{
							Request:  RequestTemplate{Method: "PATCH", BodyTemplate: "{}"},
							Response: ResponseMapping{OutputPath: "$.out"},
						},
					},
					{
						ID:       "c-delete",
						Type:     "custom",
						Endpoint: "http://localhost:8000",
						Mapping: &CustomMapping{
							Request:  RequestTemplate{Method: "delete", BodyTemplate: "{}"},
							Response: ResponseMapping{OutputPath: "$.out"},
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(&tc.cfg); err != nil {
				t.Fatalf("expected valid config, got error: %v", err)
			}
		})
	}
}

func TestValidationSuccessWithFallbackAndCustom(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{Port: 8080},
		Agents: []AgentConfig{
			{
				ID:               "primary",
				Type:             "openai",
				Endpoint:         "http://localhost:8000",
				FallbackAgentIDs: []string{"secondary"},
			},
			{
				ID:       "secondary",
				Type:     "custom",
				Endpoint: "http://localhost:8001",
				Mapping: &CustomMapping{
					Request: RequestTemplate{
						Method:       "POST",
						BodyTemplate: `{"input": {{ .Task.Input | toJson }}}`,
					},
					Response: ResponseMapping{
						OutputPath: "$.result",
					},
				},
			},
		},
	}

	if err := Validate(cfg); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}

func TestLoadConfigFileNotFound(t *testing.T) {
	_, err := Load("/path/to/non/existent/config.yaml")
	if err == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "a2a-test-invalid-*.yaml")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write([]byte("server: [invalid yaml")); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	tmpFile.Close()

	_, err = Load(tmpFile.Name())
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestExpandEnv(t *testing.T) {
	os.Setenv("VAR_A", "value_a")
	os.Setenv("VAR_B", "value_b")
	defer os.Unsetenv("VAR_A")
	defer os.Unsetenv("VAR_B")

	input := []byte("first=${VAR_A};second=${VAR_B};unset=${VAR_UNSET};literal=${NOT_MATCHED")
	expected := "first=value_a;second=value_b;unset=;literal=${NOT_MATCHED"

	actual := string(expandEnv(input))
	if actual != expected {
		t.Errorf("expandEnv mismatch:\nexpected: %s\ngot:      %s", expected, actual)
	}
}

func TestLoadReferenceExampleConfig(t *testing.T) {
	t.Setenv("LANGGRAPH_API_KEY", "dummy-langgraph-key")
	t.Setenv("CREWAI_API_TOKEN", "dummy-crewai-token")
	t.Setenv("AUTOGEN_API_KEY", "dummy-autogen-key")
	t.Setenv("OPENAI_API_KEY", "dummy-openai-key")
	t.Setenv("ENTERPRISE_AUTH_TOKEN", "dummy-enterprise-token")

	cfg, err := Load("../../config/a2a-proxy.example.yaml")
	if err != nil {
		t.Fatalf("failed to load example config: %v", err)
	}

	if cfg.Server.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Server.Port)
	}
	if !cfg.Security.Enabled {
		t.Errorf("expected security enabled")
	}
	if len(cfg.Security.APIKeys) < 2 {
		t.Errorf("expected at least 2 api keys, got %d", len(cfg.Security.APIKeys))
	}
	if len(cfg.Agents) != 5 {
		t.Fatalf("expected 5 example agents, got %d", len(cfg.Agents))
	}

	types := make(map[string]bool)
	for _, a := range cfg.Agents {
		types[a.Type] = true
	}
	for _, expectedType := range []string{"langgraph", "crewai", "autogen", "openai", "custom"} {
		if !types[expectedType] {
			t.Errorf("missing agent type %s in example config", expectedType)
		}
	}
	if cfg.Storage.Backend != "memory" {
		t.Errorf("expected storage backend memory, got %s", cfg.Storage.Backend)
	}
	if cfg.Storage.Memory.MaxTasks != 10000 {
		t.Errorf("expected storage memory max_tasks 10000, got %d", cfg.Storage.Memory.MaxTasks)
	}
}

func TestLoadProgoExampleConfig(t *testing.T) {
	t.Setenv("ADMIN_API_KEY", "dummy-admin-key")
	t.Setenv("ANALYST_API_KEY", "dummy-analyst-key")
	t.Setenv("LANGGRAPH_API_KEY", "dummy-langgraph-key")
	t.Setenv("CREWAI_API_TOKEN", "dummy-crewai-token")
	t.Setenv("AUTOGEN_API_KEY", "dummy-autogen-key")
	t.Setenv("OPENAI_API_KEY", "dummy-openai-key")
	t.Setenv("ENTERPRISE_AUTH_TOKEN", "dummy-enterprise-token")

	cfg, err := Load("../../config/progo-a2a.example.yaml")
	if err != nil {
		t.Fatalf("failed to load progo example config: %v", err)
	}

	if cfg.Server.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Server.Port)
	}
	if !cfg.Security.Enabled {
		t.Errorf("expected security enabled")
	}
	if len(cfg.Security.APIKeys) < 2 {
		t.Errorf("expected at least 2 api keys, got %d", len(cfg.Security.APIKeys))
	}
	if len(cfg.Agents) != 5 {
		t.Fatalf("expected 5 example agents, got %d", len(cfg.Agents))
	}

	types := make(map[string]bool)
	for _, a := range cfg.Agents {
		types[a.Type] = true
	}
	for _, expectedType := range []string{"langgraph", "crewai", "autogen", "openai", "custom"} {
		if !types[expectedType] {
			t.Errorf("missing agent type %s in example config", expectedType)
		}
	}
	if cfg.Storage.Backend != "memory" {
		t.Errorf("expected storage backend memory, got %s", cfg.Storage.Backend)
	}
	if cfg.Storage.Memory.MaxTasks != 10000 {
		t.Errorf("expected storage memory max_tasks 10000, got %d", cfg.Storage.Memory.MaxTasks)
	}
}

func TestValidationSelfFallback(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{Port: 8080},
		Agents: []AgentConfig{
			{
				ID:               "agent-self",
				Type:             "openai",
				Endpoint:         "http://localhost:8000",
				FallbackAgentIDs: []string{"agent-self"},
			},
		},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "cannot specify itself as fallback") {
		t.Fatalf("expected self-fallback error, got: %v", err)
	}
}

func TestValidationCyclicFallback(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{Port: 8080},
		Agents: []AgentConfig{
			{
				ID:               "agent-1",
				Type:             "openai",
				Endpoint:         "http://localhost:8000",
				FallbackAgentIDs: []string{"agent-2"},
			},
			{
				ID:               "agent-2",
				Type:             "crewai",
				Endpoint:         "http://localhost:8001",
				FallbackAgentIDs: []string{"agent-3"},
			},
			{
				ID:               "agent-3",
				Type:             "autogen",
				Endpoint:         "http://localhost:8002",
				FallbackAgentIDs: []string{"agent-1"},
			},
		},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "cyclic fallback detected") {
		t.Fatalf("expected cyclic fallback error, got: %v", err)
	}
}

func TestStorageConfig_Defaults(t *testing.T) {
	t.Run("omitted storage defaults to memory with 10000 max_tasks", func(t *testing.T) {
		yamlContent := `
agents:
  - id: "agent-default"
    type: "openai"
    endpoint: "http://localhost:8000"
`
		tmpFile, err := os.CreateTemp("", "storage-default-*.yaml")
		if err != nil {
			t.Fatalf("create temp file failed: %v", err)
		}
		defer os.Remove(tmpFile.Name())
		if _, err := tmpFile.Write([]byte(yamlContent)); err != nil {
			t.Fatalf("write temp file failed: %v", err)
		}
		tmpFile.Close()

		cfg, err := Load(tmpFile.Name())
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		if cfg.Storage.Backend != "memory" {
			t.Errorf("expected backend 'memory', got %q", cfg.Storage.Backend)
		}
		if cfg.Storage.Memory.MaxTasks != 10000 {
			t.Errorf("expected max_tasks 10000, got %d", cfg.Storage.Memory.MaxTasks)
		}
	})

	t.Run("explicit memory backend with zero max_tasks defaults to 10000", func(t *testing.T) {
		yamlContent := `
storage:
  backend: memory
agents:
  - id: "agent-default"
    type: "openai"
    endpoint: "http://localhost:8000"
`
		tmpFile, err := os.CreateTemp("", "storage-mem-*.yaml")
		if err != nil {
			t.Fatalf("create temp file failed: %v", err)
		}
		defer os.Remove(tmpFile.Name())
		if _, err := tmpFile.Write([]byte(yamlContent)); err != nil {
			t.Fatalf("write temp file failed: %v", err)
		}
		tmpFile.Close()

		cfg, err := Load(tmpFile.Name())
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		if cfg.Storage.Backend != "memory" {
			t.Errorf("expected backend 'memory', got %q", cfg.Storage.Backend)
		}
		if cfg.Storage.Memory.MaxTasks != 10000 {
			t.Errorf("expected max_tasks 10000, got %d", cfg.Storage.Memory.MaxTasks)
		}
	})

	t.Run("postgres backend defaults populated through Load", func(t *testing.T) {
		yamlContent := `
storage:
  backend: postgres
  postgres:
    dsn: "postgres://user:pass@localhost:5432/mydb"
agents:
  - id: "agent-default"
    type: "openai"
    endpoint: "http://localhost:8000"
`
		tmpFile, err := os.CreateTemp("", "storage-pg-*.yaml")
		if err != nil {
			t.Fatalf("create temp file failed: %v", err)
		}
		defer os.Remove(tmpFile.Name())
		if _, err := tmpFile.Write([]byte(yamlContent)); err != nil {
			t.Fatalf("write temp file failed: %v", err)
		}
		tmpFile.Close()

		cfg, err := Load(tmpFile.Name())
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		if cfg.Storage.Backend != "postgres" {
			t.Errorf("expected backend 'postgres', got %q", cfg.Storage.Backend)
		}
		if cfg.Storage.Postgres.MaxConnections != 20 {
			t.Errorf("expected max_connections 20, got %d", cfg.Storage.Postgres.MaxConnections)
		}
		if cfg.Storage.Postgres.MinConnections != 2 {
			t.Errorf("expected min_connections 2, got %d", cfg.Storage.Postgres.MinConnections)
		}
		if cfg.Storage.Postgres.MaxConnectionLifetimeSeconds != 1800 {
			t.Errorf("expected max lifetime 1800, got %d", cfg.Storage.Postgres.MaxConnectionLifetimeSeconds)
		}
		if cfg.Storage.Postgres.MaxConnectionIdleTimeSeconds != 300 {
			t.Errorf("expected max idle 300, got %d", cfg.Storage.Postgres.MaxConnectionIdleTimeSeconds)
		}
		if cfg.Storage.Postgres.HealthCheckPeriodSeconds != 30 {
			t.Errorf("expected health check 30, got %d", cfg.Storage.Postgres.HealthCheckPeriodSeconds)
		}
		if cfg.Storage.Postgres.ConnectTimeoutSeconds != 5 {
			t.Errorf("expected connect timeout 5, got %d", cfg.Storage.Postgres.ConnectTimeoutSeconds)
		}
	})

	t.Run("direct Validate does not mutate hand-built configs", func(t *testing.T) {
		cfg := &Config{
			Server: ServerConfig{Port: 8080},
			Agents: []AgentConfig{
				{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000"},
			},
			Storage: StorageConfig{
				Backend: "postgres",
				Postgres: PostgresStorageConfig{
					DSN:                          "postgres://localhost/test",
					MaxConnections:               15,
					MinConnections:               0,
					MaxConnectionLifetimeSeconds: 600,
					MaxConnectionIdleTimeSeconds: 120,
					HealthCheckPeriodSeconds:     15,
					ConnectTimeoutSeconds:        3,
				},
			},
		}

		if err := Validate(cfg); err != nil {
			t.Fatalf("expected direct Validate to succeed, got: %v", err)
		}

		if cfg.Storage.Postgres.MinConnections != 0 {
			t.Errorf("expected MinConnections to remain 0, got %d", cfg.Storage.Postgres.MinConnections)
		}
		if cfg.Storage.Postgres.MaxConnections != 15 {
			t.Errorf("expected MaxConnections to remain 15, got %d", cfg.Storage.Postgres.MaxConnections)
		}

		// Also check that Backend: "" is not mutated
		cfgMem := &Config{
			Server: ServerConfig{Port: 8080},
			Agents: []AgentConfig{
				{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000"},
			},
		}
		if err := Validate(cfgMem); err != nil {
			t.Fatalf("expected direct Validate to succeed on empty storage, got: %v", err)
		}
		if cfgMem.Storage.Backend != "" {
			t.Errorf("expected Storage.Backend to remain empty string, got %q", cfgMem.Storage.Backend)
		}
	})
}

func baseValidConfig() Config {
	return Config{
		Server: ServerConfig{Port: 8080},
		Agents: []AgentConfig{
			{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000"},
		},
	}
}

func TestStorageConfig_Validation(t *testing.T) {
	validPGConfig := func() Config {
		c := baseValidConfig()
		c.Storage = StorageConfig{
			Backend: "postgres",
			Postgres: PostgresStorageConfig{
				DSN:                          "postgres://user:pass@localhost:5432/db",
				MaxConnections:               20,
				MinConnections:               2,
				MaxConnectionLifetimeSeconds: 1800,
				MaxConnectionIdleTimeSeconds: 300,
				HealthCheckPeriodSeconds:     30,
				ConnectTimeoutSeconds:        5,
			},
		}
		return c
	}

	tests := []struct {
		name        string
		modify      func(c *Config)
		errContains string
		expectValid bool
	}{
		{
			name:        "valid default memory config",
			modify:      func(c *Config) {},
			expectValid: true,
		},
		{
			name: "valid memory config with custom max_tasks",
			modify: func(c *Config) {
				c.Storage.Backend = "memory"
				c.Storage.Memory.MaxTasks = 50000
			},
			expectValid: true,
		},
		{
			name: "valid memory config with upper bound max_tasks 1000000",
			modify: func(c *Config) {
				c.Storage.Backend = "memory"
				c.Storage.Memory.MaxTasks = 1000000
			},
			expectValid: true,
		},
		{
			name: "memory max_tasks negative rejected",
			modify: func(c *Config) {
				c.Storage.Backend = "memory"
				c.Storage.Memory.MaxTasks = -1
			},
			errContains: "storage memory max_tasks must be between 0 and 1000000",
		},
		{
			name: "memory max_tasks above 1000000 rejected",
			modify: func(c *Config) {
				c.Storage.Backend = "memory"
				c.Storage.Memory.MaxTasks = 1000001
			},
			errContains: "storage memory max_tasks must be between 0 and 1000000",
		},
		{
			name: "memory backend rejects non-empty postgres DSN",
			modify: func(c *Config) {
				c.Storage.Backend = "memory"
				c.Storage.Postgres.DSN = "postgres://user:super_secret_pw@localhost:5432/db"
			},
			errContains: "postgres dsn cannot be configured when storage backend is memory",
		},
		{
			name: "memory backend rejects whitespace postgres DSN",
			modify: func(c *Config) {
				c.Storage.Backend = "memory"
				c.Storage.Postgres.DSN = "   "
			},
			errContains: "postgres dsn cannot be configured when storage backend is memory",
		},
		{
			name: "memory backend rejects ignored postgres pool settings",
			modify: func(c *Config) {
				c.Storage.Backend = "memory"
				c.Storage.Postgres.MaxConnections = 20
			},
			errContains: "postgres settings cannot be configured when storage backend is memory",
		},
		{
			name: "unknown storage backend rejected",
			modify: func(c *Config) {
				c.Storage.Backend = "redis"
			},
			errContains: "unknown storage backend \"redis\"",
		},
		{
			name: "unsupported sqlite backend rejected",
			modify: func(c *Config) {
				c.Storage.Backend = "sqlite"
			},
			errContains: "unknown storage backend \"sqlite\"",
		},
		{
			name:        "valid postgres config",
			modify:      func(c *Config) { *c = validPGConfig() },
			expectValid: true,
		},
		{
			name: "postgres backend rejects ignored memory settings",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Memory.MaxTasks = 10000
			},
			errContains: "memory settings cannot be configured when storage backend is postgres",
		},
		{
			name: "valid postgres min_connections 0",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.MinConnections = 0
			},
			expectValid: true,
		},
		{
			name: "valid postgres min_connections equal max_connections",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.MinConnections = 20
				c.Storage.Postgres.MaxConnections = 20
			},
			expectValid: true,
		},
		{
			name: "valid postgres max_connections upper boundary 1000",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.MaxConnections = 1000
				c.Storage.Postgres.MinConnections = 100
			},
			expectValid: true,
		},
		{
			name: "valid postgres max_connections lower boundary 1",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.MaxConnections = 1
				c.Storage.Postgres.MinConnections = 1
			},
			expectValid: true,
		},
		{
			name: "postgres empty DSN rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.DSN = ""
			},
			errContains: "storage postgres dsn cannot be empty or whitespace",
		},
		{
			name: "postgres whitespace DSN rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.DSN = "   \t\n  "
			},
			errContains: "storage postgres dsn cannot be empty or whitespace",
		},
		{
			name: "postgres max_connections zero rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.MaxConnections = 0
			},
			errContains: "storage postgres max_connections must be between 1 and 1000",
		},
		{
			name: "postgres max_connections negative rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.MaxConnections = -5
			},
			errContains: "storage postgres max_connections must be between 1 and 1000",
		},
		{
			name: "postgres max_connections exceeds 1000 rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.MaxConnections = 1001
			},
			errContains: "storage postgres max_connections must be between 1 and 1000",
		},
		{
			name: "postgres min_connections negative rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.MinConnections = -1
			},
			errContains: "storage postgres min_connections must be between 0 and max_connections",
		},
		{
			name: "postgres min_connections greater than max_connections rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.MaxConnections = 20
				c.Storage.Postgres.MinConnections = 21
			},
			errContains: "storage postgres min_connections must be between 0 and max_connections (20), got 21",
		},
		{
			name: "postgres max_connection_lifetime_seconds zero rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.MaxConnectionLifetimeSeconds = 0
			},
			errContains: "storage postgres max_connection_lifetime_seconds must be positive",
		},
		{
			name: "postgres max_connection_lifetime_seconds negative rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.MaxConnectionLifetimeSeconds = -10
			},
			errContains: "storage postgres max_connection_lifetime_seconds must be positive",
		},
		{
			name: "postgres max_connection_idle_time_seconds zero rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.MaxConnectionIdleTimeSeconds = 0
			},
			errContains: "storage postgres max_connection_idle_time_seconds must be positive",
		},
		{
			name: "postgres max_connection_idle_time_seconds negative rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.MaxConnectionIdleTimeSeconds = -1
			},
			errContains: "storage postgres max_connection_idle_time_seconds must be positive",
		},
		{
			name: "postgres health_check_period_seconds zero rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.HealthCheckPeriodSeconds = 0
			},
			errContains: "storage postgres health_check_period_seconds must be positive",
		},
		{
			name: "postgres health_check_period_seconds negative rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.HealthCheckPeriodSeconds = -1
			},
			errContains: "storage postgres health_check_period_seconds must be positive",
		},
		{
			name: "postgres connect_timeout_seconds zero rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.ConnectTimeoutSeconds = 0
			},
			errContains: "storage postgres connect_timeout_seconds must be positive",
		},
		{
			name: "postgres connect_timeout_seconds negative rejected",
			modify: func(c *Config) {
				*c = validPGConfig()
				c.Storage.Postgres.ConnectTimeoutSeconds = -1
			},
			errContains: "storage postgres connect_timeout_seconds must be positive",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseValidConfig()
			tc.modify(&cfg)
			err := Validate(&cfg)

			if tc.expectValid {
				if err != nil {
					t.Fatalf("expected valid config, got error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errContains)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("expected error containing %q, got %q", tc.errContains, err.Error())
			}
		})
	}
}

func TestStorageConfig_DSNNonLeakageInValidationError(t *testing.T) {
	secretDSN := "postgres://admin_secret_user:super_secret_password_999@db.internal:5432/secrets"
	cfg := Config{
		Server: ServerConfig{Port: 8080},
		Agents: []AgentConfig{
			{ID: "agent-1", Type: "openai", Endpoint: "http://localhost:8000"},
		},
		Storage: StorageConfig{
			Backend: "memory",
			Postgres: PostgresStorageConfig{
				DSN: secretDSN,
			},
		},
	}

	err := Validate(&cfg)
	if err == nil {
		t.Fatal("expected error for memory backend with postgres DSN, got nil")
	}
	if strings.Contains(err.Error(), "super_secret_password_999") {
		t.Errorf("validation error leaked password from DSN: %s", err.Error())
	}
	if strings.Contains(err.Error(), secretDSN) {
		t.Errorf("validation error leaked full DSN: %s", err.Error())
	}
}

func TestRuntimeRoleAndWorkerConfig_DefaultsAndValidation(t *testing.T) {
	validPG := func() Config {
		c := baseValidConfig()
		c.Storage.Backend = "postgres"
		c.Storage.Postgres = PostgresStorageConfig{
			DSN:                          "postgres://user:pass@localhost:5432/db",
			MaxConnections:               20,
			MinConnections:               2,
			MaxConnectionLifetimeSeconds: 1800,
			MaxConnectionIdleTimeSeconds: 300,
			HealthCheckPeriodSeconds:     30,
			ConnectTimeoutSeconds:        5,
		}
		c.Worker = WorkerConfig{
			WorkerID:                 "worker-test",
			Concurrency:              10,
			BatchSize:                5,
			PollIntervalMilliseconds: 1000,
			LeaseDurationSeconds:     30,
			RenewalIntervalSeconds:   10,
			RetryBackoffSeconds:      15,
			DrainTimeoutSeconds:      30,
		}
		return c
	}

	tests := []struct {
		name        string
		modify      func(c *Config)
		errContains string
		expectValid bool
	}{
		{
			name: "default role is valid with memory storage",
			modify: func(c *Config) {
				c.Role = ""
			},
			expectValid: true,
		},
		{
			name: "explicit api role is valid with memory storage",
			modify: func(c *Config) {
				c.Role = "api"
			},
			expectValid: true,
		},
		{
			name: "explicit worker role valid with postgres storage",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
			},
			expectValid: true,
		},
		{
			name: "explicit all role valid with postgres storage",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "all"
			},
			expectValid: true,
		},
		{
			name: "invalid role rejected",
			modify: func(c *Config) {
				c.Role = "invalid-role"
			},
			errContains: "invalid runtime role \"invalid-role\": must be one of api, worker, all",
		},
		{
			name: "worker role with memory storage rejected",
			modify: func(c *Config) {
				c.Role = "worker"
				c.Storage.Backend = "memory"
			},
			errContains: "runtime role \"worker\" requires postgres storage backend",
		},
		{
			name: "all role with memory storage rejected",
			modify: func(c *Config) {
				c.Role = "all"
				c.Storage.Backend = "memory"
			},
			errContains: "runtime role \"all\" requires postgres storage backend",
		},
		{
			name: "worker worker_id whitespace-only rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.WorkerID = "   \t\n  "
			},
			errContains: "worker worker_id cannot be whitespace-only",
		},
		{
			name: "worker worker_id exceeds max length",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.WorkerID = strings.Repeat("x", 257)
			},
			errContains: "worker worker_id exceeds maximum length",
		},
		{
			name: "worker concurrency zero rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.Concurrency = 0
			},
			errContains: "worker concurrency must be between 1 and 1000",
		},
		{
			name: "worker concurrency negative rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.Concurrency = -1
			},
			errContains: "worker concurrency must be between 1 and 1000",
		},
		{
			name: "worker concurrency above 1000 rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.Concurrency = 1001
			},
			errContains: "worker concurrency must be between 1 and 1000",
		},
		{
			name: "worker batch_size zero rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.BatchSize = 0
			},
			errContains: "worker batch_size must be positive",
		},
		{
			name: "worker batch_size exceeds concurrency rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.Concurrency = 5
				c.Worker.BatchSize = 6
			},
			errContains: "must not exceed concurrency",
		},
		{
			name: "worker poll_interval non-positive rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.PollIntervalMilliseconds = 0
			},
			errContains: "worker poll_interval_milliseconds must be positive",
		},
		{
			name: "worker poll_interval exceeds max allowed rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.PollIntervalMilliseconds = MaxWorkerPollIntervalMilliseconds + 1
			},
			errContains: "worker poll_interval_milliseconds (600001) exceeds max allowed",
		},
		{
			name: "worker lease_duration below minimum rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.LeaseDurationSeconds = 0
			},
			errContains: "worker lease_duration_seconds must be at least 2",
		},
		{
			name: "worker lease_duration exceeds 24h rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.LeaseDurationSeconds = 86401
			},
			errContains: "worker lease_duration_seconds (86401) exceeds max allowed",
		},
		{
			name: "worker renewal_interval non-positive rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.RenewalIntervalSeconds = 0
			},
			errContains: "worker renewal_interval_seconds must be positive",
		},
		{
			name: "worker renewal_interval more than half lease_duration rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.LeaseDurationSeconds = 30
				c.Worker.RenewalIntervalSeconds = 16
			},
			errContains: "must be at most half of lease_duration_seconds",
		},
		{
			name: "worker renewal_interval exactly half lease_duration accepted",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.LeaseDurationSeconds = 30
				c.Worker.RenewalIntervalSeconds = 15
			},
			expectValid: true,
		},
		{
			name: "worker retry_backoff non-positive rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.RetryBackoffSeconds = 0
			},
			errContains: "worker retry_backoff_seconds must be positive",
		},
		{
			name: "worker retry_backoff exceeds max allowed rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.RetryBackoffSeconds = int(model.MaxRetryBackoff.Seconds()) + 1
			},
			errContains: "worker retry_backoff_seconds (604801) exceeds max allowed",
		},
		{
			name: "worker drain_timeout non-positive rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.DrainTimeoutSeconds = 0
			},
			errContains: "worker drain_timeout_seconds must be positive",
		},
		{
			name: "worker drain_timeout exceeds max allowed rejected",
			modify: func(c *Config) {
				*c = validPG()
				c.Role = "worker"
				c.Worker.DrainTimeoutSeconds = MaxWorkerDrainTimeoutSeconds + 1
			},
			errContains: "worker drain_timeout_seconds (3601) exceeds max allowed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseValidConfig()
			tc.modify(&cfg)
			err := Validate(&cfg)

			if tc.expectValid {
				if err != nil {
					t.Fatalf("expected valid config, got error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errContains)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("expected error containing %q, got %q", tc.errContains, err.Error())
			}
		})
	}
}

func TestDirectValidateDoesNotMutateConfig(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Role = "" // empty hand-built role

	if err := Validate(&cfg); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	// Must NOT mutate hand-built config
	if cfg.Role != "" {
		t.Fatalf("expected cfg.Role to remain empty string, got %q", cfg.Role)
	}
}

func TestShippedExampleConfigsCompatibility(t *testing.T) {
	t.Setenv("STORAGE_BACKEND", "postgres")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/testdb")
	t.Setenv("MIGRATE_ON_START", "false")
	t.Setenv("ADMIN_API_KEY", "admin-key-12345")
	t.Setenv("ANALYST_API_KEY", "analyst-key-67890")
	t.Setenv("LANGGRAPH_API_KEY", "langgraph-key")
	t.Setenv("CREWAI_API_TOKEN", "crewai-token")
	t.Setenv("AUTOGEN_API_KEY", "autogen-key")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("ENTERPRISE_AUTH_TOKEN", "enterprise-token")

	// 1. config/progo-a2a.example.yaml
	cfg1, err := Load("../../config/progo-a2a.example.yaml")
	if err != nil {
		t.Fatalf("failed to load config/progo-a2a.example.yaml: %v", err)
	}
	if cfg1.Role != "api" {
		t.Errorf("expected default role 'api', got %q", cfg1.Role)
	}
	if cfg1.Worker.Concurrency != 10 {
		t.Errorf("expected default concurrency 10, got %d", cfg1.Worker.Concurrency)
	}

	// 2. config/a2a-proxy.example.yaml
	cfg2, err := Load("../../config/a2a-proxy.example.yaml")
	if err != nil {
		t.Fatalf("failed to load config/a2a-proxy.example.yaml: %v", err)
	}
	if cfg2.Role != "api" {
		t.Errorf("expected default role 'api', got %q", cfg2.Role)
	}
	if cfg2.Storage.Backend != "memory" {
		t.Errorf("expected storage backend memory, got %q", cfg2.Storage.Backend)
	}
}
