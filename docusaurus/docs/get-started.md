---
title: Get started with NetFoundry MCP Gateway
sidebar_label: Get started
---

# Get started

This guide walks you through NetFoundry MCP Gateway from scratch. You'll start with the simplest possible
setup — a single MCP server exposed over the network — and build up to a full multi-backend gateway
with tool filtering and namespacing.

## Prerequisites

Before you begin, you need:

- **Go 1.25.4+**: For building from source.
- **A zrok v2.0.x account**: Sign up for free at [zrok.io](https://zrok.io) or follow the
  `zrok2 invite` instructions below.

## Part 1: Enable zrok

NetFoundry MCP Gateway uses [zrok](https://zrok.io) for secure, zero-trust networking. All traffic between
components travels over an OpenZiti overlay network — nothing is ever exposed on a public IP.

If you already have a zrok v1.x account on zrok.io, the same account token works for enabling a
v2.x environment; the new environment ends up in `~/.zrok2` and appears in your account overview.

### Request an account

```bash
zrok2 invite
```

Enter your email address. You'll receive an invitation email with your account token.

### Install zrok

Download the `zrok2` binary (v2.0.0-rc7 or later) for your platform from the
[releases page](https://github.com/openziti/zrok/releases/tag/v2.0.0-rc7).

### Enable your environment

```bash
zrok2 enable <your-token>
zrok2 status
```

## Part 2: Your first MCP server (mcp-bridge + mcp-tools)

The simplest setup uses two components:

- **`mcp-bridge`**: Takes a local stdio MCP server and makes it available over the overlay.
- **`mcp-tools`**: Connects to a remote share and bridges it back to stdio.

Together they let any MCP client talk to an MCP server running anywhere, without opening ports or
configuring firewalls.

### Install

Install all components with a single command:

```bash
go install github.com/openziti/mcp-gateway/cmd/...@latest
```

This installs all components: `mcp-gateway`, `mcp-bridge`, `mcp-tools`, and `mcp-filesystem` (a
sandboxed filesystem server included for getting started).

### Build from source

Clone the repository and build each binary individually:

```bash
git clone https://github.com/openziti/mcp-gateway.git
cd mcp-gateway
go build ./cmd/mcp-gateway
go build ./cmd/mcp-bridge
go build ./cmd/mcp-tools
```

### Start the bridge

```bash
mcp-bridge mcp-filesystem ~/Documents
```

The bridge spawns `mcp-filesystem ~/Documents`, creates a zrok private share, and prints the share
token:

```json
{"share_token":"a1b2c3d4e5f6"}
```

The share token is the only thing needed to connect. There's no IP address, no port, no DNS name —
the server is a "dark service" that doesn't listen on any network interface. Keep this terminal
running.

### Connect with mcp-tools

In a second terminal:

```bash
mcp-tools run a1b2c3d4e5f6
```

`mcp-tools run` connects to the zrok share and bridges it to stdin/stdout. Any MCP client that speaks
stdio can use this as its transport.

### Configure Claude Desktop

Add the share to Claude Desktop's config file:

| Platform | Path |
|----------|------|
| macOS | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| Windows | `%APPDATA%\Claude\claude_desktop_config.json` |
| Linux | `~/.config/Claude/claude_desktop_config.json` |

Add the server entry:

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "mcp-tools",
      "args": ["run", "a1b2c3d4e5f6"]
    }
  }
}
```

Restart Claude Desktop. The `read_file`, `write_file`, and `list_directory` tools will be available.

## Part 3: Aggregate multiple servers (mcp-gateway)

`mcp-gateway` aggregates multiple backends and serves them all through a single zrok share.

### Create a configuration file

Create `gateway-config.yml`:

```yaml
aggregator:
  name: "my-gateway"
  version: "1.0.0"
  separator: ":"

backends:
  - id: docs
    transport:
      type: stdio
      command: mcp-filesystem
      args: ["~/Documents"]

  - id: data
    transport:
      type: stdio
      command: mcp-filesystem
      args: ["~/Data"]
    tools:
      mode: allow
      list:
        - "read_file"
        - "list_directory"
```

### Start the gateway

Pass the config file path as the argument:

```bash
mcp-gateway run gateway-config.yml
```

```text title="Output"
{"share_token":"x9y8z7w6v5u4"}
```

Connect the same way:

```bash
mcp-tools run x9y8z7w6v5u4
```

The available tools are now namespaced by backend ID:

| Tool | Source |
|------|--------|
| `docs:read_file` | docs backend |
| `docs:write_file` | docs backend |
| `docs:list_directory` | docs backend |
| `data:read_file` | data backend (filtered to read-only) |
| `data:list_directory` | data backend (filtered to read-only) |

`data:write_file` is absent because the allow list on the `data` backend only includes
`read_file` and `list_directory`. See [Configuration](configuration.md) for the full list of
aggregator settings, transport types, filtering options, and environment variable syntax.

## Part 4: Connect remote servers

You can connect to MCP servers running on other machines using `mcp-bridge` with the `zrok` transport.

### Run a bridge on a remote machine

```bash
mcp-bridge mcp-filesystem /data
```

```text title="Output"
{"share_token":"remote-token"}
```

### Add as a gateway backend

Reference the remote bridge's share token under a `zrok` transport:

```yaml
backends:
  - id: local
    transport:
      type: stdio
      command: mcp-filesystem
      args: ["~/Documents"]

  - id: remote
    transport:
      type: zrok
      share_token: "remote-token"
```

The gateway connects over the zrok overlay — no ports to open, no firewall rules. The remote
backend's tools are namespaced and filtered like any other backend.

Gateways can chain freely: a gateway backend can point to another gateway's share, or to a bridge
running anywhere on the network.

## Part 5: Connect to your agent

### Claude Desktop (stdio)

Add the gateway share to Claude Desktop's config:

```json
{
  "mcpServers": {
    "my-tools": {
      "command": "mcp-tools",
      "args": ["run", "x9y8z7w6v5u4"]
    }
  }
}
```

### HTTP mode

For agents or clients that expect an HTTP endpoint:

```bash
mcp-tools http x9y8z7w6v5u4 --bind 127.0.0.1:8080
```

Options:

- **`--bind`**: Address to listen on (default: `127.0.0.1:8080`)
- **`--stateless`**: No session persistence
- **`--json-response`**: Prefer JSON responses over SSE streams

Any MCP client that supports stdio transport can use `mcp-tools run <token>` directly. For HTTP-based
clients, use `mcp-tools http`.

**n8n example:** Configure the n8n MCP Client Tool:

- **URL**: `http://127.0.0.1:8080`
- **Transport**: SSE (default) or streamable HTTP

## Troubleshooting

- **"zrok enable" required**: Run `zrok2 enable` with your account token first.

- **Backend connection failures**: Check that stdio commands are correct and executables are in PATH.
  For zrok backends, verify the share token is valid and the remote bridge is running.

- **Tool not found**: Check the namespace prefix matches the backend ID. Verify the tool isn't filtered
  by your allow/deny list.

- **Debug logging**: Set `PFXLOG_LEVEL=debug` for verbose output:

  ```bash
  PFXLOG_LEVEL=debug mcp-gateway run config.yml
  ```

## What's next

- [Configuration](configuration.md): Full reference for every aggregator, backend, and filtering option.
- [Persistent shares](persistent-shares.md): Create share tokens that survive restarts for production deployments.
