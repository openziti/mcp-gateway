package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/michaelquigley/df/dl"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	mcpagora "github.com/openziti/mcp-gateway/agora"
	"github.com/openziti/mcp-gateway/streamable"
)

// httpShutdownTimeout bounds the graceful shutdown of the local HTTP server,
// and with it how long Stop waits for a handshake that is still in flight.
const httpShutdownTimeout = 5 * time.Second

// Client bridges local MCP frontends to a remote backend over zrok or Agora.
// the fabric attachment — the zrok access or the Agora dial — is owned for the
// lifetime of the process, but every frontend session gets its own MCP session
// on the backend, so local agents never share remote gateway or bridge state.
type Client struct {
	shareToken  string
	agoraTunnel string
	agoraConfig *mcpagora.Config
	access      *Access
	agoraClient *mcpagora.Client
	httpClient  *http.Client
	tools       []*mcp.Tool // discovered at Start (immutable)
	mainCtx     context.Context

	streamable streamable.Sessions

	// mu guards the whole backend-session lifecycle: the tracked set, the
	// count of sessions whose handshake is still in flight, and the shutdown
	// flag. closing a fabric session is itself a network call over the shared
	// attachment, so Stop cannot release that attachment until every creation
	// and close has settled.
	mu       sync.Mutex
	settled  *sync.Cond
	sessions map[string]*backendSession
	creating int
	closing  bool
}

// errShuttingDown refuses a frontend session that arrives after Stop begins.
var errShuttingDown = fmt.Errorf("mcp-tools is shutting down")

// settledCond returns the lifecycle condition variable, creating it on first
// use so a zero-value Client stays usable. callers must hold mu.
func (c *Client) settledCond() *sync.Cond {
	if c.settled == nil {
		c.settled = sync.NewCond(&c.mu)
	}
	return c.settled
}

// awaitCreationsLocked waits for in-flight handshakes to settle, up to within,
// and returns how many were still outstanding when it gave up. callers must
// hold mu, which is released and reacquired while waiting.
func (c *Client) awaitCreationsLocked(within time.Duration) int {
	if c.creating == 0 {
		return 0
	}

	// sync.Cond has no deadline, so a timer does the waking.
	expired := false
	timer := time.AfterFunc(within, func() {
		c.mu.Lock()
		expired = true
		c.settledCond().Broadcast()
		c.mu.Unlock()
	})
	defer timer.Stop()

	for c.creating > 0 && !expired {
		c.settledCond().Wait()
	}
	return c.creating
}

// backendSession is one frontend session's private MCP session on the fabric
// backend. Close is the teardown that matters: the SDK sends the Streamable
// HTTP DELETE that releases the remote gateway or bridge session. that DELETE
// is built on the context the session was connected with, so the context must
// outlive process shutdown — see newBackendSession.
type backendSession struct {
	id        string
	createdAt time.Time
	session   *mcp.ClientSession
	cancel    context.CancelFunc
}

// New creates a Client for the given share token.
func New(shareToken string) (*Client, error) {
	if shareToken == "" {
		return nil, fmt.Errorf("share token is required")
	}

	return &Client{
		shareToken: shareToken,
		sessions:   make(map[string]*backendSession),
	}, nil
}

// NewAgora creates a Client for the given Agora tunnel.
func NewAgora(tunnel string, cfg *mcpagora.Config) (*Client, error) {
	tunnel = strings.TrimSpace(tunnel)
	if tunnel == "" {
		return nil, fmt.Errorf("agora tunnel is required")
	}
	if cfg == nil {
		return nil, fmt.Errorf("agora config is required")
	}
	cfg.Enabled = true
	agoraClient, err := newAgoraClient(cfg)
	if err != nil {
		return nil, err
	}

	return &Client{
		agoraTunnel: tunnel,
		agoraConfig: cfg,
		agoraClient: agoraClient,
		sessions:    make(map[string]*backendSession),
	}, nil
}

// Start attaches to the fabric and discovers tools. the attachment is held for
// the lifetime of the process; backend sessions are opened per frontend session
// by Run and RunHTTP.
func (c *Client) Start(ctx context.Context) error {
	dl.Log().Debug("starting mcp-tools")
	c.mainCtx = ctx

	httpClient, err := c.attachFabric(ctx)
	if err != nil {
		return err
	}
	c.httpClient = httpClient

	tools, err := c.discoverTools(ctx)
	if err != nil {
		c.closeTransport()
		return err
	}
	c.tools = tools

	dl.Log().With("toolCount", len(tools)).Debug("discovered tools from backend")
	dl.Log().Debug("mcp-tools started")
	return nil
}

