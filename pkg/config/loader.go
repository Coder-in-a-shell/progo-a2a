package config

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

var envRegex = regexp.MustCompile(`\$\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)

func expandEnv(content []byte) []byte {
	return envRegex.ReplaceAllFunc(content, func(match []byte) []byte {
		key := string(match[2 : len(match)-1])
		return []byte(os.Getenv(key))
	})
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	expanded := expandEnv(data)

	var cfg Config
	if err := yaml.Unmarshal(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	applyDefaults(&cfg)

	if err := Validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Role == "" {
		cfg.Role = "api"
	}
	if cfg.Worker.Concurrency == 0 {
		cfg.Worker.Concurrency = 10
	}
	if cfg.Worker.BatchSize == 0 {
		cfg.Worker.BatchSize = 5
	}
	if cfg.Worker.PollIntervalMilliseconds == 0 {
		cfg.Worker.PollIntervalMilliseconds = 1000
	}
	if cfg.Worker.LeaseDurationSeconds == 0 {
		cfg.Worker.LeaseDurationSeconds = 30
	}
	if cfg.Worker.RenewalIntervalSeconds == 0 {
		cfg.Worker.RenewalIntervalSeconds = 10
	}
	if cfg.Worker.RetryBackoffSeconds == 0 {
		cfg.Worker.RetryBackoffSeconds = 15
	}
	if cfg.Worker.DrainTimeoutSeconds == 0 {
		cfg.Worker.DrainTimeoutSeconds = 30
	}

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.ReadTimeoutSeconds == 0 {
		cfg.Server.ReadTimeoutSeconds = 30
	}
	if cfg.Server.WriteTimeoutSeconds == 0 {
		cfg.Server.WriteTimeoutSeconds = 120
	}
	if cfg.Server.IdleTimeoutSeconds == 0 {
		cfg.Server.IdleTimeoutSeconds = 60
	}
	for i := range cfg.Agents {
		if cfg.Agents[i].TimeoutSeconds == 0 {
			cfg.Agents[i].TimeoutSeconds = 60
		}
	}

	if cfg.Storage.Backend == "" {
		cfg.Storage.Backend = "memory"
	}
	switch cfg.Storage.Backend {
	case "memory":
		if cfg.Storage.Memory.MaxTasks == 0 {
			cfg.Storage.Memory.MaxTasks = 10000
		}
	case "postgres":
		if cfg.Storage.Postgres.MaxConnections == 0 {
			cfg.Storage.Postgres.MaxConnections = 20
		}
		if cfg.Storage.Postgres.MinConnections == 0 {
			cfg.Storage.Postgres.MinConnections = 2
		}
		if cfg.Storage.Postgres.MaxConnectionLifetimeSeconds == 0 {
			cfg.Storage.Postgres.MaxConnectionLifetimeSeconds = 1800
		}
		if cfg.Storage.Postgres.MaxConnectionIdleTimeSeconds == 0 {
			cfg.Storage.Postgres.MaxConnectionIdleTimeSeconds = 300
		}
		if cfg.Storage.Postgres.HealthCheckPeriodSeconds == 0 {
			cfg.Storage.Postgres.HealthCheckPeriodSeconds = 30
		}
		if cfg.Storage.Postgres.ConnectTimeoutSeconds == 0 {
			cfg.Storage.Postgres.ConnectTimeoutSeconds = 5
		}
	}
}
