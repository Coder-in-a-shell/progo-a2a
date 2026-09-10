package config

import "time"

type Config struct {
	Role     string         `yaml:"role"`
	Server   ServerConfig   `yaml:"server"`
	Worker   WorkerConfig   `yaml:"worker"`
	Security SecurityConfig `yaml:"security"`
	Agents   []AgentConfig  `yaml:"agents"`
	Storage  StorageConfig  `yaml:"storage"`
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

type StorageConfig struct {
	Backend  string                `yaml:"backend"`
	Memory   MemoryStorageConfig   `yaml:"memory"`
	Postgres PostgresStorageConfig `yaml:"postgres"`
}

type MemoryStorageConfig struct {
	MaxTasks int `yaml:"max_tasks"`
}

type PostgresStorageConfig struct {
	DSN                          string `yaml:"dsn"`
	MaxConnections               int    `yaml:"max_connections"`
	MinConnections               int    `yaml:"min_connections"`
	MaxConnectionLifetimeSeconds int    `yaml:"max_connection_lifetime_seconds"`
	MaxConnectionIdleTimeSeconds int    `yaml:"max_connection_idle_time_seconds"`
	HealthCheckPeriodSeconds     int    `yaml:"health_check_period_seconds"`
	ConnectTimeoutSeconds        int    `yaml:"connect_timeout_seconds"`
	MigrateOnStart               bool   `yaml:"migrate_on_start"`
}

// Practical upper bounds for worker timing configurations
const (
	// MaxWorkerPollIntervalMilliseconds bounds the empty-queue polling interval to 10 minutes.
	MaxWorkerPollIntervalMilliseconds = 600_000
	// MaxWorkerDrainTimeoutSeconds bounds the graceful shutdown drain deadline to 1 hour.
	MaxWorkerDrainTimeoutSeconds = 3600
)

// WorkerConfig defines settings for the durable background worker engine.
type WorkerConfig struct {
	WorkerID                 string `yaml:"worker_id"`
	Concurrency              int    `yaml:"concurrency"`
	BatchSize                int    `yaml:"batch_size"`
	PollIntervalMilliseconds int    `yaml:"poll_interval_milliseconds"`
	LeaseDurationSeconds     int    `yaml:"lease_duration_seconds"`
	RenewalIntervalSeconds   int    `yaml:"renewal_interval_seconds"`
	RetryBackoffSeconds      int    `yaml:"retry_backoff_seconds"`
	DrainTimeoutSeconds      int    `yaml:"drain_timeout_seconds"`
}

// PollInterval returns the configured poll interval duration.
func (w WorkerConfig) PollInterval() time.Duration {
	return time.Duration(w.PollIntervalMilliseconds) * time.Millisecond
}

// LeaseDuration returns the configured lease duration.
func (w WorkerConfig) LeaseDuration() time.Duration {
	return time.Duration(w.LeaseDurationSeconds) * time.Second
}

// RenewalInterval returns the configured renewal interval duration.
func (w WorkerConfig) RenewalInterval() time.Duration {
	return time.Duration(w.RenewalIntervalSeconds) * time.Second
}

// RetryBackoff returns the configured retry backoff duration.
func (w WorkerConfig) RetryBackoff() time.Duration {
	return time.Duration(w.RetryBackoffSeconds) * time.Second
}

// DrainTimeout returns the configured graceful shutdown drain timeout.
func (w WorkerConfig) DrainTimeout() time.Duration {
	return time.Duration(w.DrainTimeoutSeconds) * time.Second
}
