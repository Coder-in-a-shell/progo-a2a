package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoadConfigWithEnvExpansion(t *testing.T) {
	os.Setenv("TEST_AGENT_KEY", "secret-agent-key-999")
	defer os.Unsetenv("TEST_AGENT_KEY")

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

func TestValidationSuccessWithFallbackAndCustom(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{Port: 8080},
		Agents: []AgentConfig{
			{
				ID:       "primary",
				Type:     "openai",
				Endpoint: "http://localhost:8000",
				FallbackAgentIDs: []string{"secondary"},
			},
			{
				ID:       "secondary",
				Type:     "custom",
				Endpoint: "http://localhost:8001",
				Mapping:  &CustomMapping{},
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

