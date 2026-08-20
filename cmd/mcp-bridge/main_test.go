package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	mcpagora "github.com/openziti/mcp-gateway/agora"
	"github.com/openziti/mcp-gateway/bridge"
)

type fakeBridgeRunner struct {
	startErr   error
	runErr     error
	stopErr    error
	startCalls int
	runCalls   int
	stopCalls  int
}

func (f *fakeBridgeRunner) Start(context.Context) error {
	f.startCalls++
	return f.startErr
}

func (f *fakeBridgeRunner) Run(context.Context) error {
	f.runCalls++
	return f.runErr
}

func (f *fakeBridgeRunner) Stop() error {
	f.stopCalls++
	return f.stopErr
}

func TestRunStopsBridgeOnRunError(t *testing.T) {
	origFactory := newBridgeRunner
	origEnv, origWorkingDir, origShareToken, origAccessGrants := env, workingDir, shareToken, accessGrants
	origNetwork, origIntegrationFile := network, agoraIntegrationFile
	defer func() {
		newBridgeRunner = origFactory
		env = origEnv
		workingDir = origWorkingDir
		shareToken = origShareToken
		accessGrants = origAccessGrants
		network = origNetwork
		agoraIntegrationFile = origIntegrationFile
	}()

	var gotCfg *bridge.Config
	fake := &fakeBridgeRunner{runErr: errors.New("serve failed")}
	newBridgeRunner = func(cfg *bridge.Config) (bridgeRunner, error) {
		gotCfg = cfg
		return fake, nil
	}

	env = []string{"FOO=bar"}
	workingDir = "/tmp/work"
	shareToken = "managed-share"
	network = ""
	agoraIntegrationFile = ""

	err := run(nil, []string{"backend", "arg1"})
	if err == nil || !strings.Contains(err.Error(), "serve failed") {
		t.Fatalf("expected run error, got %v", err)
	}
	if fake.stopCalls != 1 {
		t.Fatalf("expected stop to be called once, got %d", fake.stopCalls)
	}
	if gotCfg == nil || gotCfg.Command != "backend" || len(gotCfg.Args) != 1 || gotCfg.Args[0] != "arg1" {
		t.Fatalf("unexpected config: %+v", gotCfg)
	}
	if gotCfg.Env["FOO"] != "bar" || gotCfg.WorkingDir != "/tmp/work" || gotCfg.ShareToken != "managed-share" {
		t.Fatalf("unexpected config fields: %+v", gotCfg)
	}
}

func TestRunReturnsStopErrorOnCleanShutdown(t *testing.T) {
	origFactory := newBridgeRunner
	defer func() {
		newBridgeRunner = origFactory
	}()

	fake := &fakeBridgeRunner{stopErr: errors.New("stop failed")}
	newBridgeRunner = func(*bridge.Config) (bridgeRunner, error) {
		return fake, nil
	}

	err := run(nil, []string{"backend"})
	if err == nil || !strings.Contains(err.Error(), "stop failed") {
		t.Fatalf("expected stop error, got %v", err)
	}
}

func TestApplyOverridesRejectsInvalidNetwork(t *testing.T) {
	withBridgeGlobals(t, func() {
		network = "invalid"
		if err := applyOverrides(&bridge.Config{Command: "backend"}); err == nil {
			t.Fatal("expected invalid network error")
		}
	})
}

func TestApplyOverridesAgoraNetwork(t *testing.T) {
	withBridgeGlobals(t, func() {
		network = "agora"
		cfg := &bridge.Config{Command: "backend"}

		if err := applyOverrides(cfg); err != nil {
			t.Fatalf("applyOverrides returned error: %v", err)
		}
		if cfg.Agora == nil || !cfg.Agora.Enabled || cfg.Agora.Serve == nil || !cfg.Agora.Serve.Enabled {
			t.Fatalf("agora serve was not enabled: %#v", cfg.Agora)
		}
		// publish is left at its default (nil), not forced on, so enrolled
		// accounts without workgroup IDs can serve without publishing.
		if cfg.Agora.Advertisement != nil {
			t.Fatalf("advertisement config should be left untouched: %#v", cfg.Agora.Advertisement)
		}
		if cfg.Zrok == nil || cfg.Zrok.Share == nil || cfg.Zrok.Share.Enabled {
			t.Fatalf("zrok share was not disabled: %#v", cfg.Zrok)
		}
	})
}

func TestApplyOverridesAgoraTunnelFlag(t *testing.T) {
	withBridgeGlobals(t, func() {
		network = "agora"
		agoraTunnel = " lore "
		cfg := &bridge.Config{Command: "backend"}

		if err := applyOverrides(cfg); err != nil {
			t.Fatalf("applyOverrides returned error: %v", err)
		}
		if cfg.Agora == nil || cfg.Agora.Serve == nil || cfg.Agora.Serve.Tunnel != "lore" {
			t.Fatalf("agora tunnel flag was not applied: %#v", cfg.Agora)
		}
		if !cfg.Agora.Serve.Enabled {
			t.Fatalf("agora serve should remain enabled: %#v", cfg.Agora.Serve)
		}
	})
}

