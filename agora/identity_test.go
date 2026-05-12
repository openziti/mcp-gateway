package agora

import (
	"strings"
	"testing"
)

func TestResolveIdentityUsesDefaults(t *testing.T) {
	identity, err := resolveIdentity(&Config{}, Defaults{
		InstanceName:    "mcp-bridge",
		Description:     "MCP single-server bridge",
		TunnelMode:      "tcp",
		AgentNamePrefix: "mcp-bridge",
	})
	if err != nil {
		t.Fatalf("resolveIdentity returned error: %v", err)
	}

	if identity.InstanceName != "mcp-bridge" {
		t.Fatalf("instance name = %q", identity.InstanceName)
	}
	if identity.Description != "MCP single-server bridge" {
		t.Fatalf("description = %q", identity.Description)
	}
	if identity.TunnelMode != "tcp" {
		t.Fatalf("tunnel mode = %q", identity.TunnelMode)
	}
	if identity.AgentName != "mcp-bridge-mcp-bridge" {
		t.Fatalf("agent name = %q", identity.AgentName)
	}
}

func TestResolveIdentityConfigOverridesDefaults(t *testing.T) {
	identity, err := resolveIdentity(&Config{
		InstanceName: "engineering",
		Description:  "engineering gateway",
		TunnelMode:   "http",
	}, Defaults{
		InstanceName:    "mcp-gateway",
		Description:     "MCP tool gateway",
		TunnelMode:      "tcp",
		AgentNamePrefix: "mcp-gateway",
	})
	if err != nil {
		t.Fatalf("resolveIdentity returned error: %v", err)
	}

	if identity.InstanceName != "engineering" {
		t.Fatalf("instance name = %q", identity.InstanceName)
	}
	if identity.Description != "engineering gateway" {
		t.Fatalf("description = %q", identity.Description)
	}
	if identity.TunnelMode != "http" {
		t.Fatalf("tunnel mode = %q", identity.TunnelMode)
	}
	if identity.AgentName != "mcp-gateway-engineering" {
		t.Fatalf("agent name = %q", identity.AgentName)
	}
}

func TestResolveIdentityRejectsDisallowedTunnelMode(t *testing.T) {
	_, err := resolveIdentity(&Config{TunnelMode: "udp"}, Defaults{
		TunnelMode:         "tcp",
		AllowedTunnelModes: []string{"tcp", "http"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid agora tunnel_mode") {
		t.Fatalf("expected invalid tunnel mode error, got %v", err)
	}
}

func TestResolveIdentityAllowsAllSdkModesByDefault(t *testing.T) {
	identity, err := resolveIdentity(&Config{TunnelMode: "udp"}, Defaults{})
	if err != nil {
		t.Fatalf("resolveIdentity returned error: %v", err)
	}
	if identity.TunnelMode != "udp" {
		t.Fatalf("tunnel mode = %q", identity.TunnelMode)
	}
}
