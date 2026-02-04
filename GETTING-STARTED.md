# Getting Started with MCP Gateway

This guide walks you through setting up MCP Gateway from scratch. You'll go from zero to a fully functional setup in about 10 minutes.

## Prerequisites

Before you begin, ensure you have:

- **Node.js and npm** - Required for running MCP servers (most are distributed as npm packages)
- **Go 1.25.4+** - For building MCP Gateway from source
- **zrok v2.0.x or later** - The overlay network that makes this all work

## Part 1: Enable zrok

MCP Gateway uses zrok for secure, zero-trust networking. You'll need a zrok account.

If you already have a zrok v1.x account on zrok.io, the same account token will work for enabling an environment for v2.x; the new, separate environment will end up in a new `~/.zrok2` directory and will show up in your account overview.

### Request an Account

```bash
zrok2 invite
```

Enter your email address when prompted. You'll receive an invitation email with your account token.

### Install zrok

Download the zrok `v2.0.0-rc5`+ binary for your platform from the [releases page](https://github.com/openziti/zrok/releases/tag/v2.0.0-rc5).

The binary is named `zrok2` to distinguish it from the v1.x series.

### Enable Your Environment

Once you receive your token via email:

```bash
zrok2 enable <your-token>
```

Verify it's working:

```bash
zrok2 status
```

You should see your account information and environment details.

## Part 2: Your First MCP Server (mcp-bridge)

Let's start simple: expose a filesystem MCP server over the network.

### Install MCP Gateway Tools

```bash
go install github.com/openziti/mcp-gateway/cmd/...@latest
```

This installs all three tools: `mcp-gateway`, `mcp-bridge`, and `mcp-tools`.

### Start the Bridge

```bash
mcp-bridge npx -y @modelcontextprotocol/server-filesystem ~/Documents
```

Output:
```json
{"share_token":"a1b2c3d4e5f6"}
```

The bridge is now running. It:
- Spawned the filesystem MCP server as a subprocess
- Created a zrok private share
- Is ready to accept connections

### Test the Connection

In another terminal, connect with mcp-tools:

```bash
mcp-tools run a1b2c3d4e5f6
```

You're now connected to the remote MCP server via stdio. Any MCP client can use this connection.

Press Ctrl+C to disconnect.

## Part 3: Aggregate Multiple Servers (mcp-gateway)

For real-world use, you'll want to combine multiple MCP servers into one endpoint. That's what `mcp-gateway` does.

### Create a Configuration File

Create `gateway-config.yml`:

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
        # write operations blocked for safety

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
        - "delete_*"  # block destructive operations

  - id: fetch
    transport:
      type: stdio
      command: npx
      args: ["-y", "@modelcontextprotocol/server-fetch"]
```

### Start the Gateway

```bash
export GITHUB_TOKEN="ghp_your_token_here"
mcp-gateway run gateway-config.yml
```

Output:
```json
{"share_token":"x9y8z7w6v5u4"}
```

### Understanding the Configuration

**Tool Namespacing**: Tools are prefixed with the backend ID. With the config above:
- `filesystem:read_file`
- `github:list_issues`
- `fetch:fetch`

The separator (`:` in this example) is configurable.

**Tool Filtering**: Two modes control which tools are exposed:
- `allow` mode: Only tools matching the patterns are exposed
- `deny` mode: All tools except those matching patterns are exposed

Patterns support globs: `read_*` matches `read_file`, `read_directory`, etc.

### Connect to the Gateway

```bash
mcp-tools run x9y8z7w6v5u4
```

You now have access to all three backends through a single connection.

## Part 4: Connect to Your Agent

### Claude Desktop

Locate your Claude Desktop config file:

| Platform | Path |
|----------|------|
| macOS | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| Windows | `%APPDATA%\Claude\claude_desktop_config.json` |
| Linux | `~/.config/Claude/claude_desktop_config.json` |

Add your MCP server configuration:

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

### n8n

For n8n's MCP Client Tool, use HTTP mode:

```bash
mcp-tools http x9y8z7w6v5u4 --bind 127.0.0.1:8080
```

Configure the n8n MCP Client Tool:
- **URL**: `http://127.0.0.1:8080`
- **Transport**: SSE (default) or streamable HTTP

Options:
- `--stateless` - Stateless mode (no session persistence)
- `--json-response` - Prefer JSON responses over SSE streams

### Other Agents

Any MCP client that supports stdio transport can use `mcp-tools run <token>` directly. For HTTP-based clients, use `mcp-tools http`.

## Part 5: Reserved Shares (Persistent Tokens)

By default, share tokens are ephemeral—they disappear when the process exits. For production use, create reserved shares that persist.

### Create a Reserved Share

Share names are globally unique across the zrok instance. Choose a name unlikely to conflict:

```bash
mcp-tools create abc123-gateway
```

Output:
```json
{"share_token":"abc123-gateway"}
```

If the name is taken, you'll get an error—just choose a different name.

### Use the Reserved Share

In your gateway config, add the share token:

```yaml
share_token: abc123-gateway

aggregator:
  name: "my-dev-tools"
  # ... rest of config
```

Or with mcp-bridge:

```bash
mcp-bridge --share-token abc123-gateway npx -y @modelcontextprotocol/server-filesystem ~/Documents
```

Now you can stop and restart the gateway or bridge, and clients can reconnect using the same token.

### Delete When Done

```bash
mcp-tools delete abc123-gateway
```

### Auto-Generated Tokens

If you omit the name, zrok generates a random token:

```bash
mcp-tools create
```

Output:
```json
{"share_token":"abc123xyz789"}
```

Token names must be 3–32 characters, lowercase alphanumeric and hyphens (`[a-z0-9-]`).

## Next Steps

- **Full Configuration Reference**: See [etc/mcp-gateway.yml](etc/mcp-gateway.yml) for all configuration options
- **Architecture Overview**: The [README](README.md) has diagrams showing how components interact
- **Troubleshooting**: Check the development guide in CLAUDE.md for debugging tips

## Common MCP Servers

Here are some well-maintained MCP servers to try:

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

## Troubleshooting

**"zrok enable" required**: The zrok SDK requires an enabled environment. Run `zrok2 enable` with your account token.

**Backend connection failures**:
- Check that stdio commands are correct and executables are in PATH
- For zrok backends, verify the share token is valid and the remote bridge is running

**Tool not found**:
- Check the namespace prefix matches the backend ID
- Verify the tool isn't filtered by your allow/deny list
- Check that the backend successfully connected (look for connection logs)
