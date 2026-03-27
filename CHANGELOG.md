# CHANGELOG

## v0.1.3

FEATURE: Support for HTTPS MCP servers (SSE/streamable) from `mcp-gateway`. See the example configuration in `etc/mcp-gateway.yml` for details. (https://github.com/openziti/mcp-gateway/issues/14)

## v0.1.2

FIX: Fix cleanup leaks on runtime failures so `mcp-tools`, `mcp-bridge`, and `mcp-gateway` properly release zrok accesses and shares before exiting.

## v0.1.1

Initial public release.
