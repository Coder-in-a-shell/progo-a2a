package config

import (
	"fmt"
	"net/url"
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

		if strings.TrimSpace(agent.Endpoint) == "" {
			return fmt.Errorf("agent %s endpoint cannot be empty", agent.ID)
		}
		if strings.ContainsAny(agent.Endpoint, " \t\r\n") {
			return fmt.Errorf("agent %s endpoint is malformed: contains whitespace", agent.ID)
		}
		u, err := url.Parse(agent.Endpoint)
		if err != nil {
			return fmt.Errorf("agent %s endpoint is malformed: %w", agent.ID, err)
		}
		if !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("agent %s endpoint must be an absolute http or https URL", agent.ID)
		}
		if u.Hostname() == "" {
			return fmt.Errorf("agent %s endpoint must have a non-empty hostname", agent.ID)
		}
		if u.User != nil {
			return fmt.Errorf("agent %s endpoint must not contain user info", agent.ID)
		}
		if u.Fragment != "" || strings.Contains(agent.Endpoint, "#") {
			return fmt.Errorf("agent %s endpoint must not contain a fragment", agent.ID)
		}

		switch agent.Type {
		case "langgraph", "crewai", "autogen", "openai", "custom":
			// valid
		case "":
			return fmt.Errorf("agent %s type cannot be empty", agent.ID)
		default:
			return fmt.Errorf("agent %s has unsupported type %q: must be one of langgraph, crewai, autogen, openai, custom", agent.ID, agent.Type)
		}

		if agent.Retries < 0 || agent.Retries > 10 {
			return fmt.Errorf("agent %s retries must be between 0 and 10, got %d", agent.ID, agent.Retries)
		}
		if agent.TimeoutSeconds < 0 {
			return fmt.Errorf("agent %s timeout_seconds cannot be negative, got %d", agent.ID, agent.TimeoutSeconds)
		}

		authType := agent.Auth.Type
		if authType == "" {
			authType = "none"
		}
		switch authType {
		case "none":
			if agent.Auth.Token != "" || agent.Auth.HeaderName != "" || agent.Auth.HeaderValue != "" {
				return fmt.Errorf("agent %s auth type 'none' must not contain credentials", agent.ID)
			}
		case "bearer":
			if strings.TrimSpace(agent.Auth.Token) == "" {
				return fmt.Errorf("agent %s auth type 'bearer' requires a non-empty token", agent.ID)
			}
			if agent.Auth.HeaderName != "" || agent.Auth.HeaderValue != "" {
				return fmt.Errorf("agent %s auth type 'bearer' must not contain header credentials", agent.ID)
			}
		case "header":
			if agent.Auth.Token != "" {
				return fmt.Errorf("agent %s auth type 'header' must not contain a bearer token", agent.ID)
			}
			if !isValidHeaderFieldName(agent.Auth.HeaderName) {
				return fmt.Errorf("agent %s auth type 'header' requires an RFC-valid header_name, got %q", agent.ID, agent.Auth.HeaderName)
			}
			if strings.TrimSpace(agent.Auth.HeaderValue) == "" {
				return fmt.Errorf("agent %s auth type 'header' requires a non-empty header_value", agent.ID)
			}
		default:
			return fmt.Errorf("agent %s has unsupported auth type %q: must be one of none, bearer, header", agent.ID, agent.Auth.Type)
		}

		if agent.Type == "custom" {
			if agent.Mapping == nil {
				return fmt.Errorf("agent %s is of type 'custom' but mapping is missing", agent.ID)
			}
			method := strings.ToUpper(strings.TrimSpace(agent.Mapping.Request.Method))
			switch method {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
				// valid
			default:
				return fmt.Errorf("agent %s custom mapping request.method must be one of GET, POST, PUT, PATCH, DELETE, got %q", agent.ID, agent.Mapping.Request.Method)
			}
			if strings.TrimSpace(agent.Mapping.Request.BodyTemplate) == "" {
				return fmt.Errorf("agent %s custom mapping request.body_template cannot be empty", agent.ID)
			}
			if strings.TrimSpace(agent.Mapping.Response.OutputPath) == "" {
				return fmt.Errorf("agent %s custom mapping response.output_path cannot be empty", agent.ID)
			}
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

func isValidHeaderFieldName(name string) bool {
	if len(name) == 0 {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		if !isValidHeaderFieldByte(b) {
			return false
		}
	}
	return true
}

func isValidHeaderFieldByte(b byte) bool {
	if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') {
		return true
	}
	switch b {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}
