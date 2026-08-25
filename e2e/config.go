//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gateway configuration is written as YAML text rather than marshalled from
// gateway.Config, so the suite exercises the documented operator surface — the
// same file shape README and getting-started tell people to write.

// gatewaySpec describes one gateway configuration under test.
type gatewaySpec struct {
	name               string
	separator          string
	zrokShare          bool
	shareToken         string // bind an existing named share
	agoraEnabled       bool
	agoraServe         bool
	agoraTunnel        string
	sessionIdleTimeout string // e.g. "5s"; empty leaves the default
	backends           []string
}

// write renders the spec to a temp file and returns its path.
func (s gatewaySpec) write(t *testing.T) string {
	t.Helper()

	separator := s.separator
	if separator == "" {
		separator = ":"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "zrok:\n  share:\n    enabled: %t\n\n", s.zrokShare)
	if s.shareToken != "" {
		fmt.Fprintf(&b, "share_token: %q\n\n", s.shareToken)
	}
	if s.sessionIdleTimeout != "" {
		fmt.Fprintf(&b, "session_idle_timeout: %s\n\n", s.sessionIdleTimeout)
	}
	if s.agoraEnabled {
		b.WriteString("agora:\n  enabled: true\n")
		fmt.Fprintf(&b, "  instance_name: %q\n", s.name)
		fmt.Fprintf(&b, "  description: %q\n", "mcp-gateway smoke suite")
		if s.agoraServe {
			b.WriteString("  serve:\n    enabled: true\n")
			if s.agoraTunnel != "" {
				fmt.Fprintf(&b, "    tunnel: %q\n", s.agoraTunnel)
			}
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "aggregator:\n  name: %q\n  version: \"1.0.0\"\n  separator: %q\n", s.name, separator)
	b.WriteString("  connection:\n    connect_timeout: 30s\n    call_timeout: 60s\n\n")

	b.WriteString("backends:\n")
	for _, backend := range s.backends {
		b.WriteString(backend)
	}

	path := filepath.Join(t.TempDir(), "gateway.yml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write gateway config: %v", err)
	}
	t.Logf("gateway config %s:\n%s", path, b.String())
	return path
}

// stdioBackend spawns a bundled mcp-filesystem rooted at dir. an empty allow
// list exposes every tool.
func stdioBackend(id, dir string, allow ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  - id: %q\n", id)
	b.WriteString("    transport:\n      type: \"stdio\"\n")
	fmt.Fprintf(&b, "      command: %q\n", binPath("mcp-filesystem"))
	fmt.Fprintf(&b, "      args: [%q]\n", dir)
	b.WriteString(toolFilter(allow))
	return b.String()
}

// zrokBackend reaches a remote MCP service over a zrok share, which is how a
// gateway chains to an mcp-bridge running elsewhere.
func zrokBackend(id, shareToken string, allow ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  - id: %q\n", id)
	b.WriteString("    transport:\n      type: \"zrok\"\n")
	fmt.Fprintf(&b, "      share_token: %q\n", shareToken)
	b.WriteString(toolFilter(allow))
	return b.String()
}

// agoraBackend reaches a remote MCP service over an Agora Layer 1 tunnel.
func agoraBackend(id, tunnel string, allow ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  - id: %q\n", id)
	b.WriteString("    transport:\n      type: \"agora\"\n")
	fmt.Fprintf(&b, "      agora_tunnel: %q\n", tunnel)
	b.WriteString(toolFilter(allow))
	return b.String()
}

func toolFilter(allow []string) string {
	if len(allow) == 0 {
		allow = []string{"*"}
	}
	quoted := make([]string, 0, len(allow))
	for _, name := range allow {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	return fmt.Sprintf("    tools:\n      mode: \"allow\"\n      list: [%s]\n", strings.Join(quoted, ", "))
}
