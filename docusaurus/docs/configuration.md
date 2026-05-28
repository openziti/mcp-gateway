---
title: Configuration reference
sidebar_label: Configuration
---

# Configuration reference

The gateway is configured with a YAML file passed to `mcp-gateway run`. This page covers every
top-level key and option.

## Top-level structure

A complete config file looks like this:

```yaml
share_token: "my-gateway"    # optional — see Persistent shares

aggregator:
  name: "my-gateway"
  version: "1.0.0"
  separator: ":"
  connection:
    connect_timeout: 30s
    call_timeout: 60s

backends:
  - id: my-backend
    transport:
      type: stdio
      command: my-command
      args: ["arg1"]
      env:
        MY_VAR: "${MY_VAR}"
    tools:
      mode: allow
      list:
        - "tool_name"
```

## Aggregator settings

The `aggregator` block configures the gateway's identity and connection behavior:

- **`name`**: Gateway name, returned in tool-list responses.
- **`version`**: Gateway version, returned in tool-list responses.
- **`separator`**: Character used to namespace tool names (default: `_`). See [Tool namespacing](#tool-namespacing).
- **`connection.connect_timeout`**: Time to wait when connecting to a backend (default: `30s`).
- **`connection.call_timeout`**: Time to wait for a tool call to complete (default: `60s`).

## Backends

Each entry in the `backends` list defines one backend MCP server. Every backend requires an `id`
and a `transport` block.

### Tool namespacing

The backend `id` is used as the namespace prefix for every tool the backend exposes, combined with
the `separator` set in the `aggregator` block:

| Backend | Original tool | Namespaced tool |
|---------|---------------|-----------------|
| docs | `read_file` | `docs:read_file` |
| docs | `write_file` | `docs:write_file` |
| data | `read_file` | `data:read_file` |

Common separator choices:

| Separator | Example | Notes |
|-----------|---------|-------|
| `_` (default) | `docs_read_file` | Blends in with snake_case names |
| `:` | `docs:read_file` | Visually distinct |
| `-` | `docs-read_file` | Can be ambiguous with hyphenated tool names |

### Transport types

- **`stdio`**: Spawns a local process and communicates over stdin/stdout. Use the `env` map to pass
  environment variables to the process; values support `${VAR}` substitution from the shell
  environment:

  ```yaml
  transport:
    type: stdio
    command: mcp-filesystem
    args: ["~/Documents"]
    env:
      GITHUB_TOKEN: "${GITHUB_TOKEN}"
  ```

- **`zrok`**: Connects to a remote bridge over the zrok overlay:

  ```yaml
  transport:
    type: zrok
    share_token: "remote-token"
  ```

- **`https`**: Connects to a remote MCP server over HTTPS. Only accepts `https://` endpoints.
  Supports SSE (default) or streamable HTTP transport, with optional custom headers and TLS
  configuration.

  With custom headers:

  ```yaml
  transport:
    type: https
    endpoint: "https://mcp.example.com/sse"
    headers:
      Authorization: "Bearer sk-abc123"
  ```

  With a custom CA cert and streamable HTTP protocol:

  ```yaml
  transport:
    type: https
    endpoint: "https://mcp.internal.corp/mcp"
    protocol: "streamable"
    tls:
      ca_cert_file: "/etc/ssl/certs/internal-ca.pem"
  ```

- **`http`**: Connects to a remote MCP server over HTTP or HTTPS. Unlike `https`, accepts both
  `http://` and `https://` endpoints, but plaintext HTTP requires explicit opt-in:

  ```yaml
  transport:
    type: http
    endpoint: "http://localhost:8080/sse"
    allow_insecure: true
  ```

### Tool filtering

By default, every tool from every backend is exposed. Use allow or deny lists to control this
per backend.

**Allow mode**: Only expose tools that match:

```yaml
tools:
  mode: allow
  list:
    - "read_file"
    - "list_directory"
```

**Deny mode**: Expose everything except tools that match:

```yaml
tools:
  mode: deny
  list:
    - "write_file"
```

**Glob patterns**: `*` matches any sequence of characters, `?` matches a single character:

| Pattern | Matches |
|---------|---------|
| `read_file` | Exactly `read_file` |
| `read_*` | `read_file`, `read_dir`, ... |
| `*file` | `read_file`, `write_file` |
| `*` | Everything |

Omit the `tools` section entirely to expose all tools.

## Example: Multi-backend configuration

A three-backend setup combining filesystem access, GitHub, and web fetching:

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
