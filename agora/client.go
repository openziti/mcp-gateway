package agora

import (
	"context"
	"fmt"
	"strings"
)

const dialTargetKey = "target"

// ClientOptions configures an Agora dial-only client.
type ClientOptions struct {
	Config   *Config
	Defaults Defaults
}

// Client wraps the shared subsystem for dial-only use.
type Client struct {
	opts      ClientOptions
	subsystem *Subsystem
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
	if strings.TrimSpace(opts.Defaults.TunnelMode) == "" {
		opts.Defaults.TunnelMode = "tcp"
	}
	if strings.TrimSpace(opts.Defaults.AgentNamePrefix) == "" {
		opts.Defaults.AgentNamePrefix = "mcp-tools"
	}

	return &Client{opts: opts}, nil
}

// Dial connects to an Agora Layer 1 tunnel and returns the local loopback address.
func (c *Client) Dial(ctx context.Context, service string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("agora client is nil")
	}
	service = strings.TrimSpace(service)
	if service == "" {
		return "", fmt.Errorf("agora tunnel is required")
	}
	if c.subsystem != nil {
		return "", fmt.Errorf("agora client already dialed")
	}

	subsystem, err := NewSubsystem(SubsystemOptions{
		Config:   c.opts.Config,
		Defaults: c.opts.Defaults,
		ConnectTargets: []ConnectTarget{{
			Key:    dialTargetKey,
			Tunnel: service,
		}},
		ServeWanted:   false,
		PublishWanted: false,
	})
	if err != nil {
		return "", err
	}
	if subsystem == nil {
		return "", fmt.Errorf("agora subsystem was not initialized")
	}
	c.subsystem = subsystem

	if err := c.subsystem.BootstrapConnects(ctx); err != nil {
		return "", err
	}
	loopbackAddr, ok := c.subsystem.ConnectAddress(dialTargetKey)
	if !ok {
		return "", fmt.Errorf("agora connect address for tunnel '%s' was not initialized", service)
	}
	return loopbackAddr, nil
}

// Close closes any active Agora dial resources.
func (c *Client) Close() error {
	if c == nil || c.subsystem == nil {
		return nil
	}
	return c.subsystem.Close()
}
