package bridge

import (
	"strings"
	"testing"

	mcpagora "github.com/openziti/mcp-gateway/agora"
)

func TestValidateDefaultsToZrokShare(t *testing.T) {
	cfg := &Config{Command: "backend"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if !cfg.ZrokShareEnabled() {
		t.Fatal("expected zrok share to default enabled")
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
