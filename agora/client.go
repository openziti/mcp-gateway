package agora

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// ClientOptions configures an Agora dial-only client.
type ClientOptions struct {
	Config   *Config
	Defaults Defaults
}

// Client wraps the shared subsystem for dial-only use (mcp-tools). It is the
// gateway dial pattern specialized to a single tunnel and process.
type Client struct {
	opts      ClientOptions
	subsystem *Subsystem
	tunnel    string
}

// NewClient creates a dial-only Agora client.
func NewClient(opts ClientOptions) (*Client, error) {
	if opts.Config == nil || !opts.Config.Enabled {
		return nil, fmt.Errorf("agora config is required")
	}
	if strings.TrimSpace(opts.Defaults.InstanceName) == "" {
		opts.Defaults.InstanceName = "mcp-tools"
	}
	if strings.TrimSpace(opts.Defaults.Description) == "" {
		opts.Defaults.Description = "MCP tools client"
	}
	if strings.TrimSpace(opts.Defaults.AgentNamePrefix) == "" {
		opts.Defaults.AgentNamePrefix = "mcp-tools"
	}

	return &Client{opts: opts}, nil
}

// Attach reserves the dialer attachment for the tunnel once and returns the
// shared HTTP client routed through it. The MCP HTTP/SSE transport binds the
// returned client directly; the dummy host it is given is ignored by the
// dialer's DialContext.
func (c *Client) Attach(ctx context.Context, service string) (*http.Client, error) {
	if c == nil {
		return nil, fmt.Errorf("agora client is nil")
	}
	service = strings.TrimSpace(service)
	if service == "" {
		return nil, fmt.Errorf("agora tunnel is required")
	}
	if c.subsystem != nil {
		return nil, fmt.Errorf("agora client already attached")
	}

	subsystem, err := NewSubsystem(SubsystemOptions{
		Config:   c.opts.Config,
		Defaults: c.opts.Defaults,
	})
	if err != nil {
		return nil, err
	}
	if subsystem == nil {
		return nil, fmt.Errorf("agora subsystem was not initialized")
	}
	c.subsystem = subsystem
	c.tunnel = service

	if err := subsystem.Dialer().Attach(ctx, service); err != nil {
		return nil, err
	}
	return subsystem.Dialer().HTTPClient(service)
}

// HTTPClient returns the shared client for the attached tunnel.
func (c *Client) HTTPClient() (*http.Client, error) {
	if c == nil || c.subsystem == nil {
		return nil, fmt.Errorf("agora client is not attached")
	}
	return c.subsystem.Dialer().HTTPClient(c.tunnel)
}

// Close detaches the tunnel and tears down the dial subsystem.
func (c *Client) Close() error {
	if c == nil || c.subsystem == nil {
		return nil
	}
	return c.subsystem.Close()
}
