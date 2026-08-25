package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/michaelquigley/df/dl"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openziti/mcp-gateway/aggregator"
	"github.com/openziti/mcp-gateway/tools"
)

// ClientContext holds information about the connecting client.
type ClientContext struct {
	RemoteAddr string
	UserAgent  string
	Headers    map[string]string
}

// NewClientContext extracts client context from an HTTP request.
func NewClientContext(r *http.Request) *ClientContext {
	return &ClientContext{
		RemoteAddr: r.RemoteAddr,
		UserAgent:  r.Header.Get("User-Agent"),
		Headers:    extractHeaders(r),
	}
}

// extractHeaders extracts relevant headers for logging.
func extractHeaders(r *http.Request) map[string]string {
	headers := make(map[string]string)
	for _, key := range []string{"X-Forwarded-For", "X-Real-IP", "X-Request-ID"} {
		if v := r.Header.Get(key); v != "" {
			headers[key] = v
		}
	}
	return headers
}

// ClientSession holds per-client isolated backend connections.
// each incoming SSE client gets its own ClientSession with dedicated
// connections to all configured backends.
type ClientSession struct {
	id        string
	createdAt time.Time
	client    *ClientContext
	config    *Config
	namespace *aggregator.Namespace
	agoraDial aggregator.AgoraDialClient
	policies  map[string]*aggregator.CallPolicy
	backends  map[string]*sessionBackend
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	closed    bool
}

// sessionBackend represents one backend connection for this client session.
type sessionBackend struct {
	id      string
	cfg     aggregator.BackendConfig
	client  *mcp.Client
	session *mcp.ClientSession
	cmd     *exec.Cmd     // stdio backends only
	access  *tools.Access // zrok backends only
	policy  *aggregator.CallPolicy
}

// NewClientSession creates an isolated session with connections to all backends.
// the session will be cleaned up when ctx is cancelled.
func NewClientSession(ctx context.Context, config *Config, namespace *aggregator.Namespace, client *ClientContext, agoraDial aggregator.AgoraDialClient, policies map[string]*aggregator.CallPolicy) (*ClientSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	cs := &ClientSession{
		id:        uuid.New().String(),
		createdAt: time.Now(),
		client:    client,
		config:    config,
		namespace: namespace,
		agoraDial: agoraDial,
		policies:  policies,
		backends:  make(map[string]*sessionBackend),
		ctx:       sessionCtx,
		cancel:    cancel,
	}

	// connect to all backends
	for _, bcfg := range config.Backends {
		backend, err := cs.connectBackend(sessionCtx, bcfg)
		if err != nil {
			// cleanup any backends we already connected
			cs.Close()
			return nil, fmt.Errorf("failed to connect to backend '%s': %w", bcfg.ID, err)
		}
		cs.backends[bcfg.ID] = backend
	}

	dl.Log().
		With("session_id", cs.id).
		With("remote_addr", client.RemoteAddr).
		With("user_agent", client.UserAgent).
		With("backend_count", len(cs.backends)).
		Info("client session started")

	return cs, nil
}

// connectBackend establishes a connection to a single backend.
func (cs *ClientSession) connectBackend(ctx context.Context, cfg aggregator.BackendConfig) (*sessionBackend, error) {
	policy, ok := cs.policies[cfg.ID]
	if !ok || policy == nil {
		return nil, fmt.Errorf("startup-resolved call policy for backend %q is missing", cfg.ID)
	}
	var backend *sessionBackend
	var err error
	switch cfg.Transport.Type {
	case "stdio":
		backend, err = cs.connectStdioBackend(ctx, cfg, policy.WorkingDir())
	case "zrok":
		backend, err = cs.connectZrokBackend(ctx, cfg)
	case "agora":
		backend, err = cs.connectAgoraBackend(ctx, cfg)
	case "http", "https":
		backend, err = cs.connectHTTPBackend(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported transport type '%s'", cfg.Transport.Type)
	}
	if err != nil {
		return nil, err
	}
	backend.policy = policy
	return backend, nil
}

// connectStdioBackend spawns a subprocess and connects via stdio.
func (cs *ClientSession) connectStdioBackend(ctx context.Context, cfg aggregator.BackendConfig, workingDir string) (*sessionBackend, error) {
	mcpClient := mcp.NewClient(
		&mcp.Implementation{
			Name:    cs.config.Aggregator.Name,
			Version: cs.config.Aggregator.Version,
		},
		nil,
	)

	// build command for stdio transport
	cmd := exec.CommandContext(ctx, cfg.Transport.Command, cfg.Transport.Args...)
	if workingDir != "" {
		cmd.Dir = workingDir
	} else if cfg.Transport.WorkingDir != "" {
		cmd.Dir = cfg.Transport.WorkingDir
	}

	// set environment variables per the transport's env policy
	cmd.Env = aggregator.StdioEnvironment(cfg.Transport)

	// create transport and connect
	transport := &mcp.CommandTransport{Command: cmd}
	connectCtx, cancel := context.WithTimeout(ctx, cs.config.Aggregator.Connection.ConnectTimeout)
	defer cancel()

	session, err := mcpClient.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, err
	}

	return &sessionBackend{
		id:      cfg.ID,
		cfg:     cfg,
		client:  mcpClient,
		session: session,
		cmd:     cmd,
	}, nil
}

