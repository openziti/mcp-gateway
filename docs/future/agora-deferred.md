# Agora Integration — Deferred Work

The Agora integration shipped across `mcp-gateway`, `mcp-bridge`, and `mcp-tools` (serve, publish, and per-backend connect over Layer 1 tunnels). This note records the work that was consciously left out of that slice, so the deferral is intentional rather than forgotten. The realized behavior lives in [../current/agora.md](../current/agora.md).

## Persistent named shares over Agora

zrok has named, persistent shares; an operator can reserve `my-gateway` and have clients reconnect to the same token across restarts. Agora has no direct analogue today — Layer 1 tunnels are named per-runtime, and catalog advertisements record presence rather than a stable dial target. Revisit when Layer 2 advertisements grow stable-name semantics; until then, Agora dial targets are tunnel names resolved at connect time.

## Multiplexing one connect across multiple backends

Each `transport.type: agora` backend currently gets its own connect and its own loopback listener. When several backends point at the same upstream Agora service, this allocates redundant connects. It is correct and not a blocker, just inefficient. A future change could share one connect (and one loopback) across backends that resolve to the same tunnel, keyed by tunnel name rather than backend ID.

## Unifying mcp-tools' two transport paths

`mcp-tools` keeps its zrok and Agora dial paths parallel and uncombined. In particular, `--agora` does not write the Agora identity into the local zrok metadata cache. Merging the two paths would let a single invocation reason about both fabrics, but the parallel design is simpler and keeps existing zrok invocations (and Claude Desktop configs) unchanged. Unify only if a concrete use case needs one client to span both transports.

## Hardening the loopback serve/connect listeners

The Agora serve target and per-backend connects are plain `127.0.0.1` listeners with no rate limiting and no TLS. For the MVP, the loopback boundary is the security boundary — only local processes can reach these listeners. Production hardening (authn on the loopback hop, TLS, connection limits) is deferred until there's a deployment that needs more than the loopback boundary provides.
