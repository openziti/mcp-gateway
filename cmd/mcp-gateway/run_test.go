package main

import (
	"context"
	"errors"
	"strings"
	"testing"

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

	cfg := &gateway.Config{}
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
		return &gateway.Config{}, nil
	}
	newGatewayRunner = func(*gateway.Config) (gatewayRunner, error) {
		return fake, nil
	}

	err := newRunCommand().run(nil, []string{"config.yml"})
	if err == nil || !strings.Contains(err.Error(), "stop failed") {
		t.Fatalf("expected stop error, got %v", err)
	}
}
