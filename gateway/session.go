package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
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
}

// NewClientSession creates an isolated session with connections to all backends.
// the session will be cleaned up when ctx is cancelled.
func NewClientSession(ctx context.Context, config *Config, namespace *aggregator.Namespace, client *ClientContext) (*ClientSession, error) {
	sessionCtx, cancel := context.WithCancel(ctx)

	cs := &ClientSession{
		id:        uuid.New().String(),
		createdAt: time.Now(),
		client:    client,
		config:    config,
		namespace: namespace,
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
	switch cfg.Transport.Type {
	case "stdio":
		return cs.connectStdioBackend(ctx, cfg)
	case "zrok":
		return cs.connectZrokBackend(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported transport type '%s'", cfg.Transport.Type)
	}
}

// connectStdioBackend spawns a subprocess and connects via stdio.
func (cs *ClientSession) connectStdioBackend(ctx context.Context, cfg aggregator.BackendConfig) (*sessionBackend, error) {
	mcpClient := mcp.NewClient(
		&mcp.Implementation{
			Name:    cs.config.Aggregator.Name,
			Version: cs.config.Aggregator.Version,
		},
		nil,
	)

	// build command for stdio transport
	cmd := exec.CommandContext(ctx, cfg.Transport.Command, cfg.Transport.Args...)
	if cfg.Transport.WorkingDir != "" {
		cmd.Dir = cfg.Transport.WorkingDir
	}

	// set environment variables
	cmd.Env = os.Environ()
	for k, v := range cfg.Transport.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

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

// connectZrokBackend creates a zrok access and connects via SSE.
func (cs *ClientSession) connectZrokBackend(ctx context.Context, cfg aggregator.BackendConfig) (*sessionBackend, error) {
	access, err := tools.NewAccess(cfg.Transport.ShareToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create zrok access: %w", err)
	}

	mcpClient := mcp.NewClient(
		&mcp.Implementation{
			Name:    cs.config.Aggregator.Name,
			Version: cs.config.Aggregator.Version,
		},
		nil,
	)

	// create SSE transport using zrok HTTP client
	sseTransport := &mcp.SSEClientTransport{
		// the host doesn't matter for routing since zrok handles it
		Endpoint:   "http://mcp-backend/sse",
		HTTPClient: access.HTTPClient(),
	}

	// for SSE transports, the context controls the HTTP connection lifetime.
	// we use the session context (ctx) directly rather than a timeout context,
	// because cancelling the context closes the SSE connection.
	// the connect timeout is enforced by the HTTP client's dial timeout instead.
	session, err := mcpClient.Connect(ctx, sseTransport, nil)
	if err != nil {
		access.Close()
		return nil, fmt.Errorf("failed to connect to zrok backend: %w", err)
	}

	return &sessionBackend{
		id:      cfg.ID,
		cfg:     cfg,
		client:  mcpClient,
		session: session,
		access:  access,
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

	// apply call timeout
	callCtx, cancel := context.WithTimeout(ctx, cs.config.Aggregator.Connection.CallTimeout)
	defer cancel()

	// call the tool using the original (non-namespaced) name
	result, err := backend.session.CallTool(callCtx, &mcp.CallToolParams{
		Name:      tool.OriginalName,
		Arguments: args,
	})
	duration := time.Since(start)

	if err != nil {
		dl.Log().
			With("session_id", cs.id).
			With("tool", namespacedName).
			With("backend", tool.BackendID).
			With("args", summarizeArgs(args)).
			With("duration_ms", duration.Milliseconds()).
			With("error", err.Error()).
			Info("tool call failed")
		return nil, err
	}

	dl.Log().
		With("session_id", cs.id).
		With("tool", namespacedName).
		With("backend", tool.BackendID).
		With("args", summarizeArgs(args)).
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

	// cancel context to signal all operations to stop
	cs.cancel()

	var errs []error
	for id, backend := range cs.backends {
		if err := backend.Close(); err != nil {
			dl.Log().With("session_id", cs.id).With("backend", id).With("error", err).Warn("error closing backend")
			errs = append(errs, fmt.Errorf("backend '%s': %w", id, err))
		}
	}

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

// summarizeArgs creates a loggable summary of tool arguments.
// truncates long values to avoid log bloat.
func summarizeArgs(args any) string {
	if args == nil {
		return "{}"
	}
	data, err := json.Marshal(args)
	if err != nil {
		return "<marshal error>"
	}
	if len(data) > 500 {
		return string(data[:500]) + "..."
	}
	return string(data)
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