// attachFabric acquires the process-lifetime fabric attachment — the zrok
// access or the Agora tunnel reservation — and returns the HTTP client routed
// through it. the dialing itself happens later, per connection, inside that
// client's DialContext.
func (c *Client) attachFabric(ctx context.Context) (*http.Client, error) {
	if c.agoraTunnel != "" {
		dl.Log().With("agora_tunnel", c.agoraTunnel).Debug("creating agora dial")
		if c.agoraClient == nil {
			agoraClient, err := newAgoraClient(c.agoraConfig)
			if err != nil {
				return nil, err
			}
			c.agoraClient = agoraClient
		}
		httpClient, err := c.agoraClient.Attach(ctx, c.agoraTunnel)
		if err != nil {
			c.closeTransport()
			return nil, fmt.Errorf("failed to dial agora tunnel: %w", err)
		}
		return httpClient, nil
	}

	dl.Log().With("share_token", c.shareToken).Debug("creating zrok access")
	access, err := NewAccess(c.shareToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create access: %w", err)
	}
	c.access = access
	return access.HTTPClient(), nil
}

// discoverTools opens a throwaway backend session to read the tool list. the
// session is closed immediately; every frontend session opens its own.
func (c *Client) discoverTools(ctx context.Context) ([]*mcp.Tool, error) {
	session, err := c.connect(ctx)
	if err != nil {
		return nil, err
	}

	toolsResult, err := session.ListTools(ctx, nil)
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("failed to list tools: %w", err)
	}

	if err := session.Close(); err != nil {
		dl.Log().With("error", err).Debug("error closing discovery session")
	}

	return toolsResult.Tools, nil
}

// connect opens one MCP session on the fabric backend. each call initializes a
// distinct Streamable HTTP session on the remote gateway or bridge.
func (c *Client) connect(ctx context.Context) (*mcp.ClientSession, error) {
	mcpClient := mcp.NewClient(
		&mcp.Implementation{
			Name:    "mcp-tools",
			Version: "1.0.0",
		},
		nil,
	)

	session, err := mcpClient.Connect(ctx, newFabricMCPTransport(c.httpClient), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to backend: %w", err)
	}
	return session, nil
}

// newBackendSession opens and tracks a private backend session for one
// frontend session.
//
// the session context is fully detached from ctx, and nothing but Close ever
// cancels it. the SDK derives the connection's lifecycle context from the one
// passed to Connect and builds its closing DELETE on it, so any path that can
// cancel that context early leaves the remote gateway or bridge session alive
// until its own idle timer expires. cancelling the handshake at shutdown was
// tried and reintroduced exactly that: a Connect can succeed as the
// cancellation lands, yielding a live session with a dead context. shutdown is
// bounded by Stop's wait instead — see Stop.
func (c *Client) newBackendSession(ctx context.Context) (*backendSession, error) {
	// count this creation before the handshake, so Stop waits for it rather
	// than releasing the fabric while it is still in flight.
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return nil, errShuttingDown
	}
	c.creating++
	c.mu.Unlock()

	sessionCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	session, err := c.connect(sessionCtx)
	if err != nil {
		cancel()
		c.finishCreation(nil)
		return nil, err
	}

	bs := &backendSession{
		id:        uuid.New().String(),
		createdAt: time.Now(),
		session:   session,
		cancel:    cancel,
	}
	c.finishCreation(bs)

	dl.Log().With("session_id", bs.id).Info("backend session started")

	return bs, nil
}

// finishCreation retires an in-flight creation and wakes anything waiting for
// creations to settle, registering the session when the handshake produced one.
// registration happens even during shutdown: Stop is still waiting on this
// creation, so the session lands in the set it is about to close.
func (c *Client) finishCreation(bs *backendSession) {
	c.mu.Lock()
	c.creating--
	if bs != nil {
		c.sessions[bs.id] = bs
	}
	c.settledCond().Broadcast()
	c.mu.Unlock()
}

