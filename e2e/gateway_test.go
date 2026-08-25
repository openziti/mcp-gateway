//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"
)

// mcp-gateway aggregates several backends behind one fabric endpoint. beyond
// the round trip, these scenarios assert the two things aggregation adds:
// namespacing and per-backend tool filtering.

// twoBackends builds the standard gateway shape used across these scenarios —
// an unrestricted "docs" backend and a read-only "data" backend — and returns
// the config spec plus the docs sandbox file and its contents.
func twoBackends(t *testing.T, label, name string) (gatewaySpec, string, string) {
	t.Helper()
	docsDir, docsFile, docsContents := sandbox(t, label+"-docs")
	dataDir, _, _ := sandbox(t, label+"-data")

	spec := gatewaySpec{
		name: name,
		backends: []string{
			stdioBackend("docs", docsDir),
			stdioBackend("data", dataDir, "read_file", "list_directory"),
		},
	}
	return spec, docsFile, docsContents
}

// assertAggregation checks namespacing and the allow filter on the standard
// two-backend shape.
func assertAggregation(t *testing.T, names []string) {
	t.Helper()
	requireTools(t, names,
		"docs:read_file", "docs:write_file", "docs:list_directory",
		"data:read_file", "data:list_directory")
	// the data backend allows only reads, so its write tool must never be
	// advertised — filtering happens at startup, not at call time.
	requireNoTool(t, names, "data:write_file")
}

func TestGatewayOverZrok(t *testing.T) {
	spec, docsFile, docsContents := twoBackends(t, "gateway-zrok", "mcpe2e-gateway-zrok")
	spec.zrokShare = true

	gateway := start(t, "mcp-gateway", "run", spec.write(t))
	token := gateway.shareToken(t)

	session := dialViaTools(t, token)
	assertAggregation(t, toolNames(t, session.ClientSession))

	if got := readFile(t, session.ClientSession, "docs:read_file", docsFile); got != docsContents {
		t.Fatalf("docs:read_file returned %q, want %q", got, docsContents)
	}

	session.Close()
	gateway.stop(t)
	awaitZrokShareAbsent(t, token)
}

func TestGatewayOverAgora(t *testing.T) {
	spec, docsFile, docsContents := twoBackends(t, "gateway-agora", "mcpe2e-gateway-agora")
	tunnel := resourceName("gtun")
	spec.agoraEnabled = true
	spec.agoraServe = true
	spec.agoraTunnel = tunnel

	gateway := start(t, "mcp-gateway", "run", spec.write(t))
	gateway.awaitRunning(t, 3*time.Second)

	session := dialViaTools(t, "--agora", tunnel)
	assertAggregation(t, toolNames(t, session.ClientSession))

	if got := readFile(t, session.ClientSession, "docs:read_file", docsFile); got != docsContents {
		t.Fatalf("docs:read_file returned %q, want %q", got, docsContents)
	}

	session.Close()
	gateway.stop(t)
	awaitAgoraTunnelAbsent(t, tunnel)
}

// TestGatewayHTTPModeViaTools covers the client surface for agents that speak
// Streamable HTTP rather than stdio.
func TestGatewayHTTPModeViaTools(t *testing.T) {
	spec, docsFile, docsContents := twoBackends(t, "gateway-http", "mcpe2e-gateway-http")
	spec.zrokShare = true

	gateway := start(t, "mcp-gateway", "run", spec.write(t))
	token := gateway.shareToken(t)

	bind := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	proxy := start(t, "mcp-tools", "http", token, "--bind", bind)
	proxy.awaitRunning(t, 2*time.Second)

	session := dialHTTP(t, "http://"+bind)
	assertAggregation(t, toolNames(t, session))

	if got := readFile(t, session, "docs:read_file", docsFile); got != docsContents {
		t.Fatalf("docs:read_file returned %q, want %q", got, docsContents)
	}

	session.Close()
	proxy.stop(t)
	gateway.stop(t)
	awaitZrokShareAbsent(t, token)
}

// TestGatewayChainsZrokBackend points a gateway at an mcp-bridge across zrok,
// which exercises transport.type: zrok on the backend side — a path that also
// moved to Streamable HTTP.
func TestGatewayChainsZrokBackend(t *testing.T) {
	remoteDir, remoteFile, remoteContents := sandbox(t, "chain-zrok-remote")
	localDir, _, _ := sandbox(t, "chain-zrok-local")

	bridge := start(t, "mcp-bridge", binPath("mcp-filesystem"), remoteDir)
	bridgeToken := bridge.shareToken(t)

	spec := gatewaySpec{
		name:      "mcpe2e-chain-zrok",
		zrokShare: true,
		backends: []string{
			stdioBackend("local", localDir),
			zrokBackend("remote", bridgeToken),
		},
	}
	gateway := start(t, "mcp-gateway", "run", spec.write(t))
	gatewayToken := gateway.shareToken(t)

	session := dialViaTools(t, gatewayToken)
	requireTools(t, toolNames(t, session.ClientSession), "local:read_file", "remote:read_file")

	// the payload has to come back from the far side of two fabric hops.
	if got := readFile(t, session.ClientSession, "remote:read_file", remoteFile); got != remoteContents {
		t.Fatalf("remote:read_file returned %q, want %q", got, remoteContents)
	}

	session.Close()
	gateway.stop(t)
	bridge.stop(t)
	awaitZrokShareAbsent(t, gatewayToken)
	awaitZrokShareAbsent(t, bridgeToken)
}

