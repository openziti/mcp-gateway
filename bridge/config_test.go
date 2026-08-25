package bridge

import (
	"strings"
	"testing"
	"time"

	mcpagora "github.com/openziti/mcp-gateway/agora"
	"github.com/openziti/mcp-gateway/gateway"
)

func TestValidateDefaultsToZrokShare(t *testing.T) {
	cfg := &Config{Command: "backend"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !cfg.ZrokShareEnabled() {
		t.Fatal("expected zrok share to default enabled")
	}
	if got := cfg.EffectiveSessionIdleTimeout(); got != gateway.DefaultSessionIdleTimeout {
		t.Fatalf("session idle timeout = %s, want %s", got, gateway.DefaultSessionIdleTimeout)
	}
}

func TestValidateSessionIdleTimeout(t *testing.T) {
	override := 45 * time.Minute
	cfg := &Config{Command: "backend", SessionIdleTimeout: &override}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.EffectiveSessionIdleTimeout(); got != 45*time.Minute {
		t.Fatalf("session idle timeout = %s, want 45m", got)
	}

	disabled := time.Duration(0)
	cfg.SessionIdleTimeout = &disabled
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit zero timeout was rejected: %v", err)
	}
	if got := cfg.EffectiveSessionIdleTimeout(); got != 0 {
		t.Fatalf("explicit zero timeout = %s, want disabled", got)
	}

	negative := -time.Second
	cfg.SessionIdleTimeout = &negative
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "session idle timeout") {
		t.Fatalf("expected negative timeout validation error, got %v", err)
	}
}

func TestZrokShareAccessGrants(t *testing.T) {
	if got := (*Config)(nil).ZrokShareAccessGrants(); len(got) != 0 {
		t.Fatalf("nil config grants = %#v, want owner-only", got)
	}

	cfg := &Config{Zrok: &ZrokConfig{Share: &ZrokShareConfig{
		AccessGrants: []string{"other@example.com"},
	}}}
	got := cfg.ZrokShareAccessGrants()
	if len(got) != 1 || got[0] != "other@example.com" {
		t.Fatalf("grants = %#v, want configured account", got)
	}
}

func TestValidateAcceptsAccessGrantsForNewZrokShare(t *testing.T) {
	cfg := &Config{
		Command: "backend",
		Zrok: &ZrokConfig{Share: &ZrokShareConfig{
			Enabled:      true,
			AccessGrants: []string{"other@example.com"},
		}},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestValidateRejectsAccessGrantsWithShareToken(t *testing.T) {
	cfg := &Config{
		Command:    "backend",
		ShareToken: "managed-share",
		Zrok: &ZrokConfig{Share: &ZrokShareConfig{
			Enabled:      true,
			AccessGrants: []string{"other@example.com"},
		}},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "access_grants apply only to newly created shares") {
		t.Fatalf("expected access-grant validation error, got %v", err)
	}
}

func TestValidateRejectsAccessGrantsWhenZrokDisabled(t *testing.T) {
	cfg := &Config{
		Command: "backend",
		Zrok: &ZrokConfig{Share: &ZrokShareConfig{
			AccessGrants: []string{"other@example.com"},
		}},
		Agora: &mcpagora.Config{
			Enabled: true,
			Serve:   &mcpagora.ServeConfig{Enabled: true},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "access_grants require zrok share serving") {
		t.Fatalf("expected access-grant validation error, got %v", err)
	}
}

func TestValidateRejectsDualTransport(t *testing.T) {
	cfg := &Config{
		Command: "backend",
		Agora: &mcpagora.Config{
			Enabled: true,
			Serve:   &mcpagora.ServeConfig{Enabled: true},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "one network per invocation") {
		t.Fatalf("expected dual transport error, got %v", err)
	}
}

func TestValidateAcceptsAgoraOnly(t *testing.T) {
	cfg := &Config{
		Command: "backend",
		Zrok:    &ZrokConfig{Share: &ZrokShareConfig{Enabled: false}},
		Agora: &mcpagora.Config{
			Enabled: true,
			Serve:   &mcpagora.ServeConfig{Enabled: true},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestValidateRejectsShareTokenWhenZrokDisabled(t *testing.T) {
	cfg := &Config{
		Command:    "backend",
		ShareToken: "managed-share",
		Zrok:       &ZrokConfig{Share: &ZrokShareConfig{Enabled: false}},
		Agora: &mcpagora.Config{
			Enabled: true,
			Serve:   &mcpagora.ServeConfig{Enabled: true},
		},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "share_token requires zrok.share.enabled") {
		t.Fatalf("expected share token error, got %v", err)
	}
}

func TestValidateRejectsPublishWithoutServe(t *testing.T) {
	cfg := &Config{
		Command: "backend",
		Agora:   &mcpagora.Config{Enabled: true},
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "advertisement.publish requires agora.serve.enabled") {
		t.Fatalf("expected publish without serve error, got %v", err)
	}
}
