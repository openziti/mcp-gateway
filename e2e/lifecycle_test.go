//go:build e2e

package e2e

import (
	"fmt"
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

// TestToolsHTTPIsolatesBackendSessions is the mcp-tools counterpart. one
// `mcp-tools http` process serving two local agents must give each its own
// session on the far side rather than fanning both onto one. the gateway
// spawns a subprocess per backend per session, so the subprocess count is the
// direct observable of that isolation.
func TestToolsHTTPIsolatesBackendSessions(t *testing.T) {
	docsDir, docsFile, docsContents := sandbox(t, "tools-http-isolation")

	spec := gatewaySpec{
		name:      "mcpe2e-tools-isolation",
		zrokShare: true,
		backends:  []string{stdioBackend("docs", docsDir)},
	}
	gateway := start(t, "mcp-gateway", "run", spec.write(t))
	token := gateway.shareToken(t)

	bind := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	proxy := start(t, "mcp-tools", "http", token, "--bind", bind)
	proxy.awaitRunning(t, 2*time.Second)

	// mcp-tools discovers tools over a session it closes, so a settled proxy
	// holds nothing open on the gateway.
	awaitBackendChildren(t, gateway.pid(), 0, 30*time.Second, "gateway before any http client")

	first := dialHTTP(t, "http://"+bind)
	second := dialHTTP(t, "http://"+bind)
	readFile(t, first, "docs:read_file", docsFile)
	readFile(t, second, "docs:read_file", docsFile)

	// one backend, two frontend sessions: two subprocesses. sharing a single
	// backend session — the defect this scenario guards — leaves it at one.
	awaitBackendChildren(t, gateway.pid(), 2, 30*time.Second, "gateway with two http clients")

	// one agent leaving must not disturb the other.
	first.Close()
	awaitBackendChildren(t, gateway.pid(), 1, 60*time.Second, "gateway after one http client disconnects")
	if got := readFile(t, second, "docs:read_file", docsFile); got != docsContents {
		t.Fatalf("surviving session read %q, want %q", got, docsContents)
	}

	second.Close()
	awaitBackendChildren(t, gateway.pid(), 0, 60*time.Second, "gateway after both http clients disconnect")

	proxy.stop(t)
	gateway.stop(t)
	awaitZrokShareAbsent(t, token)
}

// TestGatewayReleasesChainedBridgeOnClientDisconnect covers the release path
// that stdio backends cannot exercise. a fabric-backed backend terminates its
// remote MCP session with a Streamable HTTP DELETE, so the downstream bridge
// only reclaims its subprocess if the gateway closes that backend while its
// connection context is still live. the observable is the *bridge's* child
// count, not the gateway's.
func TestGatewayReleasesChainedBridgeOnClientDisconnect(t *testing.T) {
	remoteDir, remoteFile, remoteContents := sandbox(t, "chain-release-remote")

	bridge := start(t, "mcp-bridge", binPath("mcp-filesystem"), remoteDir)
	bridgeToken := bridge.shareToken(t)
	awaitBackendChildren(t, bridge.pid(), 0, 30*time.Second, "bridge before any client")

	spec := gatewaySpec{
		name:      "mcpe2e-chain-release",
		zrokShare: true,
		backends:  []string{zrokBackend("remote", bridgeToken)},
	}
	gateway := start(t, "mcp-gateway", "run", spec.write(t))
	token := gateway.shareToken(t)

	// gateway discovery opens and closes a session of its own, so the bridge
	// settles back to zero before any real client arrives.
	awaitBackendChildren(t, bridge.pid(), 0, 30*time.Second, "bridge after gateway discovery")

	session := dialViaTools(t, token)
	if got := readFile(t, session.ClientSession, "remote:read_file", remoteFile); got != remoteContents {
		t.Fatalf("remote:read_file returned %q, want %q", got, remoteContents)
	}
	awaitBackendChildren(t, bridge.pid(), 1, 30*time.Second, "bridge with one chained client")

	// the gateway must terminate its backend MCP session, not merely drop it.
	session.Close()
	awaitBackendChildren(t, bridge.pid(), 0, 60*time.Second, "bridge after chained client disconnect")

	gateway.stop(t)
	awaitZrokShareAbsent(t, token)
	bridge.stop(t)
	awaitZrokShareAbsent(t, bridgeToken)
}
