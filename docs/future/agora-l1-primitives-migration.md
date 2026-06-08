# Spec — Migrate Agora to Embedded Listen/Dial Primitives

## Context

The Agora integration shipped against v0.1.0 of the SDK, which exposed only a
*managed-proxy* tunnel API (`tunnel.EnsureServed` / `tunnel.EnsureConnected`).
That API could not hand the application a raw connection, so the integration was
built as a workaround: every Agora serve allocated a plain `127.0.0.1` loopback
listener and pointed an Agora Layer 1 tunnel at it; every Agora backend allocated
its own loopback listener fronting a connect tunnel, and per-client sessions
reached backends through a loopback *resolver* that mapped backend IDs to those
local ports. The loopback hop became the security boundary, and three separate
items were consciously deferred because the workaround made them awkward
(loopback hardening, multiplexing one connect across backends, persistent named
shares).

The SDK has since been amended (post-v0.1.2, currently untagged HEAD) to expose
thin `Listen`/`Dial` primitives that return raw `net.Listener` / `net.Conn` and
require no embedded runtime — the same shape zrok's embedded SDK already has in
this repo. This migration tears out the tunnel+loopback scaffolding and embeds
Agora the way zrok is embedded, while keeping the advertisement/catalog model
exactly as it stands.

The intended outcome: Agora and zrok become structurally parallel transports.
The loopback middleman disappears, the security boundary returns to the fabric,
and Agora gains the persistent-named-share capability that zrok already has.

## The Model

The new SDK splits into two layers that map almost one-to-one onto the existing
zrok embedding:

| zrok (today) | agora (new primitives) |
|---|---|
| `environment.LoadRoot()` | `agent.NewStandalone({EnvRoot})` — `WithRuntime: false` |
| `sdk.CreateShare → Share{Token}` | `tunnel.Create(ctx, agent, Spec) → Tunnel{Name, tt_ID}` |
| `sdk.NewListener(token, root) → net.Listener` | `tunnel.Listen(ctx, agent, name) → net.Listener` |
| `sdk.CreateAccess(root, {token}) → Access` | `tunnel.Attach(ctx, agent, name) → Attachment` |
| `sdk.NewDialer(token, root) → net.Conn` | `tunnel.Dial(ctx, agent, name) → net.Conn` |
| `sdk.DeleteShare` | `tunnel.Delete` / `tunnel.Detach` |

`Listen` and `Dial` are thin: no runtime, no heartbeat, no managed status, no
local proxy. The gateway's existing MCP HTTP/SSE handler binds directly to the
Agora `net.Listener`; backends and `mcp-tools` get a `net.Conn` straight into
`http.Transport.DialContext`. A single process-wide `*agent.Agent` (no runtime)
backs the serve listener, every backend attach/dial, and catalog publish/retract.

**The one genuinely new wrinkle.** zrok folds provisioning and binding together —
`CreateShare` hands you the token you then listen on. Agora *splits* them:
`Create`/`Attach` own the controller record, the Ziti policy, the grants, and
persistence; `Listen`/`Dial` own nothing. So the integration must decide who
calls `Create`/`Attach` and when. The codebase already carries the answer in
`gateway/share.go`, which distinguishes `NewShare` (create, managed, delete on
exit) from `NewShareFromToken` (bind to an existing persistent share, leave it
alone). The same fork resolves the Agora question.

## Design Decisions

**1. Hybrid provisioning — create-if-absent, bind-if-present, delete-only-what-we-created.**
The serve side resolves a tunnel name (defaulting to the instance name). If a
tunnel already exists on the controller under that name, the process *binds* to
it and never deletes it — this is the persistent named share. If no such tunnel
exists, the process *creates* one at startup and deletes it at shutdown — the
ephemeral path that preserves today's UX. This mirrors `share.go`'s `managed`
flag exactly and delivers zrok-parity persistent Agora shares as a side effect
rather than a separate feature.

