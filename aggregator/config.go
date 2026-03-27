package aggregator

import (
	"fmt"
	"time"

	"github.com/michaelquigley/df/dd"
)

// Config represents the top-level aggregator configuration.
type Config struct {
	Aggregator AggregatorConfig
	Backends   []BackendConfig
}

// AggregatorConfig contains settings for the aggregator itself.
type AggregatorConfig struct {
	Name       string
	Version    string
	Separator  string
	Connection ConnectionConfig
}

// ConnectionConfig defines timeout settings.
type ConnectionConfig struct {
	ConnectTimeout time.Duration
	CallTimeout    time.Duration
}

// BackendConfig defines a single backend MCP server.
type BackendConfig struct {
	ID        string
	Name      string
	Transport TransportConfig
	Tools     ToolFilterConfig
}

// TransportConfig specifies how to connect to a backend.
type TransportConfig struct {
	Type string
	// stdio transport fields
	Command    string
	Args       []string
	Env        map[string]string
	WorkingDir string
	// zrok transport fields
	ShareToken string
	// https transport fields
	Endpoint string
	Protocol string            // "sse" (default) or "streamable"
	Headers  map[string]string
	TLS      *TLSConfig
}

// TLSConfig provides optional TLS settings for HTTPS backends.
type TLSConfig struct {
	InsecureSkipVerify bool
	CACertFile         string
}

// ToolFilterConfig defines which tools are permitted.
type ToolFilterConfig struct {
	Mode string
	List []string
}

// DefaultConfig returns a Config with all defaults pre-populated.
func DefaultConfig() *Config {
	return &Config{
		Aggregator: AggregatorConfig{
			Name:      "mcp-aggregator",
			Version:   "1.0.0",
			Separator: "_",
			Connection: ConnectionConfig{
				ConnectTimeout: 30 * time.Second,
				CallTimeout:    60 * time.Second,
			},
		},
	}
}

// LoadConfig loads configuration from a YAML file, merging into defaults.
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()
	if err := dd.MergeYAMLFile(cfg, path); err != nil {
		return nil, &ConfigError{Field: "file", Message: fmt.Sprintf("failed to load '%s': %v", path, err)}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	if len(c.Backends) == 0 {
		return &ConfigError{Field: "backends", Message: "at least one backend is required"}
	}

	seen := make(map[string]bool)
	for i, b := range c.Backends {
		if b.ID == "" {
			return &ConfigError{
				Field:   fmt.Sprintf("backends[%d].id", i),
				Message: "backend id is required",
			}
		}
		if seen[b.ID] {
			return &ConfigError{
				Field:   fmt.Sprintf("backends[%d].id", i),
				Message: fmt.Sprintf("duplicate backend id '%s'", b.ID),
			}
		}
		seen[b.ID] = true

		if b.Transport.Type == "" {
			return &ConfigError{
				Field:   fmt.Sprintf("backends[%d].transport.type", i),
				Message: "transport type is required",
			}
		}
		switch b.Transport.Type {
		case "stdio":
			if b.Transport.Command == "" {
				return &ConfigError{
					Field:   fmt.Sprintf("backends[%d].transport.command", i),
					Message: "command is required for stdio transport",
				}
			}
		case "zrok":
			if b.Transport.ShareToken == "" {
				return &ConfigError{
					Field:   fmt.Sprintf("backends[%d].transport.share_token", i),
					Message: "share_token is required for zrok transport",
				}
			}
		case "https":
			if b.Transport.Endpoint == "" {
				return &ConfigError{
					Field:   fmt.Sprintf("backends[%d].transport.endpoint", i),
					Message: "endpoint is required for https transport",
				}
			}
			if b.Transport.Protocol != "" && b.Transport.Protocol != "sse" && b.Transport.Protocol != "streamable" {
				return &ConfigError{
					Field:   fmt.Sprintf("backends[%d].transport.protocol", i),
					Message: fmt.Sprintf("unsupported protocol '%s', must be 'sse' or 'streamable'", b.Transport.Protocol),
				}
			}
		default:
			return &ConfigError{
				Field:   fmt.Sprintf("backends[%d].transport.type", i),
				Message: fmt.Sprintf("unsupported transport type '%s', must be 'stdio', 'zrok', or 'https'", b.Transport.Type),
			}
		}

		if b.Tools.Mode != "" && b.Tools.Mode != "allow" && b.Tools.Mode != "deny" {
			return &ConfigError{
				Field:   fmt.Sprintf("backends[%d].tools.mode", i),
				Message: fmt.Sprintf("invalid tool filter mode '%s', must be 'allow' or 'deny'", b.Tools.Mode),
			}
		}
	}

	return nil
}
