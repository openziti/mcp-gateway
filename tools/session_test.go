package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openziti/mcp-gateway/streamable"
)

// fakeFabric stands in for the remote gateway or bridge reached over zrok or
// Agora. every backend session it serves gets its own identity, so a test can
// tell whether two local frontends were actually isolated.
type fakeFabric struct {
	server   *httptest.Server
	sessions streamable.Sessions

	mu    sync.Mutex
	next  int
	total int
	live  map[string]struct{}
	// gate, when non-nil, stalls every DELETE until it is closed, and stalling
	// is what lets a test catch a close that is still in flight.
	gate    chan struct{}
	stalled int
	// stallPosts extends the gate to handshakes, not just closes.
	stallPosts bool
}

func newFakeFabric(t *testing.T) *fakeFabric {
	t.Helper()
	f := &fakeFabric{live: make(map[string]struct{})}
	inner := f.sessions.Handler(
		streamable.Options{SessionIdleTimeout: streamable.DefaultSessionIdleTimeout},
		func(*http.Request) (*mcp.Server, func()) {
			id := f.open()
			server := mcp.NewServer(&mcp.Implementation{Name: "fake-fabric", Version: "1.0.0"}, nil)
			server.AddTool(
				&mcp.Tool{Name: "whoami", InputSchema: map[string]any{"type": "object"}},
				func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: id}}}, nil
				},
			)
			return server, func() { f.close(id) }
		},
	)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete || (r.Method == http.MethodPost && f.stallPosts) {
			if gate := f.enterDelete(); gate != nil {
				<-gate
				f.leaveDelete()
			}
		}
		inner.ServeHTTP(w, r)
	})
	f.server = httptest.NewServer(handler)
	t.Cleanup(func() {
		// close protocol sessions first so a parked stream cannot hold the
		// test server open.
		_ = f.sessions.Close()
		f.server.Close()
	})
	return f
}

func (f *fakeFabric) open() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.total++
	id := fmt.Sprintf("backend-%d", f.next)
	f.live[id] = struct{}{}
	return id
}

func (f *fakeFabric) close(id string) {
	f.mu.Lock()
	delete(f.live, id)
	f.mu.Unlock()
}

// stallDeletes holds every subsequent DELETE until the returned release func
// is called. arm it only after startup discovery has closed its own session.
func (f *fakeFabric) stallDeletes(t *testing.T) func() {
	t.Helper()
	gate := make(chan struct{})
	f.mu.Lock()
	f.gate = gate
	f.mu.Unlock()

	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return release
}

// enterDelete returns the active gate, counting this DELETE as stalled.
func (f *fakeFabric) enterDelete() chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gate != nil {
		f.stalled++
	}
	return f.gate
}

func (f *fakeFabric) leaveDelete() {
	f.mu.Lock()
	f.stalled--
	f.mu.Unlock()
}

// stalledDeletes counts closes currently parked on the gate.
func (f *fakeFabric) stalledDeletes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stalled
}

func (f *fakeFabric) liveSessions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.live)
}

func (f *fakeFabric) totalSessions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total
}

// httpClient mirrors the zrok Access and Agora Dialer clients: DialContext
// ignores the address and routes every connection onto the fabric.
func (f *fakeFabric) httpClient() *http.Client {
	addr := f.server.Listener.Addr().String()
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
		},
	}
}

// newToolsTestClient builds a Client already started against the fake fabric,
// standing in for what Start does with a real zrok access or Agora dial.
func newToolsTestClient(t *testing.T, fabric *fakeFabric) *Client {
	t.Helper()
	c := &Client{
		httpClient: fabric.httpClient(),
		mainCtx:    context.Background(),
		sessions:   make(map[string]*backendSession),
	}

	tools, err := c.discoverTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c.tools = tools

	t.Cleanup(func() { _ = c.Stop() })
	return c
}

// newToolsTestServer serves a started Client's local Streamable HTTP endpoint.
func newToolsTestServer(t *testing.T, fabric *fakeFabric, opts *HTTPOptions) (*Client, *httptest.Server) {
	t.Helper()
	c := newToolsTestClient(t, fabric)
	local := httptest.NewServer(c.createHTTPHandler(opts))
	// close protocol sessions before the test server, so a parked SSE stream
	// cannot block Close. Stop is safe to call more than once.
	t.Cleanup(func() {
		_ = c.Stop()
		local.Close()
	})
	return c, local
}

func activeBackendSessions(c *Client) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sessions)
}

func connectLocal(t *testing.T, local *httptest.Server) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "tools-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: local.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func whoami(t *testing.T, session *mcp.ClientSession) string {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content = %#v", result.Content)
	}
	return text.Text
}