// TestGatewayChainsAgoraBackend points a gateway at an Agora-served bridge.
// the gateway serves over zrok so a failure isolates to the Agora backend
// path: the startup dialer attach and the per-session dial.
func TestGatewayChainsAgoraBackend(t *testing.T) {
	remoteDir, remoteFile, remoteContents := sandbox(t, "chain-agora-remote")
	localDir, _, _ := sandbox(t, "chain-agora-local")
	tunnel := resourceName("ctun")

	bridge := start(t, "mcp-bridge",
		"--network=agora",
		"--agora-tunnel", tunnel,
		binPath("mcp-filesystem"), remoteDir)
	bridge.awaitRunning(t, 3*time.Second)

	spec := gatewaySpec{
		name:         "mcpe2e-chain-agora",
		zrokShare:    true,
		agoraEnabled: true,
		backends: []string{
			stdioBackend("local", localDir),
			agoraBackend("remote", tunnel),
		},
	}
	gateway := start(t, "mcp-gateway", "run", spec.write(t))
	gatewayToken := gateway.shareToken(t)

	session := dialViaTools(t, gatewayToken)
	requireTools(t, toolNames(t, session.ClientSession), "local:read_file", "remote:read_file")

	if got := readFile(t, session.ClientSession, "remote:read_file", remoteFile); got != remoteContents {
		t.Fatalf("remote:read_file returned %q, want %q", got, remoteContents)
	}

	session.Close()
	gateway.stop(t)
	bridge.stop(t)
	awaitZrokShareAbsent(t, gatewayToken)
	awaitAgoraTunnelAbsent(t, tunnel)
}

// TestGatewayBindsPersistentAgoraTunnel covers the Agora bind path: a tunnel
// provisioned outside the process is served and then left intact.
func TestGatewayBindsPersistentAgoraTunnel(t *testing.T) {
	if persistentTunnel == "" {
		t.Skip("no persistent agora tunnel was provisioned for this run")
	}
	spec, docsFile, docsContents := twoBackends(t, "gateway-persistent", "mcpe2e-gw-persist")
	spec.agoraEnabled = true
	spec.agoraServe = true
	spec.agoraTunnel = persistentTunnel

	gateway := start(t, "mcp-gateway", "run", spec.write(t))
	gateway.awaitRunning(t, 3*time.Second)

	session := dialViaTools(t, "--agora", persistentTunnel)
	if got := readFile(t, session.ClientSession, "docs:read_file", docsFile); got != docsContents {
		t.Fatalf("docs:read_file returned %q, want %q", got, docsContents)
	}
	session.Close()
	gateway.stop(t)

	if !agoraTunnelExists(t, persistentTunnel) {
		t.Errorf("persistent tunnel %s was deleted on shutdown; the bind path must leave it intact", persistentTunnel)
	}
}

// TestGatewayDualListeners covers a gateway serving zrok and Agora at the same
// time. the two listeners are independent and share one MCP handler, so the
// thing worth proving is that both answer for the same process.
func TestGatewayDualListeners(t *testing.T) {
	spec, docsFile, docsContents := twoBackends(t, "gateway-dual", "mcpe2e-gateway-dual")
	tunnel := resourceName("dtun")
	spec.zrokShare = true
	spec.agoraEnabled = true
	spec.agoraServe = true
	spec.agoraTunnel = tunnel

	gateway := start(t, "mcp-gateway", "run", spec.write(t))
	token := gateway.shareToken(t)

	viaZrok := dialViaTools(t, token)
	if got := readFile(t, viaZrok.ClientSession, "docs:read_file", docsFile); got != docsContents {
		t.Fatalf("over zrok docs:read_file returned %q, want %q", got, docsContents)
	}

	viaAgora := dialViaTools(t, "--agora", tunnel)
	if got := readFile(t, viaAgora.ClientSession, "docs:read_file", docsFile); got != docsContents {
		t.Fatalf("over agora docs:read_file returned %q, want %q", got, docsContents)
	}

	viaZrok.Close()
	viaAgora.Close()
	gateway.stop(t)
	awaitZrokShareAbsent(t, token)
	awaitAgoraTunnelAbsent(t, tunnel)
}
