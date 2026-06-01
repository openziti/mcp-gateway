package agora

import (
	"fmt"
	"strings"
)

const (
	defaultInstanceName    = "mcp-gateway"
	defaultDescription     = "MCP gateway"
	defaultTunnelMode      = "tcp"
	defaultAgentNamePrefix = "mcp-gateway"
	defaultAllowedModeTCP  = "tcp"
	defaultAllowedModeHTTP = "http"
	defaultAllowedModeUDP  = "udp"
)

// Defaults supplies per-binary identity defaults.
type Defaults struct {
	InstanceName       string
	Description        string
	TunnelMode         string
	AgentNamePrefix    string
	AllowedTunnelModes []string
}

// Identity is the resolved Agora identity for this process.
type Identity struct {
	InstanceName string
	Description  string
	TunnelMode   string
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

	tunnelMode := strings.ToLower(strings.TrimSpace(cfg.TunnelMode))
	if tunnelMode == "" {
		tunnelMode = strings.ToLower(strings.TrimSpace(defaults.TunnelMode))
	}
	if tunnelMode == "" {
		tunnelMode = defaultTunnelMode
	}
	if !modeAllowed(tunnelMode, defaults.AllowedTunnelModes) {
		return Identity{}, fmt.Errorf("invalid agora tunnel_mode '%s'", cfg.TunnelMode)
	}

	prefix := strings.TrimSpace(defaults.AgentNamePrefix)
	if prefix == "" {
		prefix = defaultAgentNamePrefix
	}

	return Identity{
		InstanceName: instanceName,
		Description:  description,
		TunnelMode:   tunnelMode,
		AgentName:    prefix + "-" + instanceName,
	}, nil
}

func modeAllowed(mode string, allowed []string) bool {
	if len(allowed) == 0 {
		allowed = []string{defaultAllowedModeTCP, defaultAllowedModeHTTP, defaultAllowedModeUDP}
	}
	for _, candidate := range allowed {
		if mode == strings.ToLower(strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}
