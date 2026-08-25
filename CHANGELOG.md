# CHANGELOG

## Unreleased

CHANGE: **The Streamable HTTP session lifecycle moved out of `gateway` into a new `streamable` package.** `gateway.StreamableSessions` is now `streamable.Sessions` and `gateway.DefaultSessionIdleTimeout` is now `streamable.DefaultSessionIdleTimeout`; Go consumers importing either symbol must update the import path. The move exists so `mcp-tools` can share the same per-session lifecycle, which it could not do while the type lived in `gateway` — `gateway` imports `tools`, so the dependency could only run one way.

FIX: **`mcp-tools http` now gives every local client its own session on the remote gateway or bridge.** It previously opened one fabric MCP session at startup and served every local Streamable HTTP session from it, so multiple agents behind one `mcp-tools http` shared a single remote session and its backend state — defeating the isolation the gateway and bridge provide. The zrok access or Agora attachment is still acquired once and held for the process lifetime; only the MCP session is now per-client, and it is released when the client disconnects, its session expires, its initialization fails, or `mcp-tools` shuts down. `--stateless` mode owns a fabric session per request. `mcp-tools http` also gains `--session-idle-timeout`, matching `mcp-bridge`.

FIX: Gateway client sessions now close their backends before cancelling the session context. A `zrok`, `agora`, or `http` backend terminates its remote MCP session with a Streamable HTTP `DELETE` built on that context, so cancelling first left the downstream gateway or bridge holding the client's dedicated backends until its own idle timer expired. Stdio backends were unaffected.

FIX: Stdio backends now reject `transport.protocol` instead of accepting and ignoring it; protocol selection applies only to HTTP, HTTPS, zrok, and Agora backends.

## v0.1.10

CHANGE: **Fabric-served gateway and bridge endpoints now use Streamable HTTP instead of the deprecated SSE transport.** `mcp-tools run` and `mcp-tools http` move with that wire surface over both zrok and Agora; there is no dual-serving transition.

CHANGE: **Backend transports with no explicit `protocol` now default to Streamable HTTP.** Hand-written HTTP/HTTPS configurations for legacy SSE endpoints must set `protocol: sse`; zrok and Agora backends now honor and validate the same operator setting.

FIX: Abandoned Streamable HTTP sessions now release their dedicated gateway backend connections or bridge subprocess after 30 minutes of inactivity. Gateways can set `session_idle_timeout`; bridges can set `--session-idle-timeout`. An explicit zero disables idle expiry.

## v0.1.9

FIX: Agora serve initialization now deletes a newly created tunnel with a fresh bounded cleanup context when the subsequent listen fails, preventing request cancellation from leaving an orphaned tunnel behind.

FIX: `mcp-gateway` now publishes an Agora catalog advertisement only when Agora serving is enabled. Explicit `advertisement.publish: true` without serving is rejected; default-on publishing is skipped with a notice when the gateway uses Agora only for backend connections.

## v0.1.8

FIX: Backend tool discovery now follows MCP pagination for stdio, zrok, Agora, and HTTP transports, so every advertised tool reaches the gateway namespace instead of only the first page.

FIX: Ephemeral zrok shares are closed by default instead of admitting any account holding the token. Gateway configuration and `mcp-bridge --access-grant` can grant named zrok accounts access; an empty grant list remains owner-only.

FIX: HTTP backend clients no longer inherit environment proxies or follow redirects by default. Both behaviors require explicit transport opt-ins, keeping discovery and execution on the declared destination unless an operator deliberately widens it.

## v0.1.7

FEATURE: stdio backend transports accept an `env_policy`: `additive` (the default, and the historical behavior) appends configured entries to the gateway's own environment; `closed` starts the backend with exactly the configured entries and nothing inherited, so an embedding caller's spawned process tree cannot recover host secrets (for example via `/proc/<pid>/environ`). Unknown values are rejected at config validation. One shared builder now owns environment construction for both the aggregator and per-session stdio spawn paths.

## v0.1.6

FEATURE: Local stdio backends can enforce argument-aware filesystem reachability before dispatch. Per-tool path rules bind a named JSON argument to absolute roots, resolve symlinks, refuse traversal and malformed or unresolvable paths, and return a tool-level policy denial without invoking the backend.

FEATURE: The embedding API can serve streamable HTTP on a caller-provided listener, enabling explicit loopback-only development and integration paths without an enrolled overlay environment. Standalone configuration remains fabric-only and retains its SSE surface.

CHANGE: Gateway call logs now retain complete tool arguments as structured fields instead of truncating a JSON string at 500 bytes.

FIX: `mcp-gateway run` now shuts down gracefully on `SIGTERM` in addition to `SIGINT`, matching `mcp-bridge` and `mcp-tools`. Process managers such as systemd and Kubernetes send `SIGTERM`, which previously hard-killed the gateway and leaked its Agora serve tunnel and zrok share; cleanup now runs on either signal.

CHANGE: Build-metadata versioning now uses the `github.com/michaelquigley/push` framework. Each binary (`mcp-gateway`, `mcp-bridge`, `mcp-tools`, `mcp-filesystem`) gains a `version` subcommand that prints the stamped version, commit, build date, branch, and builder. This replaces the previous `--version` flag.

## v0.1.5

FEATURE: Initial support for Agora networks alongside zrok.

## v0.1.3

FEATURE: Support for HTTP/S MCP servers (SSE/streamable) from `mcp-gateway`. See the example configuration in `etc/mcp-gateway.yml` for details. (https://github.com/openziti/mcp-gateway/issues/14)

## v0.1.2

FIX: Fix cleanup leaks on runtime failures so `mcp-tools`, `mcp-bridge`, and `mcp-gateway` properly release zrok accesses and shares before exiting.

## v0.1.1

FEATURE: Initial public release.
