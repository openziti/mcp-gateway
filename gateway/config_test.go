package gateway

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/openziti/mcp-gateway/aggregator"
	mcpagora "github.com/openziti/mcp-gateway/agora"
)

func TestDefaultConfigEnablesZrokShare(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.ZrokShareEnabled() {
		t.Fatalf("expected zrok share to default enabled: %#v", cfg.Zrok)
	}
}

func TestValidateRejectsNoEnabledListener(t *testing.T) {
	cfg := validTestConfig()
	cfg.Zrok.Share.Enabled = false

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "at least one of zrok.share.enabled or agora.serve.enabled") {
		t.Fatalf("expected listener validation error, got %v", err)
	}
}

func TestValidateAllowsAgoraServeWithoutZrok(t *testing.T) {
	cfg := validTestConfig()
	cfg.Zrok.Share.Enabled = false
	cfg.Agora = &mcpagora.Config{
		Enabled: true,
		Serve:   &mcpagora.ServeConfig{Enabled: true},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestValidateRejectsShareTokenWhenZrokDisabled(t *testing.T) {
	cfg := validTestConfig()
	cfg.Zrok.Share.Enabled = false
	cfg.ShareToken = "managed-share"
	cfg.Agora = &mcpagora.Config{
		Enabled: true,
		Serve:   &mcpagora.ServeConfig{Enabled: true},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "share_token requires zrok.share.enabled") {
		t.Fatalf("expected share token validation error, got %v", err)
	}
}

func TestValidateRejectsAgoraBackendWhenAgoraDisabled(t *testing.T) {
	cfg := validTestConfig()
	cfg.Backends = []aggregator.BackendConfig{{
		ID: "remote",
		Transport: aggregator.TransportConfig{
			Type:        "agora",
			AgoraTunnel: "filesystem-relay",
		},
	}}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "agora transport backends require agora.enabled") {
		t.Fatalf("expected agora enabled validation error, got %v", err)
	}
}

func TestValidateAcceptsAgoraBackendWhenAgoraEnabled(t *testing.T) {
	cfg := validTestConfig()
	cfg.Agora = &mcpagora.Config{Enabled: true}
	cfg.Backends = []aggregator.BackendConfig{{
		ID: "remote",
		Transport: aggregator.TransportConfig{
			Type:        "agora",
			AgoraTunnel: "filesystem-relay",
		},
	}}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestDemoMCPGatewayConfigEnablesAgoraPublishing(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "etc", "demo-mcp-gateway.yml"))
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.ZrokShareEnabled() {
		t.Fatalf("demo config should disable zrok sharing: %#v", cfg.Zrok)
	}
	if !cfg.AgoraServeEnabled() {
		t.Fatalf("demo config should enable agora serving: %#v", cfg.Agora)
	}
	if cfg.Agora == nil || cfg.Agora.Advertisement == nil || cfg.Agora.Advertisement.Publish == nil || !*cfg.Agora.Advertisement.Publish {
		t.Fatalf("demo config must explicitly enable agora advertisement publishing: %#v", cfg.Agora)
	}
	if len(cfg.Backends) != 1 || cfg.Backends[0].ID != "filesystem" {
		t.Fatalf("unexpected demo backends: %#v", cfg.Backends)
	}
	if cfg.Backends[0].Transport.Type != "stdio" || cfg.Backends[0].Transport.Command != "mcp-filesystem" {
		t.Fatalf("unexpected demo backend transport: %#v", cfg.Backends[0].Transport)
	}
}

func validTestConfig() *Config {
	cfg := DefaultConfig()
	cfg.Backends = []aggregator.BackendConfig{{
		ID: "filesystem",
		Transport: aggregator.TransportConfig{
			Type:    "stdio",
			Command: "mcp-server-filesystem",
		},
	}}
	return cfg
}
