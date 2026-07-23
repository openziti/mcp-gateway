# CHANGELOG

## Unreleased

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
