# Agora Integration — Deferred Work

The Agora integration shipped across `mcp-gateway`, `mcp-bridge`, and `mcp-tools` (serve, publish, and per-backend connect over Layer 1 tunnels). It was then migrated off the v0.1.0 managed-proxy tunnel API and its loopback workaround onto the SDK's thin `Listen`/`Dial` primitives, embedding Agora the way zrok is embedded. This note records what remains deliberately out of scope. The realized behavior lives in [../current/agora.md](../current/agora.md).

## Resolved by the Listen/Dial migration

Four items from the original deferral list are no longer pending — the transport swap dissolved or delivered them:

- **Persistent named shares over Agora — delivered.** Create-or-bind serving means an operator (or demo-bootstrap) can provision a tunnel once; the gateway binds to it under `serve.tunnel` and leaves it intact across restarts. This is the analogue to `zrok create share my-gateway`.
- **Multiplexing one connect across multiple backends — dissolved.** There is no loopback connect left to multiplex. Each `transport.type: agora` backend dials its tunnel directly; backends naming the same tunnel share a single startup attachment by construction.
- **Hardening the loopback serve/connect listeners — dropped.** There is no loopback listener left to harden. The security boundary returned to the fabric, exactly as it is for zrok.
- **Swap the local `replace` for a tagged Agora release — done.** The migration was developed against a temporary `replace github.com/openziti/agora => <local checkout>` while the `Listen`/`Dial` primitives (and the additive `tunnel.Get` helper used for the wrong-mode bind check) lived on untagged Agora HEAD. Agora v0.1.3 ships both; `go.mod` now requires `v0.1.3` with no `replace`.

## Unifying mcp-tools' two transport paths

`mcp-tools` keeps its zrok and Agora dial paths parallel and uncombined. The migration made them structurally symmetric (`Serve`↔`Share`, `Dialer`↔`Access`), so merging is now nearly free — but still optional. The parallel design keeps existing zrok invocations (and Claude Desktop configs) unchanged. Unify only if a concrete use case needs one client to span both fabrics.

## Reconnect / resilience for long-lived serve

The thin primitives carry no heartbeat, retry, or managed status. A revoked tunnel surfaces as a `net.Listener` or `net.Conn` error, exactly as zrok's listener does — matching zrok's posture is the MVP. Any active-healing layer is a separate concern, adjacent to the [gateway-wedge-resilience](./gateway-wedge-resilience.md) thinking, and not pulled in here.

## Cross-org dial policy design

`Dial` resolves cross-org via can-connect authorization while `Listen` is same-org. Exposing and shaping that policy surface — who may dial whose tunnels across organizations — is future product work, out of scope for the transport swap.

## Dynamic backend add/remove at runtime

Backend config is static at startup, so the dialer attaches each unique tunnel once at startup and detaches everything at shutdown — no per-backend ref-counting. Supporting backends that come and go while the process runs would require ref-counted attachments; revisit if runtime backend churn becomes a requirement.
