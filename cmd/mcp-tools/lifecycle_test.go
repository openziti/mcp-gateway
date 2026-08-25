package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	mcpagora "github.com/openziti/mcp-gateway/agora"
	"github.com/openziti/mcp-gateway/tools"
)

type fakeToolsClient struct {
	startErr     error
	runErr       error
	runHTTPErr   error
	stopErr      error
	startCalls   int
	runCalls     int
	runHTTPCalls int
	stopCalls    int
	httpOpts     *tools.HTTPOptions
}

func (f *fakeToolsClient) Start(context.Context) error {
	f.startCalls++
	return f.startErr
}

func (f *fakeToolsClient) Run(context.Context) error {
	f.runCalls++
	return f.runErr
}

func (f *fakeToolsClient) RunHTTP(_ context.Context, opts *tools.HTTPOptions) error {
	f.runHTTPCalls++
	f.httpOpts = opts
	return f.runHTTPErr
}

func (f *fakeToolsClient) Stop() error {
	f.stopCalls++
	return f.stopErr
}

func setToolsClientFactory(t *testing.T, client toolsClient, createErr error) *toolsTarget {
	t.Helper()

	origFactory := newToolsClient
	var gotTarget toolsTarget
	newToolsClient = func(target toolsTarget) (toolsClient, error) {
		gotTarget = target
		return client, createErr
	}

	t.Cleanup(func() {
		newToolsClient = origFactory
	})
	return &gotTarget
}

func TestRunCommandStopsClientOnRunError(t *testing.T) {
	client := &fakeToolsClient{runErr: errors.New("boom")}
	target := setToolsClientFactory(t, client, nil)

	err := newRunCommand().run(nil, []string{"share"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected run error, got %v", err)
	}
	if target.ShareToken != "share" || target.AgoraTunnel != "" {
		t.Fatalf("unexpected target: %+v", target)
	}
	if client.stopCalls != 1 {
		t.Fatalf("expected stop to be called once, got %d", client.stopCalls)
	}
}

func TestRunCommandTreatsContextCanceledAsCleanShutdown(t *testing.T) {
	client := &fakeToolsClient{runErr: context.Canceled}
	setToolsClientFactory(t, client, nil)

	err := newRunCommand().run(nil, []string{"share"})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if client.stopCalls != 1 {
		t.Fatalf("expected stop to be called once, got %d", client.stopCalls)
	}
}

func TestRunCommandReturnsStopErrorOnCleanShutdown(t *testing.T) {
	client := &fakeToolsClient{stopErr: errors.New("stop failed")}
	setToolsClientFactory(t, client, nil)

	err := newRunCommand().run(nil, []string{"share"})
	if err == nil || !strings.Contains(err.Error(), "stop failed") {
		t.Fatalf("expected stop error, got %v", err)
	}
}

func TestHTTPCommandStopsClientOnRunError(t *testing.T) {
	client := &fakeToolsClient{runHTTPErr: errors.New("listen failed")}
	target := setToolsClientFactory(t, client, nil)

	command := newHTTPCommand()
	command.bind = "127.0.0.1:9090"
	command.stateless = true
	command.jsonResponse = true

	err := command.run(nil, []string{"share"})
	if err == nil || !strings.Contains(err.Error(), "listen failed") {
		t.Fatalf("expected http run error, got %v", err)
	}
	if client.stopCalls != 1 {
		t.Fatalf("expected stop to be called once, got %d", client.stopCalls)
	}
	if target.ShareToken != "share" || target.AgoraTunnel != "" {
		t.Fatalf("unexpected target: %+v", target)
	}
	if client.httpOpts == nil || client.httpOpts.Address != "127.0.0.1:9090" || !client.httpOpts.Stateless || !client.httpOpts.JSONResponse {
		t.Fatalf("expected http options to be passed through, got %+v", client.httpOpts)
	}
}

func TestRunCommandAcceptsAgoraTarget(t *testing.T) {
	t.Setenv(agoraToolsIntegrationFileEnv, "")

	client := &fakeToolsClient{}
	target := setToolsClientFactory(t, client, nil)

	command := newRunCommand()
	command.agoraTunnel = "mcp-gateway-engineering"

	if err := command.run(nil, nil); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if target.AgoraTunnel != "mcp-gateway-engineering" || target.ShareToken != "" || target.AgoraConfig == nil || !target.AgoraConfig.Enabled {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestHTTPCommandAcceptsAgoraTarget(t *testing.T) {
	t.Setenv(agoraToolsIntegrationFileEnv, "")

	client := &fakeToolsClient{}
	target := setToolsClientFactory(t, client, nil)

	command := newHTTPCommand()
	command.agoraTunnel = "mcp-gateway-engineering"

	if err := command.run(nil, nil); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if target.AgoraTunnel != "mcp-gateway-engineering" || target.ShareToken != "" || target.AgoraConfig == nil || !target.AgoraConfig.Enabled {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestResolveToolsTargetRejectsShareAndAgora(t *testing.T) {
	_, err := resolveToolsTarget([]string{"share"}, "mcp-gateway-engineering", "")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual exclusion error, got %v", err)
	}
}

func TestResolveToolsTargetRequiresShareOrAgora(t *testing.T) {
	_, err := resolveToolsTarget(nil, "", "")
	if err == nil || !strings.Contains(err.Error(), "share token is required") {
		t.Fatalf("expected missing target error, got %v", err)
	}
}

func TestResolveToolsTargetAgoraIntegrationFilePrecedence(t *testing.T) {
	t.Setenv(agoraToolsIntegrationFileEnv, "/env/agora.yaml")

	origResolve := resolveAgoraConfig
	resolveAgoraConfig = func(cfg *mcpagora.Config) error {
		return nil
	}
	t.Cleanup(func() {
		resolveAgoraConfig = origResolve
	})

	target, err := resolveToolsTarget(nil, "service", "")
	if err != nil {
		t.Fatalf("resolveToolsTarget returned error: %v", err)
	}
	if target.AgoraConfig == nil || target.AgoraConfig.IntegrationFile != "/env/agora.yaml" {
		t.Fatalf("env integration file was not applied: %+v", target.AgoraConfig)
	}

	target, err = resolveToolsTarget(nil, "service", "/flag/agora.yaml")
	if err != nil {
		t.Fatalf("resolveToolsTarget returned error: %v", err)
	}
	if target.AgoraConfig == nil || target.AgoraConfig.IntegrationFile != "/flag/agora.yaml" {
		t.Fatalf("flag integration file did not win: %+v", target.AgoraConfig)
	}
}

func TestHTTPCommandRejectsNegativeSessionIdleTimeout(t *testing.T) {
	client := &fakeToolsClient{}
	setToolsClientFactory(t, client, nil)

	command := newHTTPCommand()
	command.sessionIdleTimeout = -time.Second

	err := command.run(nil, []string{"share"})
	if err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("expected negative timeout error, got %v", err)
	}
	if client.startCalls != 0 {
		t.Fatalf("expected no client to be started, got %d start calls", client.startCalls)
	}
}