func waitFor(t *testing.T, want string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHTTPFrontendSessionsGetIsolatedBackendSessions(t *testing.T) {
	fabric := newFakeFabric(t)
	c, local := newToolsTestServer(t, fabric, &HTTPOptions{})

	first := connectLocal(t, local)
	second := connectLocal(t, local)

	firstID := whoami(t, first)
	secondID := whoami(t, second)
	if firstID == secondID {
		t.Fatalf("both frontend sessions reached backend session %q", firstID)
	}
	if got := activeBackendSessions(c); got != 2 {
		t.Fatalf("tracked backend sessions = %d, want 2", got)
	}
	if got := fabric.liveSessions(); got != 2 {
		t.Fatalf("live fabric sessions = %d, want 2", got)
	}

	// one frontend leaving must not disturb the other's backend session.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the departed frontend's backend session to close", func() bool {
		return fabric.liveSessions() == 1 && activeBackendSessions(c) == 1
	})
	if got := whoami(t, second); got != secondID {
		t.Fatalf("surviving session backend = %q, want %q", got, secondID)
	}

	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "every backend session to close", func() bool {
		return fabric.liveSessions() == 0 && activeBackendSessions(c) == 0
	})
}

func TestHTTPDiscoverySessionDoesNotOutliveStart(t *testing.T) {
	fabric := newFakeFabric(t)
	c := newToolsTestClient(t, fabric)

	if got := len(c.tools); got != 1 || c.tools[0].Name != "whoami" {
		t.Fatalf("discovered tools = %#v", c.tools)
	}
	if got := fabric.totalSessions(); got != 1 {
		t.Fatalf("fabric sessions opened during discovery = %d, want 1", got)
	}
	if got := fabric.liveSessions(); got != 0 {
		t.Fatalf("live fabric sessions after discovery = %d, want 0", got)
	}
}

