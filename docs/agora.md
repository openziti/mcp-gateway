# Agora Integration

MCP Gateway can participate in an Agora network as a first-class Layer 1
service. Agora support is additive for `mcp-gateway`: a gateway can serve over
zrok and Agora at the same time, and it can aggregate backends reached over
stdio, zrok, HTTP(S), or Agora tunnels.

The three binaries use Agora in different ways:

1. `mcp-gateway` can publish a catalog advertisement, serve its aggregated MCP
   endpoint over an Agora tunnel, and connect to Agora-backed MCP backends.
2. `mcp-bridge` can expose one stdio MCP server over Agora for one invocation.
3. `mcp-tools` can connect MCP clients to an Agora-served gateway or bridge.

`mcp-bridge` and `mcp-tools` choose one network per invocation. `mcp-gateway`
is config-driven and can run zrok and Agora listeners concurrently.

## Prerequisites

Agora mode requires:

- An enrolled Agora environment on the host running the binary.
- A reachable Agora controller.
- An `agora.api_endpoint` value matching the enrolled environment.
- A Ziti fabric reachable by the Agora runtime.

The `api_endpoint` value is validate-only. The code compares it with the
endpoint in the enrolled Agora environment and exits if they differ; it does
not rewrite Agora environment files.

For demo-bootstrap or other provisioning flows, prefer an integration file.
The integration file carries provisioned environment and catalog IDs while the
main config keeps operator choices such as serving, zrok, backend selection,
and catalog publishing.

## Configuration

### mcp-gateway

`mcp-gateway` reads Agora settings from the gateway YAML config. This example
runs Agora-only by disabling zrok sharing:

```yaml
zrok:
  share:
    enabled: false

agora:
  enabled: true
  integration_file: ""          # optional; see "Integration File"

  api_endpoint: "http://127.0.0.1:18081"
  env_root: ""                  # optional; SDK default or AGORA_ENV_ROOT may apply

  instance_name: "mcp-gateway"
  description: "MCP gateway"
  tunnel_mode: tcp              # tcp, http, or udp; default: tcp

  serve:
    enabled: true
    grants: []

  advertisement:
    publish: true               # default true when agora.enabled is true
    workgroup_ids:
      - wg_abcdefghijkl         # required when publish is true
    contract_id: con_abcdefghijkl
    capabilities: []            # derived when empty

aggregator:
  name: "mcp-gateway"
  version: "1.0.0"
  separator: ":"

backends:
  - id: filesystem
    transport:
      type: stdio
      command: mcp-filesystem
      args: ["/tmp"]
```

To serve over both zrok and Agora, leave `zrok.share.enabled: true` and set
`agora.serve.enabled: true`.

### mcp-bridge

`mcp-bridge` is CLI-driven. `--network=agora` enables Agora, enables Agora
serve and advertisement publishing, and disables zrok for that invocation.

```bash
mcp-bridge \
  --network=agora \
  --agora-integration-file /path/to/integration.mcp-bridge.yaml \
  mcp-filesystem /tmp
```

Bridge advertisements derive capabilities from `mcp-tools`, the bridged
command tag, and `agora-serve`. Use `--env`, `--working-dir`, and command args
the same way as zrok bridge mode.

### mcp-tools

`mcp-tools` is also CLI-driven. It can connect to a zrok share token or one
Agora tunnel target:

```bash
mcp-tools run <share_token>
mcp-tools run --agora mcp-gateway

mcp-tools http <share_token> --bind 127.0.0.1:8080
mcp-tools http --agora mcp-gateway --bind 127.0.0.1:8080
```

For Agora mode, `mcp-tools` uses a minimal Agora config containing
`api_endpoint` and `env_root`. It does not publish advertisements.

```bash
mcp-tools run \
  --agora mcp-gateway \
  --agora-integration-file /path/to/integration.mcp-tools.yaml
```

## CLI

### mcp-gateway

| Command or flag | Effect |
|---|---|
| `mcp-gateway run <config.yml>` | Runs the gateway with config-driven zrok and Agora settings |
| `--network=agora` | Sets `agora.enabled: true` after config load |
| `--network=zrok` | Accepted as a no-op shortcut; zrok remains config-driven |
| `--agora-integration-file <path>` | Overrides `agora.integration_file` |

`AGORA_MCP_GATEWAY_INTEGRATION_FILE` sets `agora.integration_file` when the
CLI flag is not provided.

### mcp-bridge

| Command or flag | Effect |
|---|---|
| `mcp-bridge <command> [args...]` | Existing zrok bridge behavior |
| `--network=agora` | Enables Agora serve and publish, disables zrok share |
| `--network=zrok` | Existing zrok behavior |
| `--agora-integration-file <path>` | Sets the Agora integration file |
| `--env KEY=VALUE` | Passes environment values to the stdio MCP server |
| `--working-dir <dir>` | Sets the stdio MCP server working directory |

`AGORA_MCP_BRIDGE_INTEGRATION_FILE` sets the integration file when the CLI flag
is not provided.

### mcp-tools

| Command or flag | Effect |
|---|---|
| `mcp-tools run <share_token>` | Connects stdio MCP to a zrok share |
| `mcp-tools run --agora <tunnel>` | Connects stdio MCP to an Agora tunnel |
| `mcp-tools http <share_token> --bind <addr>` | Serves local HTTP backed by zrok |
| `mcp-tools http --agora <tunnel> --bind <addr>` | Serves local HTTP backed by Agora |
| `--agora-integration-file <path>` | Sets the Agora integration file |
| `--stateless` | HTTP mode only; uses stateless streamable HTTP behavior |
| `--json-response` | HTTP mode only; prefers JSON responses over SSE |

