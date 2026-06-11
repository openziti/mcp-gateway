package tools

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/michaelquigley/df/dl"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	mcpagora "github.com/openziti/mcp-gateway/agora"
)

// Client bridges stdio MCP to a remote backend over zrok or Agora.
type Client struct {
	shareToken  string
	agoraTunnel string
	agoraConfig *mcpagora.Config
	access      *Access
	agoraClient *mcpagora.Client
	mcpClient   *mcp.Client
	session     *mcp.ClientSession
	server      *mcp.Server
}

// New creates a Client for the given share token.
func New(shareToken string) (*Client, error) {
	if shareToken == "" {
		return nil, fmt.Errorf("share token is required")
	}

	return &Client{
		shareToken: shareToken,
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
	}, nil
}

// Start connects to the backend and discovers tools.
func (c *Client) Start(ctx context.Context) error {
	dl.Log().Debug("starting mcp-tools")

	transport, err := c.buildTransport(ctx)
	if err != nil {
		return err
	}
	c.mcpClient = mcp.NewClient(
		&mcp.Implementation{
			Name:    "mcp-tools",
			Version: "1.0.0",
		},
		nil,
	)

	// connect to backend
	session, err := c.mcpClient.Connect(ctx, transport, nil)
	if err != nil {
		c.closeTransport()
		return fmt.Errorf("failed to connect to backend: %w", err)
	}
	c.session = session

	dl.Log().Debug("connected to backend")

	// discover tools from backend
	toolsResult, err := session.ListTools(ctx, nil)
	if err != nil {
		c.session.Close()
		c.closeTransport()
		return fmt.Errorf("failed to list tools: %w", err)
	}

	dl.Log().With("toolCount", len(toolsResult.Tools)).Debug("discovered tools from backend")

	// create proxy server with discovered tools
	c.server = c.createProxyServer(toolsResult.Tools)

	dl.Log().Debug("mcp-tools started")
	return nil
}

func (c *Client) buildTransport(ctx context.Context) (mcp.Transport, error) {
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
		return &mcp.SSEClientTransport{
			// the host doesn't matter for routing since agora handles it
			Endpoint:   "http://mcp-backend/sse",
			HTTPClient: httpClient,
		}, nil
	}

	dl.Log().With("share_token", c.shareToken).Debug("creating zrok access")
	access, err := NewAccess(c.shareToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create access: %w", err)
	}
	c.access = access
	return &mcp.SSEClientTransport{
		// the host doesn't matter for routing since zrok handles it
		Endpoint:   "http://mcp-backend/sse",
		HTTPClient: access.HTTPClient(),
	}, nil
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

// createProxyServer creates an MCP server that proxies to the backend.
func (c *Client) createProxyServer(tools []*mcp.Tool) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "mcp-tools",
			Version: "1.0.0",
		},
		nil,
	)

	for _, tool := range tools {
		t := tool
		server.AddTool(t, c.createProxyHandler(t.Name))
		dl.Log().With("tool", t.Name).Debug("registered proxy handler")
	}

	return server
}

// createProxyHandler creates a handler that forwards tool calls to the backend.
func (c *Client) createProxyHandler(toolName string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		dl.Log().With("tool", toolName).Debug("forwarding tool call to backend")

		result, err := c.session.CallTool(ctx, &mcp.CallToolParams{
			Name:      toolName,
			Arguments: req.Params.Arguments,
		})
		if err != nil {
			dl.Log().With("tool", toolName).With("error", err).Debug("tool call failed")
			return nil, err
		}

		dl.Log().With("tool", toolName).Debug("tool call completed")
		return result, nil
	}
}

// Run serves MCP on stdio (blocks until context cancelled).
func (c *Client) Run(ctx context.Context) error {
	if c.server == nil {
		return fmt.Errorf("client not started, call Start() first")
	}

	dl.Log().Debug("serving mcp on stdio")

	return c.server.Run(ctx, &mcp.StdioTransport{})
}

// HTTPOptions configures the HTTP server mode.
type HTTPOptions struct {
	Address      string // bind address (e.g., "127.0.0.1:8080")
	Stateless    bool   // if true, don't track sessions
	JSONResponse bool   // if true, prefer JSON over SSE responses
}

// RunHTTP serves MCP over HTTP (blocks until context cancelled).
func (c *Client) RunHTTP(ctx context.Context, opts *HTTPOptions) error {
	if c.server == nil {
		return fmt.Errorf("client not started, call Start() first")
	}

	if opts == nil {
		opts = &HTTPOptions{Address: "127.0.0.1:8080"}
	}

	dl.Log().With("address", opts.Address).Debug("serving MCP on http")

	handler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return c.server },
		&mcp.StreamableHTTPOptions{
			Stateless:    opts.Stateless,
			JSONResponse: opts.JSONResponse,
		},
	)

	httpServer := &http.Server{
		Addr:    opts.Address,
		Handler: handler,
	}

	// graceful shutdown on context cancellation
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()

	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Stop gracefully shuts down the client.
func (c *Client) Stop() error {
	dl.Log().Debug("stopping mcp-tools")

	var lastErr error

	if c.session != nil {
		if err := c.session.Close(); err != nil {
			dl.Log().With("error", err).Debug("error closing session")
			lastErr = err
		}
	}

	if c.access != nil {
		if err := c.access.Close(); err != nil {
			dl.Log().With("error", err).Debug("error closing access")
			lastErr = err
		}
	}

	if c.agoraClient != nil {
		if err := c.agoraClient.Close(); err != nil {
			dl.Log().With("error", err).Debug("error closing agora client")
			lastErr = err
		}
	}

	dl.Log().Debug("mcp-tools stopped")
	return lastErr
}
