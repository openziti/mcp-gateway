package agora

import (
	"testing"
)

func TestResolveIdentityUsesDefaults(t *testing.T) {
	identity, err := resolveIdentity(&Config{}, Defaults{
		InstanceName:    "mcp-bridge",
		Description:     "MCP single-server bridge",
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
	if identity.AgentName != "mcp-bridge-mcp-bridge" {
		t.Fatalf("agent name = %q", identity.AgentName)
	}
}

func TestResolveIdentityConfigOverridesDefaults(t *testing.T) {
	identity, err := resolveIdentity(&Config{
		InstanceName: "engineering",
		Description:  "engineering gateway",
	}, Defaults{
		InstanceName:    "mcp-gateway",
		Description:     "MCP tool gateway",
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
	if identity.AgentName != "mcp-gateway-engineering" {
		t.Fatalf("agent name = %q", identity.AgentName)
	}
}

func TestServeTunnelNameDefaultsToInstanceName(t *testing.T) {
	if got := serveTunnelName(&Config{}, "engineering"); got != "engineering" {
		t.Fatalf("serve tunnel name = %q, want instance name", got)
	}
	if got := serveTunnelName(&Config{Serve: &ServeConfig{Enabled: true}}, "engineering"); got != "engineering" {
		t.Fatalf("serve tunnel name = %q, want instance name when serve.tunnel empty", got)
	}
}

func TestServeTunnelNameUsesConfiguredTunnel(t *testing.T) {
	cfg := &Config{Serve: &ServeConfig{Enabled: true, Tunnel: "persistent-share"}}
	if got := serveTunnelName(cfg, "engineering"); got != "persistent-share" {
		t.Fatalf("serve tunnel name = %q, want %q", got, "persistent-share")
	}
}
