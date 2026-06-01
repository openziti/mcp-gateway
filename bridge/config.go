package bridge

import (
	"fmt"

	mcpagora "github.com/openziti/mcp-gateway/agora"
)

// Config holds the configuration for a single tool backend.
type Config struct {
	Command            string
	Args               []string
	Env                map[string]string
	WorkingDir         string
	ShareToken         string
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
	Enabled bool
}

// Validate ensures the config is valid.
func (c *Config) Validate() error {
	if c.Command == "" {
		return fmt.Errorf("command is required")
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

// ZrokShareEnabled reports whether the bridge should serve over zrok.
func (c *Config) ZrokShareEnabled() bool {
	if c == nil || c.Zrok == nil || c.Zrok.Share == nil {
		return true
	}
	return c.Zrok.Share.Enabled
}

// AgoraServeEnabled reports whether the bridge should serve over Agora.
func (c *Config) AgoraServeEnabled() bool {
	return c != nil && c.Agora != nil && c.Agora.Enabled && mcpagora.ServeEnabled(c.Agora)
}

// AgoraPublishEnabled reports whether the bridge should publish to the Agora catalog.
func (c *Config) AgoraPublishEnabled() bool {
	return c != nil && c.Agora != nil && c.Agora.Enabled && mcpagora.AdvertisementPublish(c.Agora)
}