**2. Grants ride the create path.** `agora.serve.grants` continues to carry
`GrantEmails` into `tunnel.Create`. On the bind-to-existing path, grants are owned
by whoever provisioned the tunnel (operator or demo-bootstrap tooling); the
process binding to it does not manage them. This keeps the config field meaningful
without making it lie about authority it doesn't have.

**3. `tunnel_mode` collapses to a stream constant.** Because `Listen`/`Dial`
return a raw stream and the application owns HTTP/SSE on top, the old tcp/http/udp
distinction no longer changes transport behavior. Tunnels are created in a single
stream mode (TCP); MCP always rides HTTP/SSE over it. `TunnelMode` survives only
as advertisement/discovery metadata in `catalog.PublishSpec`, where it is honest
about being a label rather than a switch. The user-facing `tunnel_mode` knob is
removed from operator config. (UDP is rejected by the primitives and is out of
scope regardless.)

**4. The loopback resolver is deleted, not reimplemented.** Per-client sessions
no longer indirect through a backend-ID→loopback-port map. A `transport.type:
agora` backend calls `tunnel.Attach(name)` once at startup (idempotent by
environment+tunnel, so several backends naming the same tunnel share one
attachment) and `tunnel.Dial(name)` per connection inside `DialContext`. The
"multiplexing one connect across backends" deferral dissolves: there is nothing
left to multiplex.

**5. The advertisement/catalog layer is untouched.** `catalog.EnsurePublished`,
`PublishSpec`, capability derivation, the integration-file merge for workgroup and
contract IDs, retract-on-shutdown — all unchanged. This migration is a transport
swap beneath a stable advertisement model.

**6. Thin primitives mean fabric-level lifecycle.** Deleting a tunnel or detaching
revokes at the controller; OpenZiti terminates live sessions; the application
never force-closes established connections. Shutdown retracts the advertisement,
closes the listener, then deletes the tunnel (only if created) or detaches, then
closes the Agent — continuing cleanup even if one step fails, as today.

**7. Local `replace` during development.** `go.mod` gains a temporary
`replace github.com/openziti/agora => /home/michael/Repos/nf/agora`. The work is
unblocked immediately; swapping to a tagged release is the final pre-merge step
and is recorded as a follow-up so it cannot be silently forgotten.

## Illustrative Config (shape, not final keys)

The serve surface gains an explicit tunnel name with create-or-bind semantics;
`tunnel_mode` and the `serve` loopback machinery fall away. Keys are illustrative —
the planning agent grounds the exact schema.

```yaml
agora:
  enabled: true
  api_endpoint: "http://127.0.0.1:18081"
  env_root: ""
  instance_name: "mcp-gateway"
  description: "MCP gateway"

  serve:
    enabled: true
    tunnel: "mcp-gateway"     # bind if it exists (persistent), else create+delete (ephemeral)
    grants: []                # applied only on the create path

  advertisement:
    publish: true
    workgroup_ids: [wg_abcdefghijkl]
    contract_id: con_abcdefghijkl
    capabilities: []          # derived when empty (unchanged)

backends:
  - id: remote-filesystem
    transport:
      type: agora
      agora_tunnel: filesystem-relay   # Attach once + Dial per connection
```

## Scenarios

