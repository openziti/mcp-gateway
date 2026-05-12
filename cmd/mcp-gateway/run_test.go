package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openziti/mcp-gateway/aggregator"
	mcpagora "github.com/openziti/mcp-gateway/agora"
	"github.com/openziti/mcp-gateway/gateway"
)

type fakeGatewayRunner struct {
	startErr   error
	runErr     error
	stopErr    error
	startCalls int
	runCalls   int
	stopCalls  int
}

func (f *fakeGatewayRunner) Start(context.Context) error {
	f.startCalls++
	return f.startErr
}

func (f *fakeGatewayRunner) Run(context.Context) error {
	f.runCalls++
	return f.runErr
}

func (f *fakeGatewayRunner) Stop() error {
	f.stopCalls++
	return f.stopErr
}

func TestRunStopsGatewayOnRunError(t *testing.T) {
	origLoad := loadGatewayConfig
	origFactory := newGatewayRunner
	defer func() {
		loadGatewayConfig = origLoad
		newGatewayRunner = origFactory
	}()

	cfg := validGatewayConfig()
	fake := &fakeGatewayRunner{runErr: errors.New("serve failed")}
	loadGatewayConfig = func(path string) (*gateway.Config, error) {
		if path != "config.yml" {
			t.Fatalf("unexpected config path %q", path)
		}
		return cfg, nil
	}
	newGatewayRunner = func(gotCfg *gateway.Config) (gatewayRunner, error) {
		if gotCfg != cfg {
			t.Fatalf("expected config pointer to be reused")
		}
		return fake, nil
	}

	err := newRunCommand().run(nil, []string{"config.yml"})
	if err == nil || !strings.Contains(err.Error(), "serve failed") {
		t.Fatalf("expected run error, got %v", err)
	}
	if fake.stopCalls != 1 {
		t.Fatalf("expected stop to be called once, got %d", fake.stopCalls)
	}
}

func TestRunReturnsStopErrorOnCleanShutdown(t *testing.T) {
	origLoad := loadGatewayConfig
	origFactory := newGatewayRunner
	defer func() {
		loadGatewayConfig = origLoad
		newGatewayRunner = origFactory
	}()

	fake := &fakeGatewayRunner{stopErr: errors.New("stop failed")}
	loadGatewayConfig = func(string) (*gateway.Config, error) {
		return validGatewayConfig(), nil
	}
	newGatewayRunner = func(*gateway.Config) (gatewayRunner, error) {
		return fake, nil
	}

	err := newRunCommand().run(nil, []string{"config.yml"})
	if err == nil || !strings.Contains(err.Error(), "stop failed") {
		t.Fatalf("expected stop error, got %v", err)
	}
}

func TestRunCommandApplyOverridesRejectsInvalidNetwork(t *testing.T) {
	command := &runCommand{network: "invalid"}
	if err := command.applyOverrides(validGatewayConfig()); err == nil {
		t.Fatal("expected invalid network error")
	}
}

func TestRunCommandApplyOverridesEnablesAgoraNetwork(t *testing.T) {
	cfg := validGatewayConfig()
	command := &runCommand{network: "agora"}

	if err := command.applyOverrides(cfg); err != nil {
		t.Fatalf("applyOverrides returned error: %v", err)
	}
	if cfg.Agora == nil || !cfg.Agora.Enabled {
		t.Fatalf("agora was not enabled: %#v", cfg.Agora)
	}
}

func TestRunCommandApplyOverridesAgoraIntegrationFilePrecedence(t *testing.T) {
	t.Setenv("AGORA_MCP_GATEWAY_INTEGRATION_FILE", "/env/agora.yaml")

	cfg := validGatewayConfig()
	if err := (&runCommand{}).applyOverrides(cfg); err != nil {
		t.Fatalf("applyOverrides returned error: %v", err)
	}
	if cfg.Agora == nil || cfg.Agora.IntegrationFile != "/env/agora.yaml" {
		t.Fatalf("env integration file was not applied: %#v", cfg.Agora)
	}

	cfg = validGatewayConfig()
	command := &runCommand{agoraIntegrationFile: "/flag/agora.yaml"}
	if err := command.applyOverrides(cfg); err != nil {
		t.Fatalf("applyOverrides returned error: %v", err)
	}
	if cfg.Agora == nil || cfg.Agora.IntegrationFile != "/flag/agora.yaml" {
		t.Fatalf("flag integration file did not win: %#v", cfg.Agora)
	}
}

func TestRunResolvesAndValidatesAfterOverrides(t *testing.T) {
	origLoad := loadGatewayConfig
	origFactory := newGatewayRunner
	defer func() {
		loadGatewayConfig = origLoad
		newGatewayRunner = origFactory
	}()

	cfg := validGatewayConfig()
	cfg.Zrok.Share.Enabled = false
	cfg.Agora = &mcpagora.Config{
		Serve: &mcpagora.ServeConfig{Enabled: true},
	}
	fake := &fakeGatewayRunner{}

	loadGatewayConfig = func(string) (*gateway.Config, error) {
		return cfg, nil
	}
	newGatewayRunner = func(gotCfg *gateway.Config) (gatewayRunner, error) {
		if gotCfg.Agora == nil || !gotCfg.Agora.Enabled {
			t.Fatalf("agora override was not applied before validation: %#v", gotCfg.Agora)
		}
		return fake, nil
	}

	command := newRunCommand()
	command.network = "agora"
	if err := command.run(nil, []string{"config.yml"}); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
}

func validGatewayConfig() *gateway.Config {
	cfg := gateway.DefaultConfig()
	cfg.Backends = []aggregator.BackendConfig{{
		ID: "filesystem",
		Transport: aggregator.TransportConfig{
			Type:    "stdio",
			Command: "mcp-server-filesystem",
		},
	}}
	return cfg
}
