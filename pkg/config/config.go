package config

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Security SecurityConfig `yaml:"security"`
	Agents   []AgentConfig  `yaml:"agents"`
}

type ServerConfig struct {
	Host                string `yaml:"host"`
	Port                int    `yaml:"port"`
	ReadTimeoutSeconds  int    `yaml:"read_timeout_seconds"`
	WriteTimeoutSeconds int    `yaml:"write_timeout_seconds"`
	IdleTimeoutSeconds  int    `yaml:"idle_timeout_seconds"`
}

type SecurityConfig struct {
	Enabled bool           `yaml:"enabled"`
	APIKeys []APIKeyConfig `yaml:"api_keys"`
}

type APIKeyConfig struct {
	Key           string   `yaml:"key"`
	ClientID      string   `yaml:"client_id"`
	AllowedAgents []string `yaml:"allowed_agents"`
}

type AgentConfig struct {
	ID               string         `yaml:"id"`
	Name             string         `yaml:"name"`
	Description      string         `yaml:"description"`
	Type             string         `yaml:"type"` // "langgraph", "crewai", "autogen", "openai", "custom"
	Endpoint         string         `yaml:"endpoint"`
	Capabilities     []string       `yaml:"capabilities"`
	Tags             []string       `yaml:"tags"`
	TimeoutSeconds   int            `yaml:"timeout_seconds"`
	Retries          int            `yaml:"retries"`
	FallbackAgentIDs []string       `yaml:"fallback_agent_ids"`
	Auth             AuthConfig     `yaml:"auth"`
	Options          map[string]any `yaml:"options"`
	Mapping          *CustomMapping `yaml:"mapping"`
}

type AuthConfig struct {
	Type        string `yaml:"type"` // "bearer", "header", "none"
	Token       string `yaml:"token"`
	HeaderName  string `yaml:"header_name"`
	HeaderValue string `yaml:"header_value"`
}

type CustomMapping struct {
	Request  RequestTemplate `yaml:"request"`
	Response ResponseMapping `yaml:"response"`
	Stream   StreamMapping   `yaml:"stream"`
}

type RequestTemplate struct {
	Method       string            `yaml:"method"`
	Headers      map[string]string `yaml:"headers"`
	BodyTemplate string            `yaml:"body_template"`
}

type ResponseMapping struct {
	OutputPath    string `yaml:"output_path"`
	StatusPath    string `yaml:"status_path"`
	ArtifactsPath string `yaml:"artifacts_path"`
	ErrorPath     string `yaml:"error_path"`
}

type StreamMapping struct {
	DataPath     string `yaml:"data_path"`
	DoneSentinel string `yaml:"done_sentinel"`
}