// removeBackendSession closes and untracks a session.
//
// the session stays tracked until its Close returns. closing a fabric session
// is itself a network call over the shared attachment, so Stop must be able to
// see a close that is still in flight — otherwise it can observe an empty map
// and release the zrok access or Agora tunnel out from under the DELETE. the
// SDK guards Close with a sync.Once, so Stop closing the same session simply
// waits for the in-flight close instead of duplicating it.
func (c *Client) removeBackendSession(sessionID string) {
	c.mu.Lock()
	session, ok := c.sessions[sessionID]
	c.mu.Unlock()
	if !ok {
		return
	}

	session.Close()

	c.mu.Lock()
	delete(c.sessions, sessionID)
	c.mu.Unlock()
}

func newFabricMCPTransport(httpClient *http.Client) mcp.Transport {
	return &mcp.StreamableClientTransport{
		// the host does not matter for routing through zrok or Agora.
		Endpoint:   "http://mcp-backend",
		HTTPClient: httpClient,
	}
}

func newAgoraClient(cfg *mcpagora.Config) (*mcpagora.Client, error) {
	agoraClient, err := mcpagora.NewClient(mcpagora.ClientOptions{
		Config: cfg,
		Defaults: mcpagora.Defaults{
			InstanceName:    "mcp-tools",
			Description:     "MCP tools client",
			AgentNamePrefix: "mcp-tools",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create agora client: %w", err)
	}
	return agoraClient, nil
}

func (c *Client) closeTransport() error {
	var lastErr error
	if c.access != nil {
		if err := c.access.Close(); err != nil {
			lastErr = err
		}
	}
	if c.agoraClient != nil {
		if err := c.agoraClient.Close(); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// createProxyServer creates an MCP server that proxies to this session's backend.
func (bs *backendSession) createProxyServer(tools []*mcp.Tool) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "mcp-tools",
			Version: "1.0.0",
		},
		nil,
	)

	for _, tool := range tools {
		t := tool
		server.AddTool(t, bs.createProxyHandler(t.Name))
		dl.Log().With("session_id", bs.id).With("tool", t.Name).Debug("registered proxy handler")
	}

	return server
}

// createProxyHandler creates a handler that forwards tool calls to this
// session's backend.
func (bs *backendSession) createProxyHandler(toolName string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		dl.Log().With("session_id", bs.id).With("tool", toolName).Debug("forwarding tool call to backend")

		result, err := bs.session.CallTool(ctx, &mcp.CallToolParams{
			Name:      toolName,
			Arguments: req.Params.Arguments,
		})
		if err != nil {
			dl.Log().With("session_id", bs.id).With("tool", toolName).With("error", err).Debug("tool call failed")
			return nil, err
		}

		dl.Log().With("session_id", bs.id).With("tool", toolName).Debug("tool call completed")
		return result, nil
	}
}

// Close ends the backend session, releasing the remote gateway or bridge state
// it owns.
func (bs *backendSession) Close() error {
	var errs []error

	if bs.session != nil {
		if err := bs.session.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing session: %w", err))
		}
	}
	// only after the close attempt: cancelling first would strand the DELETE.
	if bs.cancel != nil {
		bs.cancel()
	}

	dl.Log().
		With("session_id", bs.id).
		With("duration_ms", time.Since(bs.createdAt).Milliseconds()).
		Info("backend session ended")

	return errors.Join(errs...)
}

// Run serves MCP on stdio (blocks until context cancelled). stdio is a single
// frontend session, so it owns exactly one backend session.
func (c *Client) Run(ctx context.Context) error {
	dl.Log().Debug("serving mcp on stdio")

	return c.serve(ctx, &mcp.StdioTransport{})
}

// serve owns one backend session for the lifetime of a single frontend
// transport, releasing it when that transport is done.
func (c *Client) serve(ctx context.Context, transport mcp.Transport) error {
	if c.httpClient == nil {
		return fmt.Errorf("client not started, call Start() first")
	}

	session, err := c.newBackendSession(ctx)
	if err != nil {
		return err
	}
	defer c.removeBackendSession(session.id)

	return session.createProxyServer(c.tools).Run(ctx, transport)
}

// HTTPOptions configures the HTTP server mode.
type HTTPOptions struct {
	Address            string // bind address (e.g., "127.0.0.1:8080")
	Stateless          bool   // if true, don't track sessions
	JSONResponse       bool   // if true, prefer JSON over SSE responses
	SessionIdleTimeout *time.Duration
}

