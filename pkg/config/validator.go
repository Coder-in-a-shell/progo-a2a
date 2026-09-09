package config

import (
	"fmt"
	"strings"
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

	if cfg.Security.Enabled {
		if len(cfg.Security.APIKeys) == 0 {
			return fmt.Errorf("security is enabled but no API keys are configured")
		}
		seenKeys := make(map[string]bool, len(cfg.Security.APIKeys))
		for i, apiKey := range cfg.Security.APIKeys {
			if strings.TrimSpace(apiKey.Key) == "" {
				return fmt.Errorf("security API key %d is empty", i)
			}
			if seenKeys[apiKey.Key] {
				return fmt.Errorf("duplicate security API key at index %d", i)
			}
			seenKeys[apiKey.Key] = true
			for _, allowedAgent := range apiKey.AllowedAgents {
				if allowedAgent != "*" && !agentIDs[allowedAgent] {
					return fmt.Errorf("security API key %d references non-existent allowed agent %s", i, allowedAgent)
				}
			}
		}
	}

	// Validate fallback agent references and detect cycles statically
	adj := make(map[string][]string)
	for _, agent := range cfg.Agents {
		for _, fbID := range agent.FallbackAgentIDs {
			if !agentIDs[fbID] {
				return fmt.Errorf("agent %s references non-existent fallback agent %s", agent.ID, fbID)
			}
			if fbID == agent.ID {
				return fmt.Errorf("agent %s cannot specify itself as fallback", agent.ID)
			}
		}
		adj[agent.ID] = agent.FallbackAgentIDs
	}

	// DFS cycle detection (0: unvisited, 1: visiting, 2: visited)
	visitState := make(map[string]int)
	var checkCycle func(node string, path []string) error
	checkCycle = func(node string, path []string) error {
		visitState[node] = 1
		path = append(path, node)
		for _, neighbor := range adj[node] {
			if visitState[neighbor] == 1 {
				return fmt.Errorf("cyclic fallback detected: %v -> %s", path, neighbor)
			}
			if visitState[neighbor] == 0 {
				if err := checkCycle(neighbor, path); err != nil {
					return err
				}
			}
		}
		visitState[node] = 2
		return nil
	}

	for _, agent := range cfg.Agents {
		if visitState[agent.ID] == 0 {
			if err := checkCycle(agent.ID, nil); err != nil {
				return err
			}
		}
	}

	return nil
}