// connectZrokBackend creates a zrok access and connects using the configured MCP protocol.
func (cs *ClientSession) connectZrokBackend(ctx context.Context, cfg aggregator.BackendConfig) (*sessionBackend, error) {
	access, err := tools.NewAccess(cfg.Transport.ShareToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create zrok access: %w", err)
	}

	connected, err := aggregator.ConnectOverlayClientSession(ctx, &mcp.Implementation{
		Name:    cs.config.Aggregator.Name,
		Version: cs.config.Aggregator.Version,
	}, cfg.Transport, access.HTTPClient(), cs.config.Aggregator.Connection.ConnectTimeout)
	if err != nil {
		access.Close()
		return nil, fmt.Errorf("failed to connect to zrok backend: %w", err)
	}

	return &sessionBackend{
		id:      cfg.ID,
		cfg:     cfg,
		client:  connected.Client,
		session: connected.Session,
		access:  access,
	}, nil
}

// connectAgoraBackend creates an Agora-backed MCP connection, dialing the
// backend's tunnel directly through the startup-attached shared HTTP client.
func (cs *ClientSession) connectAgoraBackend(ctx context.Context, cfg aggregator.BackendConfig) (*sessionBackend, error) {
	if cs.agoraDial == nil {
		return nil, fmt.Errorf("agora dial client is not configured")
	}
	tunnel := strings.TrimSpace(cfg.Transport.AgoraTunnel)
	if tunnel == "" {
		return nil, fmt.Errorf("agora tunnel for backend '%s' is required", cfg.ID)
	}
	httpClient, err := cs.agoraDial(tunnel)
	if err != nil {
		return nil, fmt.Errorf("agora dial client for backend '%s': %w", cfg.ID, err)
	}

	connected, err := aggregator.ConnectOverlayClientSession(ctx, &mcp.Implementation{
		Name:    cs.config.Aggregator.Name,
		Version: cs.config.Aggregator.Version,
	}, cfg.Transport, httpClient, cs.config.Aggregator.Connection.ConnectTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to agora backend: %w", err)
	}

	return &sessionBackend{
		id:      cfg.ID,
		cfg:     cfg,
		client:  connected.Client,
		session: connected.Session,
	}, nil
}

// connectHTTPBackend creates an HTTP(S) connection to a remote MCP backend.
func (cs *ClientSession) connectHTTPBackend(ctx context.Context, cfg aggregator.BackendConfig) (*sessionBackend, error) {
	connected, err := aggregator.ConnectHTTPClientSession(ctx, &mcp.Implementation{
		Name:    cs.config.Aggregator.Name,
		Version: cs.config.Aggregator.Version,
	}, cfg.Transport, cs.config.Aggregator.Connection.ConnectTimeout)
	if err != nil {
		return nil, err
	}

	return &sessionBackend{
		id:      cfg.ID,
		cfg:     cfg,
		client:  connected.Client,
		session: connected.Session,
	}, nil
}

// CreateMCPServer returns an mcp.Server with tool handlers routing to this session's backends.
func (cs *ClientSession) CreateMCPServer(impl *mcp.Implementation) *mcp.Server {
	server := mcp.NewServer(impl, nil)

	// register all tools from namespace with handlers that route to this session
	for _, tool := range cs.namespace.AllTools() {
		t := tool
		server.AddTool(&t, cs.createToolHandler(t.Name))
	}

	dl.Log().With("session_id", cs.id).With("tool_count", cs.namespace.Count()).Debug("created mcp server for session")
	return server
}

// createToolHandler creates a handler that routes tool calls to the appropriate backend.
func (cs *ClientSession) createToolHandler(namespacedName string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return cs.CallTool(ctx, namespacedName, req.Params.Arguments)
	}
}

