package aggregator

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/michaelquigley/df/dl"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openziti/mcp-gateway/tools"
)

// Backend represents a connection to a single backend MCP server.
type Backend struct {
	id      string
	name    string
	client  *mcp.Client
	session *mcp.ClientSession
	tools   []*mcp.Tool
	access  *tools.Access // non-nil for zrok backends
	policy  *CallPolicy
	mu      sync.RWMutex
}

// BackendManager manages connections to multiple backend MCP servers.
type BackendManager struct {
	backends  map[string]*Backend
	config    *Config
	agoraDial AgoraDialClient
	mu        sync.RWMutex
}

// AgoraDialClient returns the shared *http.Client for an Agora tunnel. It is a
// pure accessor over clients attached at startup — no ctx, since attaching has
// already happened — injected to keep the aggregator decoupled from the agora
// package, the same way the loopback resolver used to be.
type AgoraDialClient func(tunnel string) (*http.Client, error)

// NewBackendManager creates a new manager for backend connections.
func NewBackendManager(cfg *Config) *BackendManager {
	return &BackendManager{
		backends: make(map[string]*Backend),
		config:   cfg,
	}
}

// SetAgoraDialClient sets the dial seam used for Agora backends.
func (m *BackendManager) SetAgoraDialClient(dial AgoraDialClient) {
	m.agoraDial = dial
}

// Connect establishes connections to all configured backends.
// implements fail-fast: returns error if any backend fails to connect.
func (m *BackendManager) Connect(ctx context.Context) error {
	for _, bcfg := range m.config.Backends {
		backend, err := m.connectBackend(ctx, bcfg)
		if err != nil {
			_ = m.Close()
			return &BackendError{
				BackendID: bcfg.ID,
				Op:        "connect",
				Err:       err,
			}
		}
		m.mu.Lock()
		m.backends[bcfg.ID] = backend
		m.mu.Unlock()
		dl.Log().With("backend", bcfg.ID).Info("connected to backend")
	}
	return nil
}

