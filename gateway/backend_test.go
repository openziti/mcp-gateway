package gateway

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openziti/mcp-gateway/aggregator"
	mcpagora "github.com/openziti/mcp-gateway/agora"
)

const localListenerHelperEnv = "MCP_GATEWAY_LOCAL_LISTENER_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(localListenerHelperEnv) != "" {
		server := mcp.NewServer(&mcp.Implementation{Name: "local-listener-helper", Version: "1.0.0"}, nil)
		server.AddTool(&mcp.Tool{Name: "ping", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			workingDir, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: workingDir}}}, nil
		})
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestNewWithListenerRequiresListener(t *testing.T) {
	if _, err := NewWithListener(&Config{}, nil); err == nil {
		t.Fatal("expected nil listener to be rejected")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	backend, err := NewWithListener(&Config{}, listener)
	if err != nil {
		t.Fatal(err)
	}
	if backend.localListener != listener {
		t.Fatal("caller-provided listener was not retained")
	}
	if err := backend.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestStartValidatesCallerProvidedListenerConfig(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	backend, err := NewWithListener(&Config{}, listener)
	if err != nil {
		t.Fatal(err)
	}
	err = backend.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "at least one backend is required") {
		t.Fatalf("invalid explicit config error = %v", err)
	}
}

func TestCallerProvidedListenerServesStreamableHTTP(t *testing.T) {
	startupRoot := t.TempDir()
	laterRoot := t.TempDir()
	rootLink := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(startupRoot, rootLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	inside := filepath.Join(startupRoot, "inside.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	aggregatorDefaults := aggregator.DefaultConfig()
	config := &Config{
		Aggregator: aggregatorDefaults.Aggregator,
		Backends: []aggregator.BackendConfig{{
			ID: "helper",
			Transport: aggregator.TransportConfig{
				Type: "stdio", Command: os.Args[0], Env: map[string]string{localListenerHelperEnv: "1"}, WorkingDir: rootLink,
			},
			Tools: aggregator.ToolFilterConfig{Mode: "allow", List: []string{"*"}},
			Policy: aggregator.PolicyConfig{Paths: []aggregator.PathPolicyConfig{{
				Tool: "ping", Argument: "path", Roots: []string{rootLink},
			}}},
		}},
		Zrok: &ZrokConfig{Share: &ZrokShareConfig{Enabled: false}},
	}
	backend, err := NewWithListener(config, listener)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := backend.Start(ctx); err != nil {
		t.Fatal(err)
	}
	startupPolicy := backend.sessionFactory.policies["helper"]
	if startupPolicy == nil {
		t.Fatal("startup policy is missing")
	}
	if err := os.Remove(rootLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(laterRoot, rootLink); err != nil {
		t.Fatal(err)
	}
	if err := startupPolicy.Enforce("ping", map[string]any{"path": inside}); err != nil {
		t.Fatalf("startup-resolved root changed: %v", err)
	}
	if err := startupPolicy.Enforce("ping", map[string]any{"path": laterRoot}); err == nil {
		t.Fatal("policy followed a root symlink changed after startup")
	}
	runDone := make(chan error, 1)
	go func() { runDone <- backend.Run(ctx) }()
	endpoint := "http://" + listener.Addr().String()

	legacyClient := mcp.NewClient(&mcp.Implementation{Name: "legacy-test", Version: "1.0.0"}, nil)
	legacyCtx, legacyCancel := context.WithTimeout(ctx, time.Second)
	_, err = legacyClient.Connect(legacyCtx, &mcp.SSEClientTransport{Endpoint: endpoint + "/sse"}, nil)
	legacyCancel()
	if err == nil {
		t.Fatal("gateway unexpectedly retained its legacy SSE surface")
	}
	waitForNoGatewaySessions(t, backend)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	waitForNoGatewaySessions(t, backend)

	client := mcp.NewClient(&mcp.Implementation{Name: "listener-test", Version: "1.0.0"}, nil)
	connectCtx, connectCancel := context.WithTimeout(ctx, 5*time.Second)
	defer connectCancel()
	session, err := client.Connect(connectCtx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "helper_ping" {
		t.Fatalf("tools = %#v", tools.Tools)
	}
	backend.sessionFactory.mu.Lock()
	for _, clientSession := range backend.sessionFactory.sessions {
		if clientSession.backends["helper"].policy != startupPolicy {
			backend.sessionFactory.mu.Unlock()
			t.Fatal("client session did not reuse the startup-resolved policy")
		}
	}
	backend.sessionFactory.mu.Unlock()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "helper_ping", Arguments: map[string]any{"path": inside}})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != startupRoot {
		t.Fatalf("per-session backend working directory = %#v, want %q", result.Content, startupRoot)
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- backend.Stop() }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gateway stop blocked on an active streamable session")
	}
	_ = session.Close()
	waitForNoGatewaySessions(t, backend)
	cancel()
	select {
	case err := <-runDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gateway did not stop")
	}
}

func waitForNoGatewaySessions(t *testing.T, backend *Backend) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for backend.sessionFactory.ActiveSessionCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if active := backend.sessionFactory.ActiveSessionCount(); active != 0 {
		t.Fatalf("active sessions = %d, want 0", active)
	}
}

func TestGatewayCapabilityExtrasSortsBackendIDsAndAddsServeTag(t *testing.T) {
	cfg := &Config{
		Agora: &mcpagora.Config{
			Enabled: true,
			Serve:   &mcpagora.ServeConfig{Enabled: true},
		},
		Backends: []aggregator.BackendConfig{
			{ID: "github"},
			{ID: "filesystem"},
		},
	}

	got := gatewayCapabilityExtras(cfg)
	want := []string{"filesystem", "github", "agora-serve"}
	if len(got) != len(want) {
		t.Fatalf("extras = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extras = %#v, want %#v", got, want)
		}
	}
}

func TestCollectAgoraTunnelsDedupes(t *testing.T) {
	tunnels := collectAgoraTunnels([]aggregator.BackendConfig{
		{
			ID:        "filesystem",
			Transport: aggregator.TransportConfig{Type: "agora", AgoraTunnel: " filesystem-relay "},
		},
		{
			ID:        "filesystem-2",
			Transport: aggregator.TransportConfig{Type: "agora", AgoraTunnel: "filesystem-relay"},
		},
		{
			ID:        "github",
			Transport: aggregator.TransportConfig{Type: "zrok", ShareToken: "share"},
		},
		{
			ID:        "notes",
			Transport: aggregator.TransportConfig{Type: "agora", AgoraTunnel: "notes-relay"},
		},
	})

	want := []string{"filesystem-relay", "notes-relay"}
	if len(tunnels) != len(want) {
		t.Fatalf("tunnels = %#v, want %#v", tunnels, want)
	}
	for i := range want {
		if tunnels[i] != want[i] {
			t.Fatalf("tunnels = %#v, want %#v", tunnels, want)
		}
	}
}
