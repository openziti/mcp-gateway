package main

import (
	"context"
	"errors"
	"strings"
	"testing"

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

func setToolsClientFactory(t *testing.T, client toolsClient, createErr error) {
	t.Helper()

	origFactory := newToolsClient
	newToolsClient = func(string) (toolsClient, error) {
		return client, createErr
	}

	t.Cleanup(func() {
		newToolsClient = origFactory
	})
}

func TestRunCommandStopsClientOnRunError(t *testing.T) {
	client := &fakeToolsClient{runErr: errors.New("boom")}
	setToolsClientFactory(t, client, nil)

	err := newRunCommand().run(nil, []string{"share"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected run error, got %v", err)
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
	setToolsClientFactory(t, client, nil)

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
	if client.httpOpts == nil || client.httpOpts.Address != "127.0.0.1:9090" || !client.httpOpts.Stateless || !client.httpOpts.JSONResponse {
		t.Fatalf("expected http options to be passed through, got %+v", client.httpOpts)
	}
}
