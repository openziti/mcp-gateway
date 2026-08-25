//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// mcp-bridge exposes one stdio MCP server on the fabric. these scenarios walk
// the whole path an agent walks: mcp-tools speaking stdio locally, Streamable
// HTTP across the fabric, and a real subprocess on the far side.

func TestBridgeOverZrok(t *testing.T) {
	dir, file, contents := sandbox(t, "bridge-zrok")

	bridge := start(t, "mcp-bridge", binPath("mcp-filesystem"), dir)
	token := bridge.shareToken(t)

	session := dialViaTools(t, token)
	requireTools(t, toolNames(t, session.ClientSession), "read_file", "write_file", "list_directory")

	if got := readFile(t, session.ClientSession, "read_file", file); got != contents {
		t.Fatalf("read_file returned %q, want %q", got, contents)
	}

	session.Close()
	bridge.stop(t)
	awaitZrokShareAbsent(t, token)
}

func TestBridgeOverAgora(t *testing.T) {
	dir, file, contents := sandbox(t, "bridge-agora")
	tunnel := resourceName("btun")

	bridge := start(t, "mcp-bridge",
		"--network=agora",
		"--agora-tunnel", tunnel,
		binPath("mcp-filesystem"), dir)
	bridge.awaitRunning(t, 3*time.Second)

	session := dialViaTools(t, "--agora", tunnel)
	requireTools(t, toolNames(t, session.ClientSession), "read_file", "list_directory")

	if got := readFile(t, session.ClientSession, "read_file", file); got != contents {
		t.Fatalf("read_file returned %q, want %q", got, contents)
	}

	session.Close()
	bridge.stop(t)
	awaitAgoraTunnelAbsent(t, tunnel)
}

// TestBridgeBindsPersistentZrokShare covers the bind path: a share provisioned
// outside the process, bound rather than created, and deliberately left intact
// so clients reconnect under the same token across a restart.
func TestBridgeBindsPersistentZrokShare(t *testing.T) {
	if persistentShare == "" {
		t.Skip("no persistent zrok share was provisioned for this run")
	}
	dir, file, contents := sandbox(t, "bridge-persistent")

	first := start(t, "mcp-bridge", "--share-token", persistentShare, binPath("mcp-filesystem"), dir)
	if token := first.shareToken(t); token != persistentShare {
		t.Fatalf("bridge reported share token %q, want the pre-provisioned %q", token, persistentShare)
	}

	session := dialViaTools(t, persistentShare)
	if got := readFile(t, session.ClientSession, "read_file", file); got != contents {
		t.Fatalf("read_file returned %q, want %q", got, contents)
	}
	session.Close()
	first.stop(t)

	if !zrokShareExists(t, persistentShare) {
		t.Fatalf("persistent share %s was deleted on shutdown; the bind path must leave it intact", persistentShare)
	}

	// the same token has to work again after a restart. that is the whole
	// point of a persistent share.
	second := start(t, "mcp-bridge", "--share-token", persistentShare, binPath("mcp-filesystem"), dir)
	second.shareToken(t)

	reconnected := dialViaTools(t, persistentShare)
	if got := readFile(t, reconnected.ClientSession, "read_file", file); got != contents {
		t.Fatalf("after restart read_file returned %q, want %q", got, contents)
	}
	reconnected.Close()
	second.stop(t)

	if !zrokShareExists(t, persistentShare) {
		t.Errorf("persistent share %s did not survive the second shutdown", persistentShare)
	}
}