func TestApplyOverridesAccessGrantsNilSafely(t *testing.T) {
	withBridgeGlobals(t, func() {
		accessGrants = []string{"one@example.com", "two@example.com"}
		cfg := &bridge.Config{Command: "backend"}

		if err := applyOverrides(cfg); err != nil {
			t.Fatalf("applyOverrides returned error: %v", err)
		}
		got := cfg.ZrokShareAccessGrants()
		if len(got) != 2 || got[0] != "one@example.com" || got[1] != "two@example.com" {
			t.Fatalf("access grants = %#v, want both flag values", got)
		}
		if !cfg.ZrokShareEnabled() {
			t.Fatal("access-grant flag must preserve default zrok sharing")
		}
	})
}

func TestApplyOverridesDefaultsToOwnerOnly(t *testing.T) {
	withBridgeGlobals(t, func() {
		cfg := &bridge.Config{Command: "backend"}

		if err := applyOverrides(cfg); err != nil {
			t.Fatalf("applyOverrides returned error: %v", err)
		}
		if got := cfg.ZrokShareAccessGrants(); len(got) != 0 {
			t.Fatalf("access grants = %#v, want owner-only", got)
		}
	})
}

func TestApplyOverridesAgoraIntegrationFilePrecedence(t *testing.T) {
	withBridgeGlobals(t, func() {
		t.Setenv("AGORA_MCP_BRIDGE_INTEGRATION_FILE", "/env/agora.yaml")
		cfg := &bridge.Config{Command: "backend"}

		if err := applyOverrides(cfg); err != nil {
			t.Fatalf("applyOverrides returned error: %v", err)
		}
		if cfg.Agora == nil || cfg.Agora.IntegrationFile != "/env/agora.yaml" {
			t.Fatalf("env integration file was not applied: %#v", cfg.Agora)
		}

		cfg = &bridge.Config{Command: "backend"}
		agoraIntegrationFile = "/flag/agora.yaml"
		if err := applyOverrides(cfg); err != nil {
			t.Fatalf("applyOverrides returned error: %v", err)
		}
		if cfg.Agora == nil || cfg.Agora.IntegrationFile != "/flag/agora.yaml" {
			t.Fatalf("flag integration file did not win: %#v", cfg.Agora)
		}
	})
}

func TestRunResolvesAndValidatesAfterOverrides(t *testing.T) {
	origFactory := newBridgeRunner
	defer func() {
		newBridgeRunner = origFactory
	}()

	withBridgeGlobals(t, func() {
		network = "agora"
		fake := &fakeBridgeRunner{}
		newBridgeRunner = func(cfg *bridge.Config) (bridgeRunner, error) {
			if cfg.Agora == nil || !cfg.Agora.Enabled || cfg.Agora.Serve == nil || !cfg.Agora.Serve.Enabled {
				t.Fatalf("agora override was not applied before validation: %#v", cfg.Agora)
			}
			if cfg.ZrokShareEnabled() {
				t.Fatalf("zrok share should be disabled in agora mode: %#v", cfg.Zrok)
			}
			return fake, nil
		}

		if err := run(nil, []string{"backend"}); err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	})
}

func TestRunPropagatesResolveConfigError(t *testing.T) {
	withBridgeGlobals(t, func() {
		network = "agora"
		agoraIntegrationFile = "/missing/agora.yaml"
		err := run(nil, []string{"backend"})
		if err == nil || !strings.Contains(err.Error(), "load agora integration file") {
			t.Fatalf("expected integration file error, got %v", err)
		}
	})
}

func TestApplyOverridesPreservesExistingAgoraFields(t *testing.T) {
	withBridgeGlobals(t, func() {
		network = "agora"
		publish := false
		cfg := &bridge.Config{
			Command: "backend",
			Agora: &mcpagora.Config{
				APIEndpoint: "http://controller.example",
				Advertisement: &mcpagora.AdvertisementConfig{
					Publish: &publish,
				},
			},
		}

		if err := applyOverrides(cfg); err != nil {
			t.Fatalf("applyOverrides returned error: %v", err)
		}
		if cfg.Agora.APIEndpoint != "http://controller.example" {
			t.Fatalf("api endpoint was overwritten: %q", cfg.Agora.APIEndpoint)
		}
		if cfg.Agora.Advertisement.Publish == nil || *cfg.Agora.Advertisement.Publish {
			t.Fatalf("network=agora must preserve an explicit publish=false: %#v", cfg.Agora.Advertisement.Publish)
		}
	})
}

func withBridgeGlobals(t *testing.T, fn func()) {
	t.Helper()
	origEnv, origWorkingDir, origShareToken, origAccessGrants := env, workingDir, shareToken, accessGrants
	origNetwork, origTunnel, origIntegrationFile := network, agoraTunnel, agoraIntegrationFile
	defer func() {
		env = origEnv
		workingDir = origWorkingDir
		shareToken = origShareToken
		accessGrants = origAccessGrants
		network = origNetwork
		agoraTunnel = origTunnel
		agoraIntegrationFile = origIntegrationFile
	}()

	env = nil
	workingDir = ""
	shareToken = ""
	accessGrants = nil
	network = ""
	agoraTunnel = ""
	agoraIntegrationFile = ""
	fn()
}
