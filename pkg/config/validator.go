package config

import (
	"fmt"
)

func Validate(cfg *Config) error {
	if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return fmt.Errorf("server port must be between 1 and 65535")
	}

	agentIDs := make(map[string]bool)
	for _, agent := range cfg.Agents {
		if agent.ID == "" {
			return fmt.Errorf("agent id cannot be empty")
		}
		if agentIDs[agent.ID] {
			return fmt.Errorf("duplicate agent id: %s", agent.ID)
		}
		agentIDs[agent.ID] = true

		if agent.Endpoint == "" {
			return fmt.Errorf("agent %s endpoint cannot be empty", agent.ID)
		}
		if agent.Type == "" {
			return fmt.Errorf("agent %s type cannot be empty", agent.ID)
		}
		if agent.Type == "custom" && agent.Mapping == nil {
			return fmt.Errorf("agent %s is of type 'custom' but mapping is missing", agent.ID)
		}
	}

	// Validate fallback agent references
	for _, agent := range cfg.Agents {
		for _, fbID := range agent.FallbackAgentIDs {
			if !agentIDs[fbID] {
				return fmt.Errorf("agent %s references non-existent fallback agent %s", agent.ID, fbID)
			}
		}
	}

	return nil
}