// EffectiveSessionIdleTimeout returns the configured Streamable HTTP idle
// timeout, falling back to the default when unset.
func (o *HTTPOptions) EffectiveSessionIdleTimeout() time.Duration {
	if o == nil || o.SessionIdleTimeout == nil {
		return streamable.DefaultSessionIdleTimeout
	}
	return *o.SessionIdleTimeout
}

// RunHTTP serves MCP over HTTP (blocks until context cancelled).
func (c *Client) RunHTTP(ctx context.Context, opts *HTTPOptions) error {
	if c.httpClient == nil {
		return fmt.Errorf("client not started, call Start() first")
	}

	if opts == nil {
		opts = &HTTPOptions{Address: "127.0.0.1:8080"}
	}
	// a negative duration reaches the SDK as "never expire", which is the
	// opposite of the documented zero opt-out. gateway and bridge refuse it at
	// this same level, in Config.Validate.
	if opts.EffectiveSessionIdleTimeout() < 0 {
		return fmt.Errorf("session idle timeout must not be negative")
	}

	dl.Log().With("address", opts.Address).Debug("serving MCP on http")

	httpServer := &http.Server{
		Addr:    opts.Address,
		Handler: c.createHTTPHandler(opts),
	}

	// graceful shutdown on context cancellation. protocol sessions are closed
	// first so a parked SSE stream cannot hold the shutdown open.
	go func() {
		<-ctx.Done()
		if err := c.streamable.Close(); err != nil {
			dl.Log().With("error", err).Warn("error closing streamable sessions")
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()

	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// createHTTPHandler creates a streamable HTTP handler that opens a private
// backend session for each local frontend session. in stateless mode the SDK
// asks for a server per request, so ownership is per request instead.
func (c *Client) createHTTPHandler(opts *HTTPOptions) http.Handler {
	return c.streamable.Handler(streamable.Options{
		SessionIdleTimeout: opts.EffectiveSessionIdleTimeout(),
		Stateless:          opts.Stateless,
		JSONResponse:       opts.JSONResponse,
	}, func(r *http.Request) (*mcp.Server, func()) {
		session, err := c.newBackendSession(c.mainCtx)
		if err != nil {
			dl.Log().With("error", err).Error("failed to create backend session")
			return nil, func() {}
		}

		var cleanupOnce sync.Once
		cleanup := func() {
			cleanupOnce.Do(func() {
				c.removeBackendSession(session.id)
			})
		}

		return session.createProxyServer(c.tools), cleanup
	})
}

// Stop gracefully shuts down the client.
func (c *Client) Stop() error {
	dl.Log().Debug("stopping mcp-tools")

	var lastErr error

	if err := c.streamable.Close(); err != nil {
		dl.Log().With("error", err).Warn("error closing streamable sessions")
		lastErr = err
	}

	// refuse new sessions, then wait for any handshake still in flight so its
	// session lands in the snapshot below rather than after it. the wait is
	// bounded because the fabric HTTP clients set no timeout, and a wedged
	// overlay must not hold shutdown open forever.
	//
	// the bound has an accepted cost. the far side creates its session when
	// the initialize request *arrives*, so a handshake whose response is still
	// in flight when the wait expires leaves a real remote session, holding
	// real backend resources, that this process can no longer terminate. only
	// the far side's idle expiry reclaims it.
	c.mu.Lock()
	c.closing = true
	if abandoned := c.awaitCreationsLocked(httpShutdownTimeout); abandoned > 0 {
		dl.Log().With("in_flight", abandoned).
			Warn("shutting down with backend session handshakes still in flight; the remote sessions will be released by idle expiry")
	}
	sessions := make([]*backendSession, 0, len(c.sessions))
	for _, session := range c.sessions {
		sessions = append(sessions, session)
	}
	c.sessions = make(map[string]*backendSession)
	c.mu.Unlock()

	for _, session := range sessions {
		if err := session.Close(); err != nil {
			dl.Log().With("session_id", session.id).With("error", err).Debug("error closing backend session")
			lastErr = err
		}
	}

	if err := c.closeTransport(); err != nil {
		dl.Log().With("error", err).Debug("error closing transport")
		lastErr = err
	}

	dl.Log().Debug("mcp-tools stopped")
	return lastErr
}
