package gateway

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestDefaultConfigEnablesResilience(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Resilience.WatchdogEnabled {
		t.Fatalf("watchdog should default enabled: %#v", cfg.Resilience)
	}
	if cfg.Resilience.PollInterval != 10*time.Second ||
		cfg.Resilience.ZeroEstablishedGrace != 90*time.Second ||
		cfg.Resilience.MaxRebuildFailures != 5 ||
		cfg.Resilience.HeartbeatInterval != 5*time.Minute {
		t.Fatalf("unexpected resilience defaults: %#v", cfg.Resilience)
	}
}

func TestValidateRejectsInvalidEnabledResilience(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{
			name: "poll interval",
			mutate: func(cfg *Config) {
				cfg.Resilience.PollInterval = 0
			},
			field: "resilience.poll_interval",
		},
		{
			name: "zero established grace",
			mutate: func(cfg *Config) {
				cfg.Resilience.ZeroEstablishedGrace = 0
			},
			field: "resilience.zero_established_grace",
		},
		{
			name: "max rebuild failures",
			mutate: func(cfg *Config) {
				cfg.Resilience.MaxRebuildFailures = 0
			},
			field: "resilience.max_rebuild_failures",
		},
		{
			name: "negative heartbeat",
			mutate: func(cfg *Config) {
				cfg.Resilience.HeartbeatInterval = -time.Second
			},
			field: "resilience.heartbeat_interval",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validTestConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("expected %s validation error, got %v", tt.field, err)
			}
		})
	}
}

func TestValidateAllowsDisabledWatchdogAndZeroHeartbeat(t *testing.T) {
	cfg := validTestConfig()
	cfg.Resilience.WatchdogEnabled = false
	cfg.Resilience.PollInterval = 0
	cfg.Resilience.ZeroEstablishedGrace = 0
	cfg.Resilience.MaxRebuildFailures = 0
	cfg.Resilience.HeartbeatInterval = 0

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected disabled watchdog config to validate, got %v", err)
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