// connectBackend establishes a connection to a single backend.
func (m *BackendManager) connectBackend(ctx context.Context, cfg BackendConfig) (*Backend, error) {
	policy, err := NewCallPolicy(cfg.Policy, cfg.Transport.WorkingDir)
	if err != nil {
		return nil, err
	}
	var backend *Backend
	switch cfg.Transport.Type {
	case "stdio":
		backend, err = m.connectStdioBackend(ctx, cfg, policy.WorkingDir())
	case "zrok":
		backend, err = m.connectZrokBackend(ctx, cfg)
	case "agora":
		backend, err = m.connectAgoraBackend(ctx, cfg)
	case "http", "https":
		backend, err = m.connectHTTPBackend(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported transport type '%s'", cfg.Transport.Type)
	}
	if err != nil {
		return nil, err
	}
	if err := policy.ValidateTools(backend.tools); err != nil {
		_ = backend.session.Close()
		if backend.access != nil {
			_ = backend.access.Close()
		}
		return nil, err
	}
	backend.policy = policy
	return backend, nil
}

// connectStdioBackend establishes a connection to a stdio backend.
func (m *BackendManager) connectStdioBackend(ctx context.Context, cfg BackendConfig, workingDir string) (*Backend, error) {
	// create client for this backend
	mcpClient := mcp.NewClient(
		&mcp.Implementation{
			Name:    m.config.Aggregator.Name,
			Version: m.config.Aggregator.Version,
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

	// set environment variables
	cmd.Env = os.Environ()
	for k, v := range cfg.Transport.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	// create transport and connect
	transport := &mcp.CommandTransport{Command: cmd}
	session, err := mcpClient.Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}

	// discover tools from backend
	toolsResult, err := session.ListTools(ctx, nil)
	if err != nil {
		session.Close()
		return nil, err
	}

	name := cfg.Name
	if name == "" {
		name = cfg.ID
	}

	return &Backend{
		id:      cfg.ID,
		name:    name,
		client:  mcpClient,
		session: session,
		tools:   toolsResult.Tools,
	}, nil
}

// connectZrokBackend establishes a connection to a remote zrok backend.
func (m *BackendManager) connectZrokBackend(ctx context.Context, cfg BackendConfig) (*Backend, error) {
	// create zrok access
	access, err := tools.NewAccess(cfg.Transport.ShareToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create zrok access: %w", err)
	}

	// create MCP client
	mcpClient := mcp.NewClient(
		&mcp.Implementation{
			Name:    m.config.Aggregator.Name,
			Version: m.config.Aggregator.Version,
		},
		nil,
	)

	// create SSE transport using zrok HTTP client
	sseTransport := &mcp.SSEClientTransport{
		// the host doesn't matter for routing since zrok handles it
		Endpoint:   "http://mcp-backend/sse",
		HTTPClient: access.HTTPClient(),
	}

	// bound the initial connect window without binding the timeout to the
	// long-lived session itself.
	session, err := ConnectWithTimeout(ctx, m.config.Aggregator.Connection.ConnectTimeout, func(connectCtx context.Context) (*mcp.ClientSession, error) {
		return mcpClient.Connect(connectCtx, sseTransport, nil)
	})
	if err != nil {
		access.Close()
		return nil, fmt.Errorf("failed to connect to zrok backend: %w", err)
	}

	// discover tools from backend
	listCtx, cancel := context.WithTimeout(ctx, m.config.Aggregator.Connection.ConnectTimeout)
	defer cancel()

	toolsResult, err := session.ListTools(listCtx, nil)
	if err != nil {
		session.Close()
		access.Close()
		return nil, fmt.Errorf("failed to list tools from zrok backend: %w", err)
	}

	name := cfg.Name
	if name == "" {
		name = cfg.ID
	}

	dl.Log().With("backend", cfg.ID).With("share_token", cfg.Transport.ShareToken).Info("connected to zrok backend")

	return &Backend{
		id:      cfg.ID,
		name:    name,
		client:  mcpClient,
		session: session,
		tools:   toolsResult.Tools,
		access:  access,
	}, nil
}

// connectAgoraBackend establishes a connection to a remote Agora backend by
// dialing its tunnel directly through the startup-attached shared HTTP client.
func (m *BackendManager) connectAgoraBackend(ctx context.Context, cfg BackendConfig) (*Backend, error) {
	httpClient, err := m.resolveAgoraDialClient(cfg)
	if err != nil {
		return nil, err
	}

	mcpClient := mcp.NewClient(
		&mcp.Implementation{
			Name:    m.config.Aggregator.Name,
			Version: m.config.Aggregator.Version,
		},
		nil,
	)

	sseTransport := &mcp.SSEClientTransport{
		// the host doesn't matter for routing since agora handles it
		Endpoint:   "http://mcp-backend/sse",
		HTTPClient: httpClient,
	}

	session, err := ConnectWithTimeout(ctx, m.config.Aggregator.Connection.ConnectTimeout, func(connectCtx context.Context) (*mcp.ClientSession, error) {
		return mcpClient.Connect(connectCtx, sseTransport, nil)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to agora backend: %w", err)
	}

	listCtx, cancel := context.WithTimeout(ctx, m.config.Aggregator.Connection.ConnectTimeout)
	defer cancel()

	toolsResult, err := session.ListTools(listCtx, nil)
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("failed to list tools from agora backend: %w", err)
	}

	name := cfg.Name
	if name == "" {
		name = cfg.ID
	}

	dl.Log().With("backend", cfg.ID).With("agora_tunnel", cfg.Transport.AgoraTunnel).Info("connected to agora backend")

	return &Backend{
		id:      cfg.ID,
		name:    name,
		client:  mcpClient,
		session: session,
		tools:   toolsResult.Tools,
	}, nil
}

func (m *BackendManager) resolveAgoraDialClient(cfg BackendConfig) (*http.Client, error) {
	if m.agoraDial == nil {
		return nil, fmt.Errorf("agora dial client is not configured")
	}
	tunnel := strings.TrimSpace(cfg.Transport.AgoraTunnel)
	if tunnel == "" {
		return nil, fmt.Errorf("agora tunnel for backend '%s' is required", cfg.ID)
	}
	httpClient, err := m.agoraDial(tunnel)
	if err != nil {
		return nil, fmt.Errorf("agora dial client for backend '%s': %w", cfg.ID, err)
	}
	return httpClient, nil
}

// connectHTTPBackend establishes a connection to a remote HTTP(S) backend.
func (m *BackendManager) connectHTTPBackend(ctx context.Context, cfg BackendConfig) (*Backend, error) {
	connected, err := ConnectHTTPClientSession(ctx, &mcp.Implementation{
		Name:    m.config.Aggregator.Name,
		Version: m.config.Aggregator.Version,
	}, cfg.Transport, m.config.Aggregator.Connection.ConnectTimeout)
	if err != nil {
		return nil, err
	}

	listCtx, cancel := context.WithTimeout(ctx, m.config.Aggregator.Connection.ConnectTimeout)
	defer cancel()

	toolsResult, err := connected.Session.ListTools(listCtx, nil)
	if err != nil {
		connected.Session.Close()
		return nil, fmt.Errorf("failed to list tools from http backend: %w", err)
	}

	name := cfg.Name
	if name == "" {
		name = cfg.ID
	}

	dl.Log().With("backend", cfg.ID).With("endpoint", cfg.Transport.Endpoint).With("transport_type", cfg.Transport.Type).Info("connected to http backend")

	return &Backend{
		id:      cfg.ID,
		name:    name,
		client:  connected.Client,
		session: connected.Session,
		tools:   toolsResult.Tools,
	}, nil
}

// GetBackend returns a backend by ID.
func (m *BackendManager) GetBackend(id string) (*Backend, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.backends[id]
	return b, ok
}

// Close closes all backend connections.
func (m *BackendManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var lastErr error
	for id, b := range m.backends {
		if err := b.session.Close(); err != nil {
			dl.Log().With("backend", id).With("error", err).Warn("error closing backend session")
			lastErr = err
		}
		if b.access != nil {
			if err := b.access.Close(); err != nil {
				dl.Log().With("backend", id).With("error", err).Warn("error closing zrok access")
				lastErr = err
			}
		}
	}
	return lastErr
}

// ID returns the backend's identifier.
func (b *Backend) ID() string {
	return b.id
}

// Name returns the backend's human-readable name.
func (b *Backend) Name() string {
	return b.name
}

// Tools returns the tools available on this backend.
func (b *Backend) Tools() []*mcp.Tool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.tools
}

// Policy returns the immutable policy resolved for this startup connection.
func (b *Backend) Policy() *CallPolicy { return b.policy }

// CallTool invokes a tool on this backend.
func (b *Backend) CallTool(ctx context.Context, name string, args any) (*mcp.CallToolResult, error) {
	settled, err := b.policy.Prepare(name, args)
	if err != nil {
		loggedArgs := settled
		if loggedArgs == nil {
			loggedArgs = args
		}
		dl.Log().With("backend", b.id).With("tool", name).With("args", loggedArgs).With("error", err).Info("tool call denied by policy")
		return PolicyDeniedResult(err), nil
	}
	return b.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: settled,
	})
}