`AGORA_MCP_TOOLS_INTEGRATION_FILE` sets the integration file when the CLI flag
is not provided.

## Integration File

The integration file is a partial `agora:` block. It is normally produced by
provisioning or demo-bootstrap tooling.

```yaml
api_endpoint: "http://127.0.0.1:18081"
env_root: "/home/example/.agora-demo/envs/mcp-gateway@gateway-services-org"
advertisement:
  workgroup_ids:
    - wg_abcdefghijkl
  contract_id: con_abcdefghijkl
```

The gateway and bridge merge these fields only when the same field is unset in
the main config:

| Field | Merge rule |
|---|---|
| `api_endpoint` | Used only when `agora.api_endpoint` is empty |
| `env_root` | Used only when `agora.env_root` is empty |
| `advertisement.workgroup_ids` | Used only when no inline workgroup IDs are set |
| `advertisement.contract_id` | Used only when no inline contract ID is set |

Integration-file path precedence is:

1. `--agora-integration-file`
2. Binary-specific environment variable
3. `agora.integration_file` in the main config

Values inside the main config override values loaded from the integration
file. `mcp-tools` reads only the environment fields it needs for dialing and
does not require advertisement IDs.

## Advertisement

When `agora.enabled: true`, advertisement publishing defaults to enabled.
Set `advertisement.publish: false` to use Agora serving or backend connects
without publishing a catalog card.

When publishing is enabled:

- `advertisement.workgroup_ids` must contain at least one `wg_` ID.
- `advertisement.contract_id`, when set, must be a `con_` ID.
- `advertisement.capabilities`, when empty, is derived from the binary config.

`mcp-gateway` derives capabilities from:

| Condition | Capability |
|---|---|
| Always when publishing with defaults | `mcp-tools` |
| Each configured backend | Backend ID, such as `filesystem` or `github` |
| Agora serve is enabled | `agora-serve` |

`mcp-bridge` derives capabilities from `mcp-tools`, a command tag such as
`filesystem`, and `agora-serve` when serving is enabled.

## Serving Over Agora

Set `agora.serve.enabled: true` to serve `mcp-gateway` over Agora. The gateway
allocates a local loopback listener for Agora traffic and starts the same MCP
HTTP/SSE handler used by the zrok listener.

For `mcp-gateway`, zrok and Agora are independent:

```yaml
zrok:
  share:
    enabled: true

agora:
  enabled: true
  serve:
    enabled: true
```

For `mcp-bridge`, `--network=agora` selects Agora-only bridge mode for the
invocation. It does not create a zrok share.

For `tunnel_mode: http`, the serve backend target includes an `http://` scheme.
TCP mode uses a host:port target.

## Per-Backend `transport.type: agora`

Gateway backends can point at remote MCP services exposed through Agora. This
is useful when a backend is running behind `mcp-bridge --network=agora` or
another Agora-served MCP endpoint.

```yaml
agora:
  enabled: true

backends:
  - id: remote-filesystem
    transport:
      type: agora
      agora_tunnel: filesystem-relay
```

`agora_tunnel` is the Agora Layer 1 tunnel name. It is mutually exclusive with
the stdio, zrok, and HTTP transport fields. `agora.enabled: true` is required
when any backend uses `transport.type: agora`.

At startup, the gateway creates Agora connects for all Agora backends before
backend discovery. Per-client sessions use the same loopback resolver, so tool
calls and discovery route through the same Agora connection.

## Connecting From mcp-tools

Use `mcp-tools run --agora <tunnel>` when an MCP client expects stdio:

```bash
mcp-tools run --agora mcp-gateway-engineering
```

Use HTTP mode when the client expects streamable HTTP:

```bash
mcp-tools http --agora mcp-gateway-engineering --bind 127.0.0.1:8080
```

The `--agora` flag is mutually exclusive with a positional zrok share token in
both modes.

## Lifecycle

Agora startup follows this order:

1. Load config or parse CLI args.
2. Apply CLI and environment overrides.
3. Resolve the integration file.
4. Validate config.
5. Construct the Agora client or subsystem.
6. Bootstrap Agora connects.
7. Start local HTTP servers.
8. Start Agora serve when enabled.
9. Publish the advertisement when enabled.

On shutdown, the subsystem retracts the advertisement, removes serve, removes
connects, closes the Agora agent, and continues cleanup even if one Agora
cleanup step fails.

## Manual Smoke

Run these checks against a live Agora controller and Ziti fabric:

| Scenario | Expected observation |
|---|---|
| Agora-only gateway: `agora.enabled: true`, `agora.serve.enabled: true`, `zrok.share.enabled: false` | `mcp-tools run --agora <gateway tunnel>` lists and calls tools |
| Dual listener gateway: zrok share and Agora serve both enabled | zrok share token and Agora tunnel both respond |
| Agora backend: gateway backend uses `transport.type: agora` against `mcp-bridge --network=agora` | Discovery and tool calls route through the remote bridge |
| Catalog | The dashboard shows the `mcp-gateway` card with the gateway-product accent |
| HTTP mode | `mcp-tools http --agora <gateway tunnel> --bind 127.0.0.1:8080` exposes a local HTTP MCP endpoint |
