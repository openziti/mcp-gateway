package agora

import (
	"fmt"
	"strings"
)

const (
	defaultInstanceName    = "mcp-gateway"
	defaultDescription     = "MCP gateway"
	defaultAgentNamePrefix = "mcp-gateway"
)

// Defaults supplies per-binary identity defaults.
type Defaults struct {
	InstanceName    string
	Description     string
	AgentNamePrefix string
}

// Identity is the resolved Agora identity for this process.
type Identity struct {
	InstanceName string
	Description  string
	AgentName    string
}

func resolveIdentity(cfg *Config, defaults Defaults) (Identity, error) {
	if cfg == nil {
		return Identity{}, fmt.Errorf("agora config is required")
	}

	instanceName := strings.TrimSpace(cfg.InstanceName)
	if instanceName == "" {
		instanceName = strings.TrimSpace(defaults.InstanceName)
	}
	if instanceName == "" {
		instanceName = defaultInstanceName
	}

	description := strings.TrimSpace(cfg.Description)
	if description == "" {
		description = strings.TrimSpace(defaults.Description)
	}
	if description == "" {
		description = defaultDescription
	}

	prefix := strings.TrimSpace(defaults.AgentNamePrefix)
	if prefix == "" {
		prefix = defaultAgentNamePrefix
	}

	return Identity{
		InstanceName: instanceName,
		Description:  description,
		AgentName:    prefix + "-" + instanceName,
	}, nil
}

// serveTunnelName is the single source of truth for the create-or-bind serve
// tunnel name. It is both the name Serve binds/creates and the catalog
// advertisement Name (the client's dial key), so the two can never diverge.
// it resolves before serving starts so publication and serving can share the
// same name without depending on startup order.
func serveTunnelName(cfg *Config, instanceName string) string {
	if cfg != nil && cfg.Serve != nil {
		if name := strings.TrimSpace(cfg.Serve.Tunnel); name != "" {
			return name
		}
	}
	return instanceName
}
