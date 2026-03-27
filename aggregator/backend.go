package aggregator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
	mu      sync.RWMutex
}

// BackendManager manages connections to multiple backend MCP servers.
type BackendManager struct {
	backends map[string]*Backend
	config   *Config
	mu       sync.RWMutex
}

// NewBackendManager creates a new manager for backend connections.
func NewBackendManager(cfg *Config) *BackendManager {
	return &BackendManager{
		backends: make(map[string]*Backend),
		config:   cfg,
	}
}

// Connect establishes connections to all configured backends.
// implements fail-fast: returns error if any backend fails to connect.
func (m *BackendManager) Connect(ctx context.Context) error {
	for _, bcfg := range m.config.Backends {
		backend, err := m.connectBackend(ctx, bcfg)
		if err != nil {
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
	switch cfg.Transport.Type {
	case "stdio":
		return m.connectStdioBackend(ctx, cfg)
	case "zrok":
		return m.connectZrokBackend(ctx, cfg)
	case "https":
		return m.connectHttpsBackend(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported transport type '%s'", cfg.Transport.Type)
	}
}

// connectStdioBackend establishes a connection to a stdio backend.
func (m *BackendManager) connectStdioBackend(ctx context.Context, cfg BackendConfig) (*Backend, error) {
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

// connectHttpsBackend establishes a connection to a remote HTTPS backend.
func (m *BackendManager) connectHttpsBackend(ctx context.Context, cfg BackendConfig) (*Backend, error) {
	mcpClient := mcp.NewClient(
		&mcp.Implementation{
			Name:    m.config.Aggregator.Name,
			Version: m.config.Aggregator.Version,
		},
		nil,
	)

	httpClient, err := BuildHTTPSClient(cfg.Transport)
	if err != nil {
		return nil, fmt.Errorf("failed to build http client: %w", err)
	}

	transport, err := BuildMCPTransport(cfg.Transport, httpClient)
	if err != nil {
		return nil, fmt.Errorf("failed to build mcp transport: %w", err)
	}

	session, err := ConnectWithTimeout(ctx, m.config.Aggregator.Connection.ConnectTimeout, func(connectCtx context.Context) (*mcp.ClientSession, error) {
		return mcpClient.Connect(connectCtx, transport, nil)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to https backend: %w", err)
	}

	listCtx, cancel := context.WithTimeout(ctx, m.config.Aggregator.Connection.ConnectTimeout)
	defer cancel()

	toolsResult, err := session.ListTools(listCtx, nil)
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("failed to list tools from https backend: %w", err)
	}

	name := cfg.Name
	if name == "" {
		name = cfg.ID
	}

	dl.Log().With("backend", cfg.ID).With("endpoint", cfg.Transport.Endpoint).Info("connected to https backend")

	return &Backend{
		id:      cfg.ID,
		name:    name,
		client:  mcpClient,
		session: session,
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

// CallTool invokes a tool on this backend.
func (b *Backend) CallTool(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	return b.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
}
