//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// the SSE-to-Streamable-HTTP conversion moved session ownership: a Streamable
// HTTP session outlives the request that creates it, so releasing per-session
// resources became the server's job rather than a side effect of the transport
// closing. these scenarios watch the backend subprocesses directly, which is
// the observable form of that ownership.

func TestBridgeReleasesSubprocessOnClientDisconnect(t *testing.T) {
	dir, file, contents := sandbox(t, "lifecycle-disconnect")

	bridge := start(t, "mcp-bridge", binPath("mcp-filesystem"), dir)
	token := bridge.shareToken(t)

	// tool discovery at startup spawns a temporary backend and closes it, so a
	// settled bridge owns no subprocess until a client arrives.
	awaitBackendChildren(t, bridge.pid(), 0, 30*time.Second, "bridge before any client")

	session := dialViaTools(t, token)
	readFile(t, session.ClientSession, "read_file", file)
	awaitBackendChildren(t, bridge.pid(), 1, 30*time.Second, "bridge with one client")

	session.Close()
	awaitBackendChildren(t, bridge.pid(), 0, 60*time.Second, "bridge after client disconnect")

	// the bridge itself is still healthy and serves a second client.
	reconnected := dialViaTools(t, token)
	if got := readFile(t, reconnected.ClientSession, "read_file", file); got != contents {
		t.Fatalf("read_file after reconnect returned %q, want %q", got, contents)
	}
	reconnected.Close()
	bridge.stop(t)
	awaitZrokShareAbsent(t, token)
}

// TestBridgeIdleTimeoutReleasesAbandonedSubprocess covers the bound on a client
// that vanishes without terminating its session. without idle expiry the
// subprocess would be retained until the bridge itself shut down.
func TestBridgeIdleTimeoutReleasesAbandonedSubprocess(t *testing.T) {
	dir, file, _ := sandbox(t, "lifecycle-idle")

	const idleTimeout = 10 * time.Second
	bridge := start(t, "mcp-bridge",
		"--session-idle-timeout", idleTimeout.String(),
		binPath("mcp-filesystem"), dir)
	token := bridge.shareToken(t)

	session := dialViaTools(t, token)
	readFile(t, session.ClientSession, "read_file", file)
	awaitBackendChildren(t, bridge.pid(), 1, 30*time.Second, "bridge with one client")

	session.abandon(t)

	// nothing told the bridge the client is gone; only idle expiry can reclaim
	// the subprocess. allow generous slack over the configured bound.
	awaitBackendChildren(t, bridge.pid(), 0, idleTimeout+90*time.Second, "bridge after idle expiry")

	bridge.stop(t)
	awaitZrokShareAbsent(t, token)
}

// TestGatewayReleasesBackendsOnClientDisconnect is the gateway's counterpart.
// its per-session cleanup closes dedicated backend connections rather than a
// single subprocess, so it is worth watching separately.
func TestGatewayReleasesBackendsOnClientDisconnect(t *testing.T) {
	docsDir, docsFile, _ := sandbox(t, "lifecycle-gateway-docs")
	dataDir, _, _ := sandbox(t, "lifecycle-gateway-data")

	spec := gatewaySpec{
		name:      "mcpe2e-gw-lifecycle",
		zrokShare: true,
		backends: []string{
			stdioBackend("docs", docsDir),
			stdioBackend("data", dataDir),
		},
	}
	gateway := start(t, "mcp-gateway", "run", spec.write(t))
	token := gateway.shareToken(t)

	awaitBackendChildren(t, gateway.pid(), 0, 30*time.Second, "gateway before any client")

	session := dialViaTools(t, token)
	readFile(t, session.ClientSession, "docs:read_file", docsFile)

	// each client session gets dedicated connections to every backend, so two
	// backends mean two subprocesses.
	awaitBackendChildren(t, gateway.pid(), 2, 30*time.Second, "gateway with one client")

	session.Close()
	awaitBackendChildren(t, gateway.pid(), 0, 60*time.Second, "gateway after client disconnect")

	gateway.stop(t)
	awaitZrokShareAbsent(t, token)
}
