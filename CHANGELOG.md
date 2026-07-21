# CHANGELOG

## Unreleased

FIX: `mcp-gateway run` now shuts down gracefully on `SIGTERM` in addition to `SIGINT`, matching `mcp-bridge` and `mcp-tools`. Process managers such as systemd and Kubernetes send `SIGTERM`, which previously hard-killed the gateway and leaked its Agora serve tunnel and zrok share; cleanup now runs on either signal.

CHANGE: Build-metadata versioning now uses the `github.com/michaelquigley/push` framework. Each binary (`mcp-gateway`, `mcp-bridge`, `mcp-tools`, `mcp-filesystem`) gains a `version` subcommand that prints the stamped version, commit, build date, branch, and builder. This replaces the previous `--version` flag.

## v0.1.5

FEATURE: Initial support for Agora networks alongside zrok.

## v0.1.3

FEATURE: Support for HTTP/S MCP servers (SSE/streamable) from `mcp-gateway`. See the example configuration in `etc/mcp-gateway.yml` for details. (https://github.com/openziti/mcp-gateway/issues/14)

## v0.1.2

FIX: Fix cleanup leaks on runtime failures so `mcp-tools`, `mcp-bridge`, and `mcp-gateway` properly release zrok accesses and shares before exiting.

## v0.1.1

Initial public release.
