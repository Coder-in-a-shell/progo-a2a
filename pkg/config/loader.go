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
}
