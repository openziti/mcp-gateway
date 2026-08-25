# End-to-End Smoke Suite

The smoke suite runs `mcp-tools`, `mcp-bridge`, and `mcp-gateway` as real processes against live zrok shares and Agora tunnels. It exists to answer one question after a major change: does the trifecta still work end to end? It is hand-run, not part of the ordinary repository gate.

```bash
make e2e
```

`make test` stays fabric-free and remains the gate you run constantly. The suite lives in `e2e/` behind the `e2e` build tag, so an ordinary `go build ./...` and `go test ./...` never see it.

## What it exercises

Everything on the data path goes through the project's own binaries. An MCP client in the test process speaks stdio to `mcp-tools run`, exactly as an agent does, or Streamable HTTP to `mcp-tools http`. On the far side a real `mcp-filesystem` serves a temp-directory sandbox holding a known file, so every scenario ends by reading that file back through the whole chain — a successful handshake alone is not enough to pass.

| Scenario | Path under test |
|---|---|
| `TestBridgeOverZrok` | `mcp-tools run` → zrok → `mcp-bridge` |
| `TestBridgeOverAgora` | `mcp-tools run --agora` → Agora tunnel → `mcp-bridge --network=agora` |
| `TestBridgeBindsPersistentZrokShare` | bind path: pre-provisioned share, bound and left intact across a restart |
| `TestGatewayOverZrok` | `mcp-tools run` → zrok → `mcp-gateway` with two stdio backends |
| `TestGatewayOverAgora` | `mcp-tools run --agora` → Agora tunnel → `mcp-gateway` |
| `TestGatewayDualListeners` | one gateway answering on a zrok share and an Agora tunnel at once |
| `TestGatewayHTTPModeViaTools` | Streamable HTTP client → `mcp-tools http` → zrok → `mcp-gateway` |
| `TestGatewayChainsZrokBackend` | gateway with `transport.type: zrok` reaching an `mcp-bridge` across two fabric hops |
| `TestGatewayChainsAgoraBackend` | gateway with `transport.type: agora` reaching `mcp-bridge --network=agora` |
| `TestGatewayBindsPersistentAgoraTunnel` | bind path: pre-provisioned tunnel, served and left intact |

The gateway scenarios use two backends, one unrestricted and one allow-filtered to reads, so namespacing (`docs:read_file`) and startup filtering (`data:write_file` never advertised) are asserted rather than assumed.

## Lifecycle scenarios

These are the ones that earn the suite its keep. A Streamable HTTP session outlives the request that creates it, so releasing per-session resources is the server's job rather than a side effect of a transport closing. The suite watches the backend subprocesses directly through `/proc`, which is the observable form of that ownership.

| Scenario | What it proves |
|---|---|
| `TestBridgeReleasesSubprocessOnClientDisconnect` | a client that disconnects cleanly releases its dedicated subprocess, and the bridge still serves the next client |
| `TestBridgeIdleTimeoutReleasesAbandonedSubprocess` | a client killed outright — never terminating its session — is reclaimed by `--session-idle-timeout` |
| `TestGatewayReleasesBackendsOnClientDisconnect` | a gateway client session releases its dedicated connection to *every* backend |
| `TestGatewayReleasesChainedBridgeOnClientDisconnect` | a gateway client leaving releases the *downstream bridge's* subprocess — the fabric-backed backend path, which stdio backends cannot exercise |
| `TestToolsHTTPIsolatesBackendSessions` | two agents behind one `mcp-tools http` get two gateway sessions, not one, and either can leave without disturbing the other |

Every scenario that creates an ephemeral share or tunnel also asserts, after shutdown, that the fabric resource is gone.

## Prerequisites

- An enabled zrok v2 environment (`zrok2 enable <token>`), with `zrok2` on `PATH`.
- An enrolled Agora environment, plus a reachable controller and Ziti fabric. `AGORA_ENV_ROOT` overrides the default `~/.agora`.
- The `agora` CLI, used to provision the persistent tunnel fixture and to assert tunnel release. It is often built into its own GOPATH rather than installed on `PATH`; set `MCP_E2E_AGORA_BIN` to point at it. Without the CLI the suite still runs, skipping the persistent-tunnel scenario and the tunnel-release assertions.

A missing prerequisite fails the run immediately with a message naming what is absent, rather than timing out somewhere in the middle.

## What it creates, and what it cleans up

The suite builds the working tree into a temp directory and runs those binaries. It never uses whatever happens to be installed in `GOPATH/bin` — the point is to smoke the tree in front of you.

Every fabric resource it creates is named `mcpe2e-<run id>-<role>`, and the teardown helpers refuse to touch any name without that prefix. Ephemeral shares and tunnels are released by the binaries themselves on shutdown, which the suite then verifies. The two persistent fixtures — one named zrok share, one Agora tunnel — are provisioned at the start of the run and released at the end, so the bind path is covered without leaving anything behind.

A run takes roughly four minutes, most of it zrok share creation. Scenarios run serially so a failure is legible and the fabrics see one thing at a time.

If a run is killed hard enough to skip its cleanup, `zrok2 list shares` and `agora tunnel list` will show the stragglers under the `mcpe2e-` prefix; delete them with `zrok2 delete share <name>` and `agora tunnel delete <name>`.

## What stays manual

The catalog card in the Agora dashboard, and anything else whose verification is a human looking at a screen. See the manual smoke table in [Agora Integration](agora.md).
