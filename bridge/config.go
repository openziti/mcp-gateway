package bridge

import (
	"fmt"
	"time"

	mcpagora "github.com/openziti/mcp-gateway/agora"
	"github.com/openziti/mcp-gateway/streamable"
)

// Config holds the configuration for a single tool backend.
type Config struct {
	Command            string
	Args               []string
	Env                map[string]string
	WorkingDir         string
	ShareToken         string
	SessionIdleTimeout *time.Duration
	AgoraCapabilityTag string
	Zrok               *ZrokConfig
	Agora              *mcpagora.Config
}

// ZrokConfig holds zrok-specific bridge configuration.
type ZrokConfig struct {
	Share *ZrokShareConfig
}

// ZrokShareConfig controls zrok share serving.
type ZrokShareConfig struct {
	Enabled      bool
	AccessGrants []string
}

// Validate ensures the config is valid.
func (c *Config) Validate() error {
	if c.Command == "" {
		return fmt.Errorf("command is required")
	}
	if c.SessionIdleTimeout != nil && *c.SessionIdleTimeout < 0 {
		return fmt.Errorf("session idle timeout must not be negative")
	}
	if len(c.ZrokShareAccessGrants()) > 0 {
		if !c.ZrokShareEnabled() {
			return fmt.Errorf("zrok.share.access_grants require zrok share serving to be enabled")
		}
		if c.ShareToken != "" {
			return fmt.Errorf("zrok.share.access_grants apply only to newly created shares and cannot be used with share_token")
		}
	}
	if c.ShareToken != "" && !c.ZrokShareEnabled() {
		return fmt.Errorf("share_token requires zrok.share.enabled")
	}
	if c.ZrokShareEnabled() && c.AgoraServeEnabled() {
		return fmt.Errorf("mcp-bridge supports one network per invocation; disable zrok.share.enabled when agora.serve.enabled is true")
	}
	if !c.ZrokShareEnabled() && !c.AgoraServeEnabled() {
		return fmt.Errorf("at least one of zrok.share.enabled or agora.serve.enabled must be true")
	}
	if c.AgoraPublishEnabled() && !c.AgoraServeEnabled() {
		return fmt.Errorf("agora.advertisement.publish requires agora.serve.enabled for mcp-bridge")
	}
	return nil
}

// EffectiveSessionIdleTimeout returns the configured Streamable HTTP idle
// timeout, applying the default when it is unset.
func (c *Config) EffectiveSessionIdleTimeout() time.Duration {
	if c == nil || c.SessionIdleTimeout == nil {
		return streamable.DefaultSessionIdleTimeout
	}
	return *c.SessionIdleTimeout
}

// ZrokShareEnabled reports whether the bridge should serve over zrok.
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

// AgoraServeEnabled reports whether the bridge should serve over Agora.
func (c *Config) AgoraServeEnabled() bool {
	return c != nil && c.Agora != nil && c.Agora.Enabled && mcpagora.ServeEnabled(c.Agora)
}

// AgoraPublishEnabled reports whether the bridge should publish to the Agora catalog.
func (c *Config) AgoraPublishEnabled() bool {
	return c != nil && c.Agora != nil && c.Agora.Enabled && mcpagora.AdvertisementPublish(c.Agora)
}