- **Ephemeral gateway (today's UX, preserved).** `serve.tunnel` unset or naming a
  tunnel that doesn't exist yet → the gateway creates it at startup, serves the MCP
  handler on the Agora `net.Listener`, publishes its card, and deletes the tunnel
  on shutdown. `mcp-tools run --agora mcp-gateway` attaches, dials, lists and calls
  tools — no loopback anywhere in the path.

- **Persistent named gateway (new, zrok parity).** An operator (or demo-bootstrap)
  provisions `mcp-gateway` once via `tunnel.Create`. The gateway, configured with
  `serve.tunnel: mcp-gateway`, binds to it and leaves it intact across restarts.
  Clients reconnect to the same name through restarts — the analogue to
  `zrok create share my-gateway` the old deferred note said Agora lacked.

- **Gateway aggregating an Agora backend.** A backend with `transport.type: agora`
  and `agora_tunnel: filesystem-relay` (served by `mcp-bridge --network=agora`)
  attaches once at startup; every client session dials it directly. Two backends
  naming the same tunnel share one attachment.

- **Dual listener.** zrok share and Agora serve both enabled → both respond, now
  fully symmetric in how they're embedded.

## Surfaces That Change (design altitude)

This section is named at design altitude deliberately; the planning agent grounds
it into a file-by-file work order.

- **`agora/` package shrinks to its essence.** The Subsystem keeps Agent
  construction, tunnel create-or-bind + `Listen`, backend `Attach`, a `Dial`
  helper, catalog publish/retract, and capability derivation. It sheds
  `allocateLoopbackPort`, the loopback connect map / `ConnectAddress` resolver,
  the `EnsureServed`/`EnsureConnected` calls, and `ServeSpec`/`ConnectSpec`
  plumbing. `WithRuntime` flips to `false`.
- **An Agora serve abstraction parallel to `gateway/share.go`** carries the
  create-or-bind + managed-delete logic, so the gateway and bridge consume it the
  way they consume `Share`.
- **An Agora dial abstraction parallel to `tools/access.go`** wraps Attach + a
  `DialContext` over `tunnel.Dial`, consumed by both the gateway's agora-backend
  path and `mcp-tools --agora`.
- **`gateway/session.go` loses the resolver indirection** — `connectAgoraBackend`
  dials directly. `gateway/backend.go` and `bridge/bridge.go` lose their
  `agoraListener` loopback fields, `agoraServeBackendTarget`, and
  loopback-target collection; they bind the HTTP server to the Agora listener.
- **Config** drops `tunnel_mode` (operator-facing) and the loopback `serve`
  target machinery; gains the serve `tunnel` name. Advertisement config unchanged.
- **`go.mod`** gains the temporary `replace`.

## Deferred (and Why)

- **Swap local `replace` → tagged agora release.** A release-coordination
  follow-up, not design work. Tracked so the temporary replace can't ship.
- **Unifying `mcp-tools`' two transport paths.** Now nearly free given the
  symmetry, but still optional. Keep the paths parallel unless a concrete case
  needs one client to span both fabrics; revisit then.
- **Reconnect / resilience for long-lived serve.** The thin primitives carry no
  heartbeat or retry; a revoked tunnel surfaces as a `net.Listener`/`net.Conn`
  error, exactly as zrok's listener does. Matching zrok's posture is the MVP. Any
  active-healing layer is a separate concern, adjacent to the existing
  [gateway-wedge-resilience](./gateway-wedge-resilience.md) thinking, and not
  pulled in here.
- **Cross-org dial policy design.** `Dial` resolves cross-org via can-connect
  authorization while `Listen` is same-org; exposing and shaping that policy
  surface is future product work, out of scope for the transport swap.
- **Hardening the loopback listeners** (from the prior deferred note) is *dropped*,
  not deferred — there is no loopback left to harden.

## Verification

- **Unit/build.** `go build ./...` and `go test ./...` with the `replace` in
  place; the agora package tests are rewritten against the new primitives (the
  old loopback-allocation tests are removed).
- **Manual smoke against a live controller + Ziti fabric**, refreshing the table
  in [../current/agora.md](../current/agora.md):
  - Ephemeral Agora-only gateway: `mcp-tools run --agora <tunnel>` lists and calls
    tools; confirm no `127.0.0.1` Agora listener exists in the process.
  - Persistent named tunnel: pre-provision, bind, restart the gateway, reconnect
    under the same name.
  - Agora backend: gateway with `transport.type: agora` against
    `mcp-bridge --network=agora`; discovery and tool calls route through.
  - Dual listener: zrok token and Agora tunnel both respond.
  - HTTP mode: `mcp-tools http --agora <tunnel> --bind 127.0.0.1:8080`.
- **Docs.** [../current/agora.md](../current/agora.md) updated to describe
  Listen/Dial, the create-or-bind serve model, and persistent named tunnels;
  [./agora-deferred.md](./agora-deferred.md) updated to retire the three dissolved
  items and record the new deferrals above.
