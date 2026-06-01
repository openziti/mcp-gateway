# Agora Demo Bootstrap Handoff

This is the mcp-gateway side of the Agora dashboard demo handoff.

## Integration File

`demo-bootstrap` should write the mcp-gateway integration file inside the
gateway account environment root:

```text
<env_root>/integration.mcp-gateway.yaml
```

Expected shape:

```yaml
api_endpoint: "http://127.0.0.1:18081"
env_root: "/path/to/.agora-demo/envs/mcp-gateway@gateway-services-org"
advertisement:
  workgroup_ids:
    - wg_xxxxxxxxxxxx
  contract_id: con_xxxxxxxxxxxx
```

The main gateway config keeps operator choices such as serving, zrok, backend
set, and catalog publishing. The integration file carries only provisioned
Agora environment and catalog IDs.

## Demo Launch

Use [etc/demo-mcp-gateway.yml](../../etc/demo-mcp-gateway.yml) as the gateway
config. It is Agora-only for the dashboard demo, serves the gateway over an
Agora Layer 1 tunnel, and explicitly enables catalog publication.

```bash
export AGORA_MCP_GATEWAY_INTEGRATION_FILE="<env_root>/integration.mcp-gateway.yaml"
mcp-gateway run --network=agora /path/to/mcp-gateway/etc/demo-mcp-gateway.yml
```

The demo config uses the bundled `mcp-filesystem` backend. Install the project
binaries with `go install github.com/openziti/mcp-gateway/cmd/...@latest` or
point `PATH` at locally built binaries before starting the demo.
