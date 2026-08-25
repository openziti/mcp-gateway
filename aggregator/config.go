package aggregator

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
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
	Policy    PolicyConfig
}

// PolicyConfig contains argument-aware rules enforced before a tool call is
// forwarded to its backend. An empty policy preserves the gateway's existing
// filter-only behavior.
type PolicyConfig struct {
	Paths []PathPolicyConfig
}

// PathPolicyConfig confines one tool argument to one of the configured roots.
// tool names are backend-original names, before gateway namespacing.
type PathPolicyConfig struct {
	Tool     string
	Argument string
	Roots    []string
}

// TransportConfig specifies how to connect to a backend.
type TransportConfig struct {
	Type string
	// stdio transport fields
	Command    string
	Args       []string
	Env        map[string]string
	EnvPolicy  string // "additive" (default) inherits the gateway environment; "closed" starts from exactly Env
	WorkingDir string
	// zrok transport fields
	ShareToken string
	// agora transport fields
	AgoraTunnel string
	// http(s) transport fields
	Endpoint              string
	Protocol              string // "streamable" (default) or "sse"
	Headers               map[string]string
	AllowInsecure         bool
	AllowEnvironmentProxy bool
	AllowRedirects        bool
	TLS                   *TLSConfig
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
			if err := validateEnvPolicy(b.Transport, i); err != nil {
				return err
			}
			if err := rejectHTTPBehaviorOptIns(b.Transport, i); err != nil {
				return err
			}
		case "zrok":
			if b.Transport.ShareToken == "" {
				return &ConfigError{
					Field:   fmt.Sprintf("backends[%d].transport.share_token", i),
					Message: "share_token is required for zrok transport",
				}
			}
			if err := validateMCPProtocol(b.Transport, i); err != nil {
				return err
			}
			if err := rejectHTTPBehaviorOptIns(b.Transport, i); err != nil {
				return err
			}
		case "agora":
			if err := validateAgoraTransport(b.Transport, i); err != nil {
				return err
			}
		case "https", "http":
			if err := validateHTTPTransport(b.Transport, i); err != nil {
				return err
			}
		default:
			return &ConfigError{
				Field:   fmt.Sprintf("backends[%d].transport.type", i),
				Message: fmt.Sprintf("unsupported transport type '%s', must be 'stdio', 'zrok', 'agora', 'https', or 'http'", b.Transport.Type),
			}
		}

		if b.Tools.Mode != "" && b.Tools.Mode != "allow" && b.Tools.Mode != "deny" {
			return &ConfigError{
				Field:   fmt.Sprintf("backends[%d].tools.mode", i),
				Message: fmt.Sprintf("invalid tool filter mode '%s', must be 'allow' or 'deny'", b.Tools.Mode),
			}
		}

		if err := validatePolicy(b, i); err != nil {
			return err
		}
	}

	return nil
}

func rejectHTTPBehaviorOptIns(transport TransportConfig, index int) error {
	if transport.AllowEnvironmentProxy {
		return &ConfigError{
			Field:   fmt.Sprintf("backends[%d].transport.allow_environment_proxy", index),
			Message: "allow_environment_proxy applies only to http and https transports",
		}
	}
	if transport.AllowRedirects {
		return &ConfigError{
			Field:   fmt.Sprintf("backends[%d].transport.allow_redirects", index),
			Message: "allow_redirects applies only to http and https transports",
		}
	}
	return nil
}

func validatePolicy(backend BackendConfig, index int) error {
	if len(backend.Policy.Paths) == 0 {
		return nil
	}
	if backend.Transport.Type != "stdio" {
		return &ConfigError{
			Field:   fmt.Sprintf("backends[%d].policy", index),
			Message: "path policy requires a colocated stdio backend",
		}
	}

	seenRules := map[string]bool{}
	for ruleIndex, rule := range backend.Policy.Paths {
		field := fmt.Sprintf("backends[%d].policy.paths[%d]", index, ruleIndex)
		if rule.Tool == "" || strings.TrimSpace(rule.Tool) != rule.Tool {
			return &ConfigError{Field: field + ".tool", Message: "tool is required and cannot have surrounding whitespace"}
		}
		if rule.Argument == "" || strings.TrimSpace(rule.Argument) != rule.Argument {
			return &ConfigError{Field: field + ".argument", Message: "argument is required and cannot have surrounding whitespace"}
		}
		key := rule.Tool + "\x00" + rule.Argument
		if seenRules[key] {
			return &ConfigError{Field: field, Message: fmt.Sprintf("duplicate path rule for tool %q argument %q", rule.Tool, rule.Argument)}
		}
		seenRules[key] = true
		if len(rule.Roots) == 0 {
			return &ConfigError{Field: field + ".roots", Message: "at least one root is required"}
		}
		seenRoots := map[string]bool{}
		for rootIndex, root := range rule.Roots {
			rootField := fmt.Sprintf("%s.roots[%d]", field, rootIndex)
			if !filepath.IsAbs(root) {
				return &ConfigError{Field: rootField, Message: "root must be absolute"}
			}
			if filepath.Clean(root) != root {
				return &ConfigError{Field: rootField, Message: "root must be clean"}
			}
			if seenRoots[root] {
				return &ConfigError{Field: rootField, Message: fmt.Sprintf("duplicate root %q", root)}
			}
			seenRoots[root] = true
		}
	}
	return nil
}

func validateAgoraTransport(transport TransportConfig, index int) error {
	if strings.TrimSpace(transport.AgoraTunnel) == "" {
		return &ConfigError{
			Field:   fmt.Sprintf("backends[%d].transport.agora_tunnel", index),
			Message: "agora_tunnel is required for agora transport",
		}
	}

	if transport.Command != "" || len(transport.Args) > 0 || len(transport.Env) > 0 || transport.WorkingDir != "" ||
		transport.ShareToken != "" || transport.Endpoint != "" || len(transport.Headers) > 0 ||
		transport.AllowInsecure || transport.AllowEnvironmentProxy || transport.AllowRedirects || transport.TLS != nil {
		return &ConfigError{
			Field:   fmt.Sprintf("backends[%d].transport", index),
			Message: "agora transport cannot set stdio, zrok, or http transport fields",
		}
	}

	return validateMCPProtocol(transport, index)
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

	return validateMCPProtocol(transport, index)
}

func validateMCPProtocol(transport TransportConfig, index int) error {
	if transport.Protocol != "" && transport.Protocol != "sse" && transport.Protocol != "streamable" {
		return &ConfigError{
			Field:   fmt.Sprintf("backends[%d].transport.protocol", index),
			Message: fmt.Sprintf("unsupported protocol '%s', must be 'sse' or 'streamable'", transport.Protocol),
		}
	}

	return nil
}
