package gateway

import (
	"fmt"
	"time"

	"github.com/michaelquigley/df/dd"
	"github.com/openziti/mcp-gateway/aggregator"
	"github.com/openziti/mcp-gateway/agora"
)

// Config represents the share backend configuration.
type Config struct {
	Aggregator         aggregator.AggregatorConfig
	Backends           []aggregator.BackendConfig
	SessionIdleTimeout *time.Duration
	Zrok               *ZrokConfig
	Agora              *agora.Config
	ShareToken         string // if set, use existing share (managed mode)
	Orchestrator       *OrchestratorConfig
	LogFile            string // if set, redirect logging to this file
}

// ZrokConfig holds zrok-specific gateway configuration.
type ZrokConfig struct {
	Share *ZrokShareConfig
}

// ZrokShareConfig controls zrok share serving.
type ZrokShareConfig struct {
	Enabled      bool
	AccessGrants []string
}

// OrchestratorConfig holds configuration for connecting to the orchestrator.
type OrchestratorConfig struct {
	SocketPath        string
	HeartbeatInterval time.Duration
}

// DefaultOrchestratorConfig returns default orchestrator connection configuration.
func DefaultOrchestratorConfig() *OrchestratorConfig {
	return &OrchestratorConfig{
		SocketPath:        "/var/run/mcp-orchestrator/orchestrator.sock",
		HeartbeatInterval: 30 * time.Second,
	}
}

// DefaultConfig returns a Config with defaults pre-populated.
func DefaultConfig() *Config {
	aggDefaults := aggregator.DefaultConfig()
	sessionIdleTimeout := DefaultSessionIdleTimeout
	return &Config{
		Aggregator:         aggDefaults.Aggregator,
		SessionIdleTimeout: &sessionIdleTimeout,
		Zrok: &ZrokConfig{
			Share: &ZrokShareConfig{
				Enabled: true,
			},
		},
	}
}

// LoadConfigRaw loads configuration from a YAML file without resolving or validating.
func LoadConfigRaw(path string) (*Config, error) {
	cfg := DefaultConfig()
	if err := dd.MergeYAMLFile(cfg, path); err != nil {
		return nil, &ConfigError{Field: "file", Message: fmt.Sprintf("failed to load '%s': %v", path, err)}
	}
	return cfg, nil
}

// LoadConfig loads configuration from a YAML file, merging into defaults.
func LoadConfig(path string) (*Config, error) {
	cfg, err := LoadConfigRaw(path)
	if err != nil {
		return nil, err
	}
	if err := agora.ResolveConfig(cfg.Agora); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	return c.validate(false)
}

func (c *Config) validate(hasLocalListener bool) error {
	if c == nil {
		return &ConfigError{Field: "config", Message: "configuration is required"}
	}
	if len(c.Backends) == 0 {
		return &ConfigError{Field: "backends", Message: "at least one backend is required"}
	}
	if c.SessionIdleTimeout != nil && *c.SessionIdleTimeout < 0 {
		return &ConfigError{Field: "session_idle_timeout", Message: "must not be negative"}
	}

	if len(c.ZrokShareAccessGrants()) > 0 {
		if !c.ZrokShareEnabled() {
			return &ConfigError{Field: "zrok.share.access_grants", Message: "access grants require zrok share serving to be enabled"}
		}
		if c.ShareToken != "" {
			return &ConfigError{Field: "zrok.share.access_grants", Message: "access grants apply only to newly created shares and cannot be used with share_token"}
		}
	}

	if c.ShareToken != "" && !c.ZrokShareEnabled() {
		return &ConfigError{Field: "share_token", Message: "share_token requires zrok.share.enabled"}
	}

	if c.agoraPublishExplicitlyEnabled() && !c.AgoraServeEnabled() {
		return &ConfigError{Field: "agora.advertisement.publish", Message: "explicit publish=true requires agora.serve.enabled"}
	}

	if !c.ZrokShareEnabled() && !c.AgoraServeEnabled() && !hasLocalListener {
		return &ConfigError{Field: "network", Message: "at least one of zrok.share.enabled or agora.serve.enabled must be true"}
	}

	// validate backends using aggregator's validation logic
	aggCfg := c.toAggregatorConfig()
	if err := aggCfg.Validate(); err != nil {
		return err
	}

	if c.hasAgoraBackends() && (c.Agora == nil || !c.Agora.Enabled) {
		return &ConfigError{Field: "agora.enabled", Message: "agora transport backends require agora.enabled"}
	}

	return nil
}

// EffectiveSessionIdleTimeout returns the configured Streamable HTTP idle
// timeout, applying the default when it is unset.
func (c *Config) EffectiveSessionIdleTimeout() time.Duration {
	if c == nil || c.SessionIdleTimeout == nil {
		return DefaultSessionIdleTimeout
	}
	return *c.SessionIdleTimeout
}

// toAggregatorConfig converts to an aggregator.Config for reuse.
func (c *Config) toAggregatorConfig() *aggregator.Config {
	return &aggregator.Config{
		Aggregator: c.Aggregator,
		Backends:   c.Backends,
	}
}

// ZrokShareEnabled reports whether the gateway should serve over zrok.
func (c *Config) ZrokShareEnabled() bool {
	if c == nil || c.Zrok == nil || c.Zrok.Share == nil {
		return true
	}
	return c.Zrok.Share.Enabled
}

// ZrokShareAccessGrants returns the accounts granted access to a newly
// created closed share. an absent share block means owner-only.
func (c *Config) ZrokShareAccessGrants() []string {
	if c == nil || c.Zrok == nil || c.Zrok.Share == nil {
		return nil
	}
	return c.Zrok.Share.AccessGrants
}

// AgoraServeEnabled reports whether the gateway should serve over Agora.
func (c *Config) AgoraServeEnabled() bool {
	return c != nil && c.Agora != nil && c.Agora.Enabled && agora.ServeEnabled(c.Agora)
}

// AgoraPublishEnabled reports whether the gateway should publish to the Agora catalog.
func (c *Config) AgoraPublishEnabled() bool {
	return c.AgoraServeEnabled() && agora.AdvertisementPublish(c.Agora)
}

// agoraPublishDefaultedOffWithoutServe reports the non-error case where Agora
// is enabled but advertisement publishing was left unset and serving is off.
// the gateway may still use Agora to dial backends; it just has no local Agora
// endpoint that can be advertised honestly.
func (c *Config) agoraPublishDefaultedOffWithoutServe() bool {
	return c != nil && c.Agora != nil && c.Agora.Enabled &&
		!c.AgoraServeEnabled() &&
		(c.Agora.Advertisement == nil || c.Agora.Advertisement.Publish == nil)
}

func (c *Config) agoraPublishExplicitlyEnabled() bool {
	return c != nil && c.Agora != nil &&
		c.Agora.Advertisement != nil &&
		c.Agora.Advertisement.Publish != nil &&
		*c.Agora.Advertisement.Publish
}

func (c *Config) hasAgoraBackends() bool {
	if c == nil {
		return false
	}
	for _, backend := range c.Backends {
		if backend.Transport.Type == "agora" {
			return true
		}
	}
	return false
}

// ConfigError represents a configuration validation error.
type ConfigError struct {
	Field   string
	Message string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("configuration error in '%s': %s", e.Field, e.Message)
}