func TestHTTPSessionDeleteReleasesBackendSession(t *testing.T) {
	fabric := newFakeFabric(t)
	c, local := newToolsTestServer(t, fabric, &HTTPOptions{})

	sessionID := initializeRaw(t, local)
	waitFor(t, "the backend session to open", func() bool { return fabric.liveSessions() == 1 })

	request, err := http.NewRequest(http.MethodDelete, local.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Mcp-Session-Id", sessionID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}

	waitFor(t, "the backend session to close after DELETE", func() bool {
		return fabric.liveSessions() == 0 && activeBackendSessions(c) == 0
	})
}

func TestHTTPIdleTimeoutClosesAbandonedBackendSession(t *testing.T) {
	fabric := newFakeFabric(t)
	idleTimeout := 500 * time.Millisecond
	c, local := newToolsTestServer(t, fabric, &HTTPOptions{SessionIdleTimeout: &idleTimeout})

	initializeRaw(t, local)
	waitFor(t, "the backend session to open", func() bool { return fabric.liveSessions() == 1 })

	// abandon the frontend session without sending the Streamable HTTP DELETE.
	waitFor(t, "the abandoned backend session to expire", func() bool {
		return fabric.liveSessions() == 0 && activeBackendSessions(c) == 0
	})
}

func TestHTTPFailedInitializationDoesNotLeakBackendSession(t *testing.T) {
	fabric := newFakeFabric(t)
	c, local := newToolsTestServer(t, fabric, &HTTPOptions{})

	response := postRaw(t, local, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	_ = response.Body.Close()

	waitFor(t, "the uninitialized backend session to close", func() bool {
		return fabric.liveSessions() == 0 && activeBackendSessions(c) == 0
	})
}

func TestStopClosesActiveBackendSession(t *testing.T) {
	fabric := newFakeFabric(t)
	c, local := newToolsTestServer(t, fabric, &HTTPOptions{})

	session := connectLocal(t, local)
	whoami(t, session)
	if got := fabric.liveSessions(); got != 1 {
		t.Fatalf("live fabric sessions = %d, want 1", got)
	}

	stopped := make(chan error, 1)
	go func() { stopped <- c.Stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop blocked on an active frontend session")
	}

	waitFor(t, "every backend session to close on shutdown", func() bool {
		return fabric.liveSessions() == 0 && activeBackendSessions(c) == 0
	})
	_ = session.Close()
}

func TestStatelessHTTPOwnsBackendSessionPerRequest(t *testing.T) {
	fabric := newFakeFabric(t)
	c, local := newToolsTestServer(t, fabric, &HTTPOptions{Stateless: true, JSONResponse: true})

	// a stateless frontend carries no session id, so each request must get its
	// own backend session, opened and released within the request.
	first := callWhoamiStateless(t, local)
	second := callWhoamiStateless(t, local)
	if first == second {
		t.Fatalf("both stateless requests reached backend session %q", first)
	}

	waitFor(t, "every per-request backend session to close", func() bool {
		return fabric.liveSessions() == 0 && activeBackendSessions(c) == 0
	})
	if got := fabric.totalSessions(); got < 3 {
		t.Fatalf("fabric sessions opened = %d, want discovery plus one per request", got)
	}
}

// initializeRaw performs a bare Streamable HTTP initialize and returns the
// session id the handler established.
func initializeRaw(t *testing.T, local *httptest.Server) string {
	t.Helper()
	response := postRaw(t, local, "",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw-client","version":"1.0.0"}}}`)
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("initialize status = %d: %s", response.StatusCode, body)
	}
	sessionID := response.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("initialize response did not establish a stateful session")
	}
	return sessionID
}

func callWhoamiStateless(t *testing.T, local *httptest.Server) string {
	t.Helper()
	response := postRaw(t, local, "",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`)
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("tools/call status = %d: %s", response.StatusCode, body)
	}

	var envelope struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	if len(envelope.Result.Content) == 0 {
		t.Fatalf("tools/call returned no content: %s", body)
	}
	return envelope.Result.Content[0].Text
}

func postRaw(t *testing.T, local *httptest.Server, sessionID, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, local.URL, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestStdioServeOwnsOneBackendSession(t *testing.T) {
	fabric := newFakeFabric(t)
	c := newToolsTestClient(t, fabric)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	served := make(chan error, 1)
	go func() { served <- c.serve(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := whoami(t, session); got == "" {
		t.Fatal("stdio frontend reached no backend session")
	}
	if got := activeBackendSessions(c); got != 1 {
		t.Fatalf("tracked backend sessions = %d, want 1", got)
	}
	if got := fabric.liveSessions(); got != 1 {
		t.Fatalf("live fabric sessions = %d, want 1", got)
	}

	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the frontend disconnected")
	}

	waitFor(t, "the stdio backend session to close", func() bool {
		return fabric.liveSessions() == 0 && activeBackendSessions(c) == 0
	})
}

func TestServeRequiresStart(t *testing.T) {
	c := &Client{sessions: make(map[string]*backendSession)}
	if err := c.serve(context.Background(), &mcp.StdioTransport{}); err == nil {
		t.Fatal("expected an unstarted client to refuse to serve")
	}
}

func TestRunHTTPRequiresStart(t *testing.T) {
	c := &Client{sessions: make(map[string]*backendSession)}
	if err := c.RunHTTP(context.Background(), &HTTPOptions{Address: "127.0.0.1:0"}); err == nil {
		t.Fatal("expected an unstarted client to refuse to serve http")
	}
}

func TestEffectiveSessionIdleTimeoutFallsBackToDefault(t *testing.T) {
	if got := (*HTTPOptions)(nil).EffectiveSessionIdleTimeout(); got != streamable.DefaultSessionIdleTimeout {
		t.Fatalf("nil options timeout = %s, want %s", got, streamable.DefaultSessionIdleTimeout)
	}
	if got := (&HTTPOptions{}).EffectiveSessionIdleTimeout(); got != streamable.DefaultSessionIdleTimeout {
		t.Fatalf("unset timeout = %s, want %s", got, streamable.DefaultSessionIdleTimeout)
	}
	disabled := time.Duration(0)
	if got := (&HTTPOptions{SessionIdleTimeout: &disabled}).EffectiveSessionIdleTimeout(); got != 0 {
		t.Fatalf("explicit zero timeout = %s, want 0", got)
	}
}

// TestShutdownReleasesRemoteSessions guards the teardown path that process
// shutdown takes. the SDK builds a session's closing DELETE on the context it
// was connected with, so a backend session wired to the signal-driven context
// cannot release its remote counterpart once that context is cancelled — the
// far side would hold the client's backends until its own idle timer expired.
func TestShutdownReleasesRemoteSessions(t *testing.T) {
	fabric := newFakeFabric(t)
	c := newToolsTestClient(t, fabric)

	mainCtx, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	c.mainCtx = mainCtx

	local := httptest.NewServer(c.createHTTPHandler(&HTTPOptions{}))
	t.Cleanup(local.Close)

	session := connectLocal(t, local)
	whoami(t, session)
	if got := fabric.liveSessions(); got != 1 {
		t.Fatalf("live fabric sessions = %d, want 1", got)
	}

	// SIGINT reaches mcp-tools: the process context is cancelled, then the
	// streamable set closes the frontend sessions it owns.
	shutdown()
	if err := c.streamable.Close(); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the remote session to be released during shutdown", func() bool {
		return fabric.liveSessions() == 0 && activeBackendSessions(c) == 0
	})
	_ = session.Close()
}

func TestRunHTTPRejectsNegativeSessionIdleTimeout(t *testing.T) {
	fabric := newFakeFabric(t)
	c := newToolsTestClient(t, fabric)

	negative := -time.Second
	err := c.RunHTTP(context.Background(), &HTTPOptions{
		Address:            "127.0.0.1:0",
		SessionIdleTimeout: &negative,
	})
	if err == nil || !strings.Contains(err.Error(), "must not be negative") {
		t.Fatalf("expected negative timeout error, got %v", err)
	}
}

// TestSessionStaysTrackedUntilCloseCompletes guards the shutdown ordering.
// closing a fabric session is a network call over the shared attachment, so a
// session must remain visible to Stop until its close returns — otherwise Stop
// can see an empty map and release the fabric out from under the DELETE.
func TestSessionStaysTrackedUntilCloseCompletes(t *testing.T) {
	fabric := newFakeFabric(t)
	c := newToolsTestClient(t, fabric)

	// arm the gate only now: startup discovery closes its own session, and
	// that DELETE must not stall.
	release := fabric.stallDeletes(t)

	session, err := c.newBackendSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	removed := make(chan struct{})
	go func() {
		c.removeBackendSession(session.id)
		close(removed)
	}()

	waitFor(t, "the close to park on the stalled DELETE", func() bool {
		return fabric.stalledDeletes() == 1
	})
	if got := activeBackendSessions(c); got != 1 {
		t.Fatalf("tracked backend sessions during in-flight close = %d, want 1", got)
	}
	select {
	case <-removed:
		t.Fatal("removeBackendSession returned before its close completed")
	default:
	}

	release()
	select {
	case <-removed:
	case <-time.After(5 * time.Second):
		t.Fatal("removeBackendSession did not return after the DELETE was released")
	}
	if got := activeBackendSessions(c); got != 0 {
		t.Fatalf("tracked backend sessions after close = %d, want 0", got)
	}
}

// TestStopWaitsForInFlightSessionCreation guards the other half of the shutdown
// ordering. a handshake still in flight when Stop begins must be waited for, so
// its session lands in the set Stop closes rather than registering afterwards
// and later sending its DELETE over a released fabric attachment.
func TestStopWaitsForInFlightSessionCreation(t *testing.T) {
	fabric := newFakeFabric(t)
	c := newToolsTestClient(t, fabric)

	fabric.stallPosts = true
	release := fabric.stallDeletes(t)

	created := make(chan error, 1)
	go func() {
		_, err := c.newBackendSession(context.Background())
		created <- err
	}()

	waitFor(t, "the handshake to park on the gate", func() bool {
		return fabric.stalledDeletes() == 1
	})

	stopped := make(chan error, 1)
	go func() { stopped <- c.Stop() }()

	// Stop must not finish while the handshake is unresolved.
	select {
	case <-stopped:
		t.Fatal("Stop returned while a session handshake was still in flight")
	case <-time.After(250 * time.Millisecond):
	}

	release()
	if err := <-created; err != nil {
		t.Fatalf("in-flight session creation failed: %v", err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the handshake settled")
	}

	waitFor(t, "the late session to be closed by Stop", func() bool {
		return fabric.liveSessions() == 0 && activeBackendSessions(c) == 0
	})
}

func TestNewBackendSessionRefusedAfterStop(t *testing.T) {
	fabric := newFakeFabric(t)
	c := newToolsTestClient(t, fabric)

	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.newBackendSession(context.Background()); !errors.Is(err, errShuttingDown) {
		t.Fatalf("expected shutdown refusal, got %v", err)
	}
}

// TestAwaitCreationsGivesUpAfterTimeout pins the bound on Stop's wait. the
// fabric HTTP clients set no timeout, so a wedged handshake must not be able to
// hold shutdown open indefinitely.
func TestAwaitCreationsGivesUpAfterTimeout(t *testing.T) {
	c := &Client{sessions: make(map[string]*backendSession)}

	c.mu.Lock()
	c.creating = 1
	abandoned := c.awaitCreationsLocked(50 * time.Millisecond)
	c.mu.Unlock()

	if abandoned != 1 {
		t.Fatalf("abandoned creations = %d, want 1", abandoned)
	}
}

func TestAwaitCreationsReturnsOnceCreationsSettle(t *testing.T) {
	c := &Client{sessions: make(map[string]*backendSession)}

	c.mu.Lock()
	c.creating = 1
	c.mu.Unlock()

	go func() {
		time.Sleep(20 * time.Millisecond)
		c.mu.Lock()
		c.creating--
		c.settledCond().Broadcast()
		c.mu.Unlock()
	}()

	c.mu.Lock()
	abandoned := c.awaitCreationsLocked(5 * time.Second)
	c.mu.Unlock()

	if abandoned != 0 {
		t.Fatalf("abandoned creations = %d, want 0", abandoned)
	}
}
