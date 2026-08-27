# Getting Started with MCP Gateway

This guide walks you through setting up MCP Gateway from scratch. You'll start with the simplest possible setup—a single MCP server exposed over the network—and build up to a full multi-backend gateway with tool filtering and namespacing.

## Prerequisites

Before you begin, ensure you have:

- **Go 1.25.4+** — for building MCP Gateway from source
- **Access to a zrok v2.0.x or later network** — sign up for a free account at [zrok.io](https://zrok.io) or follow the `zrok2 invite` instructions below.

## Part 1: Enable zrok

MCP Gateway uses [zrok](https://zrok.io) for secure, zero-trust networking. All traffic between components travels over an OpenZiti overlay network—nothing is ever exposed on a public IP.

If you already have a zrok v1.x account on zrok.io, the same account token will work for enabling an environment for v2.x; the new, separate environment will end up in a new `~/.zrok2` directory and will show up in your account overview.

### Request an account

```bash
zrok2 invite
```

Enter your email address when prompted. You'll receive an invitation email with your account token.

### Install zrok

Download the zrok `v2.0.0-rc7`+ binary for your platform from the [releases page](https://github.com/openziti/zrok/releases/tag/v2.0.0-rc7).

The binary is named `zrok2` to distinguish it from the v1.x series.

### Enable your environment

Once you receive your token via email:

```bash
zrok2 enable <your-token>
```

Verify it's working:

```bash
zrok2 status
```

You should see your account information and environment details.

## Part 2: Your First MCP Server (mcp-bridge + mcp-tools)

The simplest way to use MCP Gateway is with two components:

- **mcp-bridge** — takes a local stdio MCP server and makes it available over the network
- **mcp-tools** — connects to a remote share and bridges it back to stdio

Together they let any MCP client talk to an MCP server running anywhere on the network, without opening ports, configuring firewalls, or exposing public endpoints. The connection is a zrok "private share"—a dark service on the OpenZiti overlay that only authorized parties can reach.

### Install

```bash
go install github.com/openziti/mcp-gateway/cmd/...@latest
```

This installs all components: `mcp-gateway`, `mcp-bridge`, `mcp-tools`, and `mcp-filesystem` (a sandboxed filesystem server included for getting started).

### Start the bridge

We'll use `mcp-filesystem`, a sandboxed filesystem MCP server bundled with this project. It exposes three tools—`read_file`, `write_file`, and `list_directory`—restricted to the directories you specify. No additional dependencies required.

```bash
mcp-bridge mcp-filesystem ~/Documents
```

The bridge does three things:
1. Spawns `mcp-filesystem ~/Documents` as a child process
2. Creates a zrok private share on the overlay network
3. Prints the share token to stdout as JSON

```json
{"share_token":"a1b2c3d4e5f6"}
```

The share token is the only thing needed to connect to this server. There's no IP address, no port, no DNS name—the server is a "dark service" that doesn't listen on any network interface. Only someone with the share token and a zrok-enabled environment can reach it.

Keep this terminal running.

### Connect with mcp-tools

In a second terminal, use the share token to connect:

```bash
mcp-tools run a1b2c3d4e5f6
```

`mcp-tools run` connects to the zrok share and bridges it to stdin/stdout. Any MCP client that speaks stdio can use this as its transport—it looks like a local MCP server, but the actual work happens on the bridge side.

Press Ctrl+C to disconnect.

### Configure Claude Desktop

To wire this into an AI agent, add it to Claude Desktop's config file:

| Platform | Path |
|----------|------|
| macOS | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| Windows | `%APPDATA%\Claude\claude_desktop_config.json` |
| Linux | `~/.config/Claude/claude_desktop_config.json` |

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

Restart Claude Desktop. The read_file, write_file, and list_directory tools will be available in conversation.

This pattern—bridge on one side, tools on the other—works for any stdio MCP server. The server doesn't need modification; `mcp-bridge` handles the network layer transparently.

## Part 3: Aggregate Multiple Servers (mcp-gateway)

A single bridge works well for one server. For real-world use, you'll usually want to combine multiple MCP servers into one endpoint. That's what `mcp-gateway` does: it aggregates multiple backends and serves them all through a single zrok share.

### Create a configuration file

Create `gateway-config.yml`. We'll use two instances of `mcp-filesystem` pointed at different directories to demonstrate aggregation, namespacing, and filtering without needing any external dependencies:

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

```bash
mcp-gateway run gateway-config.yml
```

The gateway spawns each backend, connects to them, and creates a single zrok share:

```json
{"share_token":"x9y8z7w6v5u4"}
```

Connect the same way as before:

```bash
mcp-tools run x9y8z7w6v5u4
```

You now have access to both backends through one connection. The available tools are:

| Tool | Source |
|------|--------|
| `docs:read_file` | docs backend |
| `docs:write_file` | docs backend |
| `docs:list_directory` | docs backend |
| `data:read_file` | data backend (filtered to read-only) |
| `data:list_directory` | data backend (filtered to read-only) |

Notice that `data:write_file` is not exposed—the allow list on the `data` backend only includes `read_file` and `list_directory`.

### Aggregator settings

The `aggregator` section controls how the gateway presents itself and how it combines tools from multiple backends:

```yaml
aggregator:
  # server name and version reported to MCP clients
  name: "my-gateway"
  version: "1.0.0"

  # character used to namespace tools (default: "_")
  separator: ":"

  # timeouts for backend connections
  connection:
    connect_timeout: 30s  # time to wait when connecting to a backend
    call_timeout: 60s     # time to wait for a tool call to complete
```

### Tool namespacing

When you aggregate multiple backends, tool names could collide—two backends might both expose a tool called `read_file`. The gateway prevents this by prefixing every tool name with its backend ID and a separator.

With the config above (separator `":"`), tools are exposed as:

| Backend | Original tool | Namespaced tool |
|---------|--------------|-----------------|
| docs | `read_file` | `docs:read_file` |
| docs | `write_file` | `docs:write_file` |
| data | `read_file` | `data:read_file` |
| data | `list_directory` | `data:list_directory` |

The separator is configurable. Common choices:

| Separator | Example | Notes |
|-----------|---------|-------|
| `_` (default) | `docs_read_file` | Blends in with snake_case tool names |
| `:` | `docs:read_file` | Visually distinct from tool names |
| `-` | `docs-read_file` | Works but can be ambiguous with hyphenated tools |

Choose a backend ID that reads naturally as a namespace. Short, descriptive IDs work best: `fs`, `gh`, `db`, `fetch`.

### Tool filtering

By default, every tool from every backend is exposed. Tool filtering lets you control which tools are available per backend, using allow or deny lists with glob patterns.

**Allow mode** — only expose tools that match:

```yaml
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

This is useful when a server has many tools but you only want to expose a curated subset. Tools not in the list are never visible to clients.

**Deny mode** — expose everything except tools that match:

```yaml
- id: docs
  transport:
    type: stdio
    command: mcp-filesystem
    args: ["~/Documents"]
  tools:
    mode: deny
    list:
      - "write_file"
```

This is useful when a server's tools are generally safe, but a few should be blocked.

**Glob patterns** — the `*` wildcard matches any sequence of characters and `?` matches a single character:

| Pattern | Matches |
|---------|---------|
| `read_file` | Exactly `read_file` |
| `read_*` | `read_file`, `read_dir`, ... |
| `*file` | `read_file`, `write_file` |
| `*` | Everything |

**No filtering** — omit the `tools` section entirely to expose all tools:

```yaml
- id: docs
  transport:
    type: stdio
    command: mcp-filesystem
    args: ["~/Documents"]
  # no tools section = all tools exposed
```

Filtering happens at startup. Filtered tools are never exposed to clients—they don't appear in tool listings and can't be called.

### A real-world configuration

Once you're comfortable with the basics, here's a more realistic gateway config using third-party MCP servers (these require Node.js and npm):

```yaml
aggregator:
  name: "my-dev-tools"
  version: "1.0.0"
  separator: ":"

backends:
  - id: filesystem
    transport:
      type: stdio
      command: npx
      args: ["-y", "@modelcontextprotocol/server-filesystem", "~/Documents"]
    tools:
      mode: allow
      list:
        - "read_file"
        - "list_directory"
        - "search_files"

  - id: github
    transport:
      type: stdio
      command: npx
      args: ["-y", "@modelcontextprotocol/server-github"]
      env:
        GITHUB_TOKEN: "${GITHUB_TOKEN}"
    tools:
      mode: deny
      list:
        - "delete_*"
        - "force_*"

  - id: fetch
    transport:
      type: stdio
      command: npx
      args: ["-y", "@modelcontextprotocol/server-fetch"]
```

### Environment variables

Backend environment variables support `${VAR}` syntax to reference the gateway process's environment:

```yaml
env:
  GITHUB_TOKEN: "${GITHUB_TOKEN}"
  DATABASE_URL: "${DATABASE_URL}"
```

This keeps secrets out of config files. Export the variables before starting the gateway:

```bash
export GITHUB_TOKEN="ghp_..."
export DATABASE_URL="postgres://..."
mcp-gateway run gateway-config.yml
```

## Part 4: Connect Remote Servers

So far all backends have been local stdio processes spawned by the gateway. You can also connect to remote MCP servers running on other machines using `mcp-bridge` and the `zrok` transport type.

### Run a bridge on a remote machine

On the remote machine (with zrok enabled):

```bash
mcp-bridge mcp-filesystem /data
# outputs: {"share_token":"remote-token"}
```

### Add as a gateway backend

In your gateway config, add a backend with `type: zrok` and the bridge's share token:

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

The gateway connects to the remote bridge over the zrok overlay—no ports to open, no firewall rules to configure. The remote backend's tools are namespaced and filtered just like local ones.

### Chaining

You can chain bridges into gateways freely. A gateway backend can point to another gateway's share, or to a bridge running anywhere on the network. This makes it straightforward to distribute MCP servers across machines, data centers, or cloud regions while presenting them as a single unified set of tools to clients.

## Part 5: Connect to Your Agent

### Claude Desktop (stdio)

Claude Desktop speaks stdio natively. Add `mcp-tools run` to your config:

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

Restart Claude Desktop. Your tools will be available in the conversation.

### HTTP mode

For agents or clients that expect an HTTP endpoint (like n8n), use `mcp-tools http`:

```bash
mcp-tools http x9y8z7w6v5u4 --bind 127.0.0.1:8080
```

This starts a local HTTP server that proxies to the zrok share. Clients connect to `http://127.0.0.1:8080`.

Options:
- `--bind` — address to listen on (default: `127.0.0.1:8080`)
- `--stateless` — stateless mode, no session persistence
- `--json-response` — prefer JSON responses over SSE streams

**n8n example:**

Configure the n8n MCP Client Tool:
- **URL**: `http://127.0.0.1:8080`
- **Transport**: SSE (default) or streamable HTTP

### Other agents

Any MCP client that supports stdio transport can use `mcp-tools run <token>` directly. For HTTP-based clients, use `mcp-tools http`.

## Part 6: Persistent Shares

By default, share tokens are ephemeral—they disappear when the process exits. For production use, create persistent shares that survive restarts.

### Create a persistent share

Share names are globally unique across the zrok instance. Choose a name unlikely to conflict:

```bash
zrok2 create share my-gateway
```

This outputs the share token. If the name is taken, you'll get an error—choose a different name.

Token names must be 3-32 characters, lowercase alphanumeric and hyphens (`[a-z0-9-]`).

If you omit the name, zrok generates a random token:

```bash
zrok2 create share
```

### Use with mcp-gateway

Add the `share_token` field at the top level of your gateway config:

```yaml
share_token: "my-gateway"

aggregator:
  name: "my-dev-tools"
  version: "1.0.0"

backends:
  # ...
```

Now you can stop and restart the gateway, and clients reconnect using the same token.

### Use with mcp-bridge

Pass `--share-token` on the command line:

```bash
mcp-bridge --share-token my-bridge mcp-filesystem ~/Documents
```

### Delete when done

```bash
zrok2 delete share my-gateway
```

## Common MCP Servers

The ecosystem has many MCP servers available. Here are some well-maintained ones (these require Node.js and npm):

| Package | Purpose |
|---------|---------|
| `@modelcontextprotocol/server-filesystem` | File operations |
| `@modelcontextprotocol/server-github` | GitHub integration |
| `@modelcontextprotocol/server-fetch` | Web content fetching |
| `@modelcontextprotocol/server-memory` | Knowledge graph memory |
| `@modelcontextprotocol/server-postgres` | PostgreSQL queries |
| `@modelcontextprotocol/server-sqlite` | SQLite database |

Install any of them with:

```bash
npx -y @modelcontextprotocol/server-<name>
```

Any stdio MCP server works with `mcp-bridge` and `mcp-gateway` regardless of language or runtime.

## Next Steps

- **Full configuration reference**: See [../etc/mcp-gateway.yml](../etc/mcp-gateway.yml) for all configuration options with detailed comments
- **Architecture overview**: The [README](../README.md) has diagrams showing how components interact
- **Troubleshooting**: Check the troubleshooting section below or see CLAUDE.md for debugging tips

## Troubleshooting

**"zrok enable" required**: The zrok SDK requires an enabled environment. Run `zrok2 enable` with your account token.

**Backend connection failures**:
- Check that stdio commands are correct and executables are in PATH
- For zrok backends, verify the share token is valid and the remote bridge is running

**Tool not found**:
- Check the namespace prefix matches the backend ID
- Verify the tool isn't filtered by your allow/deny list
- Check that the backend successfully connected (look for connection logs)

**Debug logging**: Set the `PFXLOG_LEVEL` environment variable for verbose output:

```bash
PFXLOG_LEVEL=debug mcp-gateway run config.yml
```
