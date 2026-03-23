package main

import (
	"context"
	"errors"
	"strings"
	"testing"

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
	origEnv, origWorkingDir, origShareToken := env, workingDir, shareToken
	defer func() {
		newBridgeRunner = origFactory
		env = origEnv
		workingDir = origWorkingDir
		shareToken = origShareToken
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
