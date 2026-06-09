package agora

import "testing"

func TestNewClientRequiresEnabledConfig(t *testing.T) {
	if _, err := NewClient(ClientOptions{}); err == nil {
		t.Fatal("expected nil config error")
	}
	if _, err := NewClient(ClientOptions{Config: &Config{}}); err == nil {
		t.Fatal("expected disabled config error")
	}
}

func TestNewClientAppliesDialDefaults(t *testing.T) {
	client, err := NewClient(ClientOptions{Config: &Config{Enabled: true}})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if client.opts.Defaults.InstanceName != "mcp-tools" {
		t.Fatalf("InstanceName = %q", client.opts.Defaults.InstanceName)
	}
	if client.opts.Defaults.Description != "MCP tools client" {
		t.Fatalf("Description = %q", client.opts.Defaults.Description)
	}
	if client.opts.Defaults.AgentNamePrefix != "mcp-tools" {
		t.Fatalf("AgentNamePrefix = %q", client.opts.Defaults.AgentNamePrefix)
	}
}

func TestClientCloseAllowsNilAndUnused(t *testing.T) {
	var client *Client
	if err := client.Close(); err != nil {
		t.Fatalf("nil Close returned error: %v", err)
	}

	client, err := NewClient(ClientOptions{Config: &Config{Enabled: true}})
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("unused Close returned error: %v", err)
	}
}
