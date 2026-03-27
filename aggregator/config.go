package aggregator

import (
	"fmt"
	"net/url"
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
	// http(s) transport fields
	Endpoint      string
	Protocol      string // "sse" (default) or "streamable"
	Headers       map[string]string
	AllowInsecure bool
	TLS           *TLSConfig
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
		case "https", "http":
			if err := validateHTTPTransport(b.Transport, i); err != nil {
				return err
			}
		default:
			return &ConfigError{
				Field:   fmt.Sprintf("backends[%d].transport.type", i),
				Message: fmt.Sprintf("unsupported transport type '%s', must be 'stdio', 'zrok', 'https', or 'http'", b.Transport.Type),
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

func validateHTTPTransport(transport TransportConfig, index int) error {
	if transport.Endpoint == "" {
		return &ConfigError{
			Field:   fmt.Sprintf("backends[%d].transport.endpoint", index),
			Message: fmt.Sprintf("endpoint is required for %s transport", transport.Type),
		}
	}

	endpoint, err := url.Parse(transport.Endpoint)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return &ConfigError{
			Field:   fmt.Sprintf("backends[%d].transport.endpoint", index),
			Message: fmt.Sprintf("invalid endpoint url for '%s' transport", transport.Type),
		}
	}

	switch transport.Type {
	case "https":
		if endpoint.Scheme != "https" {
			return &ConfigError{
				Field:   fmt.Sprintf("backends[%d].transport.endpoint", index),
				Message: "endpoint scheme must be 'https' for https transport",
			}
		}
	case "http":
		if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
			return &ConfigError{
				Field:   fmt.Sprintf("backends[%d].transport.endpoint", index),
				Message: "endpoint scheme must be 'http' or 'https' for http transport",
			}
		}
		if endpoint.Scheme == "http" && !transport.AllowInsecure {
			return &ConfigError{
				Field:   fmt.Sprintf("backends[%d].transport.allow_insecure", index),
				Message: "allow_insecure must be true for http endpoints",
			}
		}
		if endpoint.Scheme == "http" && transport.TLS != nil {
			return &ConfigError{
				Field:   fmt.Sprintf("backends[%d].transport.tls", index),
				Message: "tls configuration is only valid for https endpoints",
			}
		}
	}

	if transport.Protocol != "" && transport.Protocol != "sse" && transport.Protocol != "streamable" {
		return &ConfigError{
			Field:   fmt.Sprintf("backends[%d].transport.protocol", index),
			Message: fmt.Sprintf("unsupported protocol '%s', must be 'sse' or 'streamable'", transport.Protocol),
		}
	}

	return nil
}