// CallTool routes a tool call to the appropriate backend.
func (cs *ClientSession) CallTool(ctx context.Context, namespacedName string, args any) (*mcp.CallToolResult, error) {
	start := time.Now()

	cs.mu.Lock()
	if cs.closed {
		cs.mu.Unlock()
		return nil, errors.New("session is closed")
	}
	cs.mu.Unlock()

	// look up the tool to find which backend owns it
	tool, ok := cs.namespace.GetTool(namespacedName)
	if !ok {
		return nil, fmt.Errorf("unknown tool '%s'", namespacedName)
	}

	// find the backend for this tool
	backend, ok := cs.backends[tool.BackendID]
	if !ok {
		return nil, fmt.Errorf("backend '%s' not found for tool '%s'", tool.BackendID, namespacedName)
	}
	settled, err := backend.policy.Prepare(tool.OriginalName, args)
	if err != nil {
		duration := time.Since(start)
		loggedArgs := settled
		if loggedArgs == nil {
			loggedArgs = args
		}
		dl.Log().
			With("session_id", cs.id).
			With("tool", namespacedName).
			With("backend", tool.BackendID).
			With("args", auditArgs(loggedArgs)).
			With("duration_ms", duration.Milliseconds()).
			With("error", err.Error()).
			Info("tool call denied by policy")
		return aggregator.PolicyDeniedResult(err), nil
	}

	// apply call timeout
	callCtx, cancel := context.WithTimeout(ctx, cs.config.Aggregator.Connection.CallTimeout)
	defer cancel()

	// call the tool using the original (non-namespaced) name
	result, err := backend.session.CallTool(callCtx, &mcp.CallToolParams{
		Name:      tool.OriginalName,
		Arguments: settled,
	})
	duration := time.Since(start)

	if err != nil {
		dl.Log().
			With("session_id", cs.id).
			With("tool", namespacedName).
			With("backend", tool.BackendID).
			With("args", auditArgs(settled)).
			With("duration_ms", duration.Milliseconds()).
			With("error", err.Error()).
			Info("tool call failed")
		return nil, err
	}

	dl.Log().
		With("session_id", cs.id).
		With("tool", namespacedName).
		With("backend", tool.BackendID).
		With("args", auditArgs(settled)).
		With("duration_ms", duration.Milliseconds()).
		With("result_type", getResultType(result)).
		Info("tool call succeeded")
	return result, nil
}

// ID returns the session's unique identifier.
func (cs *ClientSession) ID() string {
	return cs.id
}

// Close cleans up all backend connections and subprocesses.
func (cs *ClientSession) Close() error {
	cs.mu.Lock()
	if cs.closed {
		cs.mu.Unlock()
		return nil
	}
	cs.closed = true
	cs.mu.Unlock()

	// close backends before cancelling. a fabric-backed backend terminates its
	// remote MCP session with a Streamable HTTP DELETE built on the connection
	// context, so cancelling first would strand that session on the far side
	// until its idle timer expired. stdio backends do not care about the
	// order — the subprocess dies either way.
	var errs []error
	for id, backend := range cs.backends {
		if err := backend.Close(); err != nil {
			dl.Log().With("session_id", cs.id).With("backend", id).With("error", err).Warn("error closing backend")
			errs = append(errs, fmt.Errorf("backend '%s': %w", id, err))
		}
	}

	// now signal any remaining operation to stop.
	cs.cancel()

	dl.Log().
		With("session_id", cs.id).
		With("duration_ms", time.Since(cs.createdAt).Milliseconds()).
		Info("client session ended")

	return errors.Join(errs...)
}

// Close cleans up the backend connection and any subprocess.
func (sb *sessionBackend) Close() error {
	var errs []error

	// close MCP session
	if sb.session != nil {
		if err := sb.session.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing session: %w", err))
		}
	}

	// terminate subprocess with graceful shutdown
	if sb.cmd != nil && sb.cmd.Process != nil {
		// send SIGTERM first
		if err := sb.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			dl.Log().With("backend", sb.id).With("error", err).Debug("sigterm failed, trying sigkill")
			sb.cmd.Process.Kill()
		} else {
			// wait for process to exit with timeout
			done := make(chan error, 1)
			go func() { done <- sb.cmd.Wait() }()

			select {
			case <-done:
				// process exited cleanly
			case <-time.After(5 * time.Second):
				dl.Log().With("backend", sb.id).Debug("process did not exit after sigterm, sending sigkill")
				sb.cmd.Process.Kill()
			}
		}
	}

	// close zrok access
	if sb.access != nil {
		if err := sb.access.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing zrok access: %w", err))
		}
	}

	return errors.Join(errs...)
}

// auditArgs preserves complete structured arguments in the per-call record.
func auditArgs(args any) any {
	if args == nil {
		return map[string]any{}
	}
	data, err := json.Marshal(args)
	if err != nil {
		return map[string]any{"audit_error": err.Error()}
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return map[string]any{"audit_error": err.Error(), "raw": string(data)}
	}
	return decoded
}

// getResultType extracts a summary of the result for logging.
func getResultType(result *mcp.CallToolResult) string {
	if result == nil {
		return "nil"
	}
	if result.IsError {
		return "error"
	}
	if len(result.Content) == 0 {
		return "empty"
	}
	// summarize content types by checking interface types
	types := make([]string, 0, len(result.Content))
	for _, c := range result.Content {
		switch c.(type) {
		case *mcp.TextContent:
			types = append(types, "text")
		case *mcp.ImageContent:
			types = append(types, "image")
		case *mcp.AudioContent:
			types = append(types, "audio")
		case *mcp.EmbeddedResource:
			types = append(types, "resource")
		default:
			types = append(types, "unknown")
		}
	}
	return strings.Join(types, ",")
}
