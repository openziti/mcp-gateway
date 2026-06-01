package tools

import (
	"testing"

	mcpagora "github.com/openziti/mcp-gateway/agora"
)

func TestNewAgoraRequiresTunnel(t *testing.T) {
	_, err := NewAgora("", &mcpagora.Config{Enabled: true})
	if err == nil {
		t.Fatal("expected tunnel error")
	}
}

func TestNewAgoraRequiresConfig(t *testing.T) {
	_, err := NewAgora("service", nil)
	if err == nil {
		t.Fatal("expected config error")
	}
}

func TestNewAgoraEnablesConfig(t *testing.T) {
	cfg := &mcpagora.Config{}
	client, err := NewAgora(" service ", cfg)
	if err != nil {
		t.Fatalf("NewAgora returned error: %v", err)
	}
	if client.agoraTunnel != "service" {
		t.Fatalf("agora tunnel = %q", client.agoraTunnel)
	}
	if client.agoraClient == nil {
		t.Fatal("expected agora client to be constructed")
	}
	if !cfg.Enabled {
		t.Fatal("expected config to be enabled")
	}
}
