package bridge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	mcpagora "github.com/openziti/mcp-gateway/agora"
	"github.com/openziti/mcp-gateway/streamable"
)

const bridgeSessionHelperEnv = "MCP_BRIDGE_SESSION_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(bridgeSessionHelperEnv) != "" {
		server := mcp.NewServer(&mcp.Implementation{Name: "bridge-session-helper", Version: "1.0.0"}, nil)
		server.AddTool(&mcp.Tool{Name: "ping", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "pong"}}}, nil
		})
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestBridgeStreamableSessionClosesBackendSubprocessWithClient(t *testing.T) {
	bridge, httpServer := newStreamableBridgeTestServer(t)
	client := mcp.NewClient(&mcp.Implementation{Name: "bridge-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}

	backendSession := waitForBridgeSessions(t, bridge, 1)[0]
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != "pong" {
		t.Fatalf("result = %#v", result.Content)
	}

	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	waitForBridgeSessions(t, bridge, 0)
	waitForBridgeProcessExit(t, backendSession)
}

func TestBridgeStreamableFailedInitializationDoesNotLeakSubprocess(t *testing.T) {
	bridge, httpServer := newStreamableBridgeTestServer(t)
	request, err := http.NewRequest(
		http.MethodPost,
		httpServer.URL,
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`),
	)
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

	waitForBridgeSessions(t, bridge, 0)
}

func TestBridgeStreamableIdleTimeoutClosesAbandonedSubprocess(t *testing.T) {
	bridge, httpServer := newStreamableBridgeTestServer(t, 500*time.Millisecond)
	request, err := http.NewRequest(
		http.MethodPost,
		httpServer.URL,
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"abandoned-client","version":"1.0.0"}}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("initialize status = %d: %s", response.StatusCode, body)
	}
	if response.Header.Get("Mcp-Session-Id") == "" {
		t.Fatal("initialize response did not establish a stateful session")
	}

	backendSession := waitForBridgeSessions(t, bridge, 1)[0]
	// abandon the session without sending the Streamable HTTP DELETE request.
	waitForBridgeSessions(t, bridge, 0)
	waitForBridgeProcessExit(t, backendSession)
}

func TestBridgeStopClosesActiveStreamableSession(t *testing.T) {
	bridge, httpServer := newStreamableBridgeTestServer(t)
	client := mcp.NewClient(&mcp.Implementation{Name: "bridge-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	backendSession := waitForBridgeSessions(t, bridge, 1)[0]

	stopDone := make(chan error, 1)
	go func() { stopDone <- bridge.Stop() }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge stop blocked on an active streamable session")
	}

	waitForBridgeSessions(t, bridge, 0)
	waitForBridgeProcessExit(t, backendSession)
	_ = session.Close()
}

func newStreamableBridgeTestServer(t *testing.T, sessionIdleTimeout ...time.Duration) (*Bridge, *httptest.Server) {
	t.Helper()
	timeout := streamable.DefaultSessionIdleTimeout
	if len(sessionIdleTimeout) > 0 {
		timeout = sessionIdleTimeout[0]
	}
	bridge := &Bridge{
		cfg: &Config{
			Command:            os.Args[0],
			Env:                map[string]string{bridgeSessionHelperEnv: "1"},
			SessionIdleTimeout: &timeout,
		},
		tools: []*mcp.Tool{{
			Name:        "ping",
			InputSchema: map[string]any{"type": "object"},
		}},
		mainCtx:  context.Background(),
		sessions: make(map[string]*bridgeSession),
	}
	httpServer := httptest.NewServer(bridge.createHTTPHandler())
	t.Cleanup(httpServer.Close)
	return bridge, httpServer
}

func waitForBridgeSessions(t *testing.T, bridge *Bridge, want int) []*bridgeSession {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		bridge.mu.Lock()
		sessions := make([]*bridgeSession, 0, len(bridge.sessions))
		for _, session := range bridge.sessions {
			sessions = append(sessions, session)
		}
		bridge.mu.Unlock()
		if len(sessions) == want {
			return sessions
		}
		if time.Now().After(deadline) {
			t.Fatalf("active bridge sessions = %d, want %d", len(sessions), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForBridgeProcessExit(t *testing.T, session *bridgeSession) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := session.cmd.Process.Signal(syscall.Signal(0))
		if err != nil {
			if !errors.Is(err, os.ErrProcessDone) {
				t.Logf("backend process exit signal: %v", err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("bridge backend subprocess remained alive after session cleanup")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBridgeCommandTagInference(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{
			name: "npx scoped package",
			cfg:  &Config{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem"}},
			want: "filesystem",
		},
		{
			name: "uvx package",
			cfg:  &Config{Command: "uvx", Args: []string{"mcp-server-git"}},
			want: "git",
		},
		{
			name: "docker image",
			cfg:  &Config{Command: "docker", Args: []string{"run", "mcp/postgres"}},
			want: "postgres",
		},
		{
			name: "bare command",
			cfg:  &Config{Command: "mcp-server-filesystem"},
			want: "filesystem",
		},
		{
			name: "override",
			cfg:  &Config{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem"}, AgoraCapabilityTag: "files"},
			want: "files",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bridgeCommandTag(tt.cfg); got != tt.want {
				t.Fatalf("tag = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBridgeCapabilityExtrasAddsServeTag(t *testing.T) {
	cfg := &Config{
		Command: "mcp-server-filesystem",
		Zrok:    &ZrokConfig{Share: &ZrokShareConfig{Enabled: false}},
		Agora: &mcpagora.Config{
			Enabled: true,
			Serve:   &mcpagora.ServeConfig{Enabled: true},
		},
	}

	got := bridgeCapabilityExtras(cfg)
	want := []string{"filesystem", "agora-serve"}
	if len(got) != len(want) {
		t.Fatalf("extras = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("extras = %#v, want %#v", got, want)
		}
	}
}
