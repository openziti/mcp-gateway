# Migrate Agora to Embedded Listen/Dial Primitives — Work Order

## Context

This work order implements [agora-l1-primitives-migration.md](agora-l1-primitives-migration.md). The Agora integration shipped against SDK v0.1.0's managed-proxy tunnel API, which could not hand the application a raw connection, so it was built on a loopback workaround: every serve allocated a `127.0.0.1` listener fronted by an Agora tunnel, every backend allocated its own loopback fronting a connect tunnel, and per-client sessions reached backends through a backend-ID→loopback-port resolver. The loopback hop became the security boundary.

The SDK now (post-v0.1.2, local HEAD `v0.1.2-9-gef89c63`) exposes thin `Listen`/`Dial` primitives that return raw `net.Listener`/`net.Conn` with no embedded runtime — the same shape zrok already has in this repo. This work tears out the tunnel+loopback scaffolding and embeds Agora the way zrok is embedded, keeping the advertisement/catalog model exactly as it stands. It is a transport swap beneath a stable advertisement layer.

### Grounding (spec verified against both repos)

The spec's file-level "Surfaces That Change" map is accurate and every SDK symbol it assumes exists under the names it uses (`agent.NewStandalone`, `tunnel.Create/Listen/Attach/Dial/Delete/Detach`, `catalog.EnsurePublished/Retract/PublishSpec`). Three grounding facts shape the tracks below:

- **Clean consumer-side swap.** The new agora HEAD *keeps* the `catalog` package and *keeps* `tunnel.EnsureServed`/`EnsureConnected`. The SDK does not break; we simply stop calling the managed-proxy API. The advertisement layer is genuinely untouchable.
- **`tunnel.Attach` is idempotent by `(environment, tunnel)`** — verified in the controller (`internal/controller/connectTunnel.go`): the dialer path looks up the existing active attachment and returns it unchanged (`:75-79`), and recovers from the concurrent unique-violation race by re-fetching and returning the existing record (`:135-147`). `ConnectTunnelConflict` fires only for an ambiguous tunnel name or a UDP tunnel — never for a duplicate attach. So "share one attachment" is SDK-guaranteed; the consumer just attaches each tunnel once and must not detach it while siblings still use it (see below).
- **`tunnel.Detach` is not ref-counted** — there is exactly one active dialer attachment per `(environment, tunnel)`, and a single `Detach` removes that shared record for everyone (`internal/controller/dialerAttachment.go:37,85`). Hence the dial abstraction attaches each unique tunnel once at startup and detaches once at process shutdown — never on a per-session or per-connection path — so no early detach can strand a sibling that shares the tunnel.
- **No "tunnel exists" call** — create-or-bind is `Listen → ErrNotFound → Create → Listen`, `managed=true` only on the create branch, `Create→ErrConflict` falling back to bind.

## Architectural decisions (settled)

- **Single process-wide runtime-less Agent.** `agent.NewStandalone({Name, Description, EnvRoot, WithRuntime: false})`. One `*agent.Agent` backs the serve listener, every backend attach/dial, and catalog publish/retract. (Already the shape today — one `Subsystem` owns the Agent — only `WithRuntime` flips to `false`.)
- **Create-or-bind serve, delete-only-what-we-created.** Resolve a tunnel name (`serve.tunnel`, defaulting to `instance_name`). `Listen`; on `ErrNotFound`, `Create` (mode TCP, grants) then `Listen`, and mark `managed=true`. A `Create→ErrConflict` race falls back to bind (`managed=false`). Mirrors `gateway/share.go`'s `managed` flag exactly; delivers persistent named shares as a side effect.
- **Binding to a wrong-mode tunnel is a hard error.** On the bind path, an existing tunnel under that name whose mode is not TCP fails fast with a clear message rather than binding silently (`Listen` itself would accept an `http`-mode tunnel, so the check is explicit).
- **Advertisement reports `tunnel_mode = "http"` (constant).** The agora tunnel is always created TCP; the advertisement honestly labels what a client speaks. No operator knob.
- **Process-owned dialer attachments (attach at startup, detach at shutdown).** The attachment is a control-plane reservation, singular and idempotent per `(environment, tunnel)`; one serves every backend and session on that name. The gateway attaches each unique agora-backend tunnel once at startup (front-loading OpenZiti policy provisioning out of the connection hot path) and detaches them all at shutdown — the only release path. Because detach happens only as the whole process exits, no backend can strand a sibling sharing the tunnel, so no per-session ref-counting is needed. Per-session acquisition is a bug (see Track C). A real `ConnectTunnelConflict` (ambiguous name / UDP) is surfaced as an error, not swallowed.
- **`Listen`/`Dial` are thin.** No runtime, heartbeat, managed status, or local proxy. The MCP HTTP/SSE handler binds directly to the Agora `net.Listener`; backends get a `net.Conn` straight into `http.Transport.DialContext`.
- **Paths stay parallel, not unified.** Agora becomes structurally parallel to zrok (`Serve`↔`Share`, `Dialer`↔`Access`); unifying `mcp-tools`' two transport paths stays deferred.
- **Dial seam is a bare injected func.** The aggregator reaches agora through `AgoraDialClient func(tunnel string) (*http.Client, error)` (a pure accessor over startup-attached clients — no `ctx`, since attach already happened), injected exactly as today's `ConnectResolver` is. This keeps the aggregator decoupled from the `agora` package; a named interface buys nothing here.
- **Fabric-level lifecycle.** Deleting/detaching revokes at the controller; OpenZiti terminates live sessions; the app never force-closes established connections. Shutdown retracts the advertisement, closes the listener, deletes the tunnel (only if created) or detaches, then closes the Agent — continuing cleanup even if a step fails.

## Tracks

### A. Agora package — shrink the Subsystem to its essence

**`agora/subsystem.go`.** Keep `Subsystem` as the single-Agent holder; reshape it.

- Construct the Agent with `WithRuntime: false` (drop the `wantRuntime := serveWanted || len(targets) > 0` computation at `subsystem.go:152`).
- **Delete** the loopback machinery: `allocateLoopbackPort` / `allocateConnectAddress` (`subsystem.go:22,464`), the `connects map[string]*tunnel.ConnectStatus` field, the `ConnectAddress(key)` resolver (`subsystem.go:386`), `BootstrapConnects` (`subsystem.go:~291`), `StartServing(target)`, and all `ServeSpec`/`ConnectSpec` and `EnsureServed`/`EnsureConnected` usage. The `targets []ConnectTarget` / `ConnectTarget` concept is removed (backends now dial by name straight from config).
- **Reshape the `agoraOps` interface** (`subsystem.go:62-74`) — the test seam — to wrap the new primitives so the package stays unit-testable without a live controller: `Create / Listen / Delete` (serve), `Attach / Dial / Detach` (dial), `EnsurePublished / Retract` (catalog, unchanged), `NewStandalone`. `defaultOps` delegates to `tunnel.*` and `catalog.*`.
- **Catalog publish — mode constant, and the advertised name follows the served tunnel.** The `catalog.PublishSpec` build sets `TunnelMode: catalog.TunnelHTTP` (was `catalog.TunnelMode(identity.TunnelMode)`). It also sets `Name` to the **resolved serve-tunnel name** — the same value `Serve` binds/creates (`cfg.Serve.Tunnel`, default `identity.InstanceName`) — not raw `identity.InstanceName`. **Invariant: the advertisement `Name` IS the client's dial key.** Verified against the SDK: `catalog.PublishSpec` carries no tunnel reference (only `Name`, `Description`, `Capabilities`, `WorkgroupScopeIDs`, `TunnelMode`, `ContractID`), and discovery resolves a card to a tunnel purely by name (`EnsurePublished` looks owned advertisements up by `spec.Name`). So the published `Name` must equal the tunnel clients dial, or discovery advertises a target that isn't listening. The default (`serve.tunnel` unset ⇒ `instance_name`) keeps today's behavior; the bug only appears when they diverge. Resolve this name from the single shared helper regardless of whether serve is enabled in this process (a publish-only gateway still advertises the tunnel name clients should dial, even when another process serves it). Capability derivation, the integration-file workgroup/contract merge, and retract-on-shutdown are otherwise unchanged. `Description` continues to derive from `instance_name`/config.
- **`Close`** order: retract advertisement → close `Serve` listener (delete tunnel iff managed) → `Dialer.Close(ctx)` (detach all) → close Agent. Continue on error, log each failure (preserve today's `withCleanupContext` pattern; `Dialer.Close(ctx)` takes the cleanup context for symmetry with `Serve.Close(ctx)`).

### B. Agora serve abstraction (parallel to `gateway/share.go`)

**New file `agora/serve.go`.** A handle the gateway and bridge consume like `Share`.

```go
type Serve struct {
    sub      *Subsystem
    tunnel   *tunnel.Tunnel    // set only when we created it
    listener net.Listener
    managed  bool              // created-by-us → delete on close; bound → leave
}

func (s *Subsystem) Serve(ctx context.Context) (*Serve, error) // create-or-bind + Listen
func (sv *Serve) Listener() net.Listener
func (sv *Serve) Close(ctx context.Context) error              // close listener; Delete iff managed
```

`Serve(ctx)`: resolve name (`cfg.Serve.Tunnel`, default `identity.InstanceName`); `Listen`; on `ErrNotFound` → `Create({Name, Mode: tunnel.ModeTCP, GrantEmails: cfg.Serve.Grants})` then `Listen`, `managed=true`; on `Create`→`ErrConflict` retry `Listen` with `managed=false`. Grants ride only the create path (spec decision #2).

**Wrong-mode bind is a hard error.** On the bind path (Listen succeeded without our creating the tunnel), resolve the existing tunnel record and require `Mode == tcp`; otherwise return a clear error (e.g. `agora serve tunnel "<name>" exists with mode "<m>", expected tcp`). `Listen` does not enforce TCP (it accepts `http`-mode tunnels), so the check is explicit. The `tunnel` package has no public tunnel accessor, so resolve via `agent.Controller()` (`*api.Client`) `GetTunnel`/`ListTunnels` by name. If a cleaner public accessor is wanted, that's a small additive SDK helper — flag it in review rather than blocking on it.

**`Serve` is all-or-nothing — it unwinds its own partial state on error.** Track allocations as they happen and, on any error before returning, undo them in reverse with the same delete-only-what-we-created rule the normal `Close` uses: if we `Create`d the tunnel and a later step (the second `Listen`) fails, delete that tunnel; if `Listen` opened the listener and the subsequent wrong-mode check fails, close that listener (the bound tunnel is left intact — we never created it). A successful `Serve` returns a complete handle; a failed `Serve` returns an error and leaves nothing behind, so the only resource a caller must clean up is the one it receives. (Callers continue to `Close` whatever `Serve` returns; this rule governs only the construction failure path.)

### C. Agora dial abstraction (parallel to `tools/access.go`)

**New file `agora/dial.go`.** Wraps Attach + a `DialContext` over `tunnel.Dial`.

The attachment (the controller-side "this environment wants to dial tunnel X" reservation) is a **process-level** concern, owned once per unique tunnel name for the life of the process — it is explicitly **not** acquired per client session. Acquiring it on the per-session connect path was the C3 defect: sessions would re-attach and inflate the count, and detach would track session churn instead of configured backends. So the abstraction splits cleanly into a startup `Attach` step, a cheap per-session client fetch, and a shutdown `Close`.

```go
type Dialer struct {
    sub     *Subsystem
    mu      sync.Mutex
    clients map[string]*http.Client // keyed by tunnel name; one shared client per attached tunnel
}

func (s *Subsystem) Dialer() *Dialer
func (d *Dialer) Attach(ctx context.Context, tunnel string) error      // startup: reserve + build the shared client, once per unique name
func (d *Dialer) HTTPClient(tunnel string) (*http.Client, error)       // per session: return the shared client; never attaches, never ref-bumps
func (d *Dialer) Close(ctx context.Context) error                      // shutdown: detach every attached tunnel
```

- **`Attach(ctx, tunnel)`** — called once per *unique* agora-backend tunnel at gateway/tools startup (the replacement for the old `BootstrapConnects` step). It calls `tunnel.Attach` (controller-idempotent, so a redundant call returns the existing reservation — surface a real `ConnectTunnelConflict`, ambiguous-name/UDP, as an error) and builds+caches the shared `*http.Client` whose `Transport.DialContext` ignores `addr` and returns `tunnel.Dial(ctx, agent, name)` — exactly parallel to `tools/access.go:56` (`sdk.NewDialer(token, root)`). Calling it again for an already-attached name is a no-op.
- **`HTTPClient(tunnel)`** — the per-session/per-request accessor. Returns the cached shared client by name; it **does not** attach or change any count. An unknown name (never attached at startup) is a programming error → return an error. Per-connection work is the `Dial` inside `DialContext`; the `http.Client` is safe for concurrent use across sessions.
- **`Close(ctx)`** — detaches every attached tunnel exactly once, at gateway/tools shutdown, taking the caller's cleanup context (symmetry with `Serve.Close(ctx)`; detach is a controller call that should honor a deadline). This is the **only** release path. Because detach happens only when the whole process is going down, no backend can ever strand a sibling that shares the tunnel — the detach-safety the idempotency analysis called for, achieved without per-session ref-counting. (Per-backend live teardown would need a ref-count, but dynamic backend add/remove is out of scope — see Deferred.)

**Rationale — why attach is split from dial, and why it lives at startup (do not re-couple).** An agora attachment is a control-plane authorization edge: attaching provisions an OpenZiti dial policy and is singular/idempotent per `(environment, tunnel)`, whereas a dial is a cheap data-plane action riding it. Front-loading `Attach` to startup keeps the expensive policy provisioning out of the connection hot path, so no client connection pays that latency (the inline, per-connection model — e.g. how the gateway uses zrok today — pays it on first connect). An implementer "simplifying" attach onto the per-session/per-dial path would both drag provisioning latency back into the hot path and reintroduce the shared-reservation teardown bug. Keep attach at startup, dial per connection.

### D. Gateway wiring — bind the listener, dial by name, delete the resolver

- **`gateway/backend.go`** — replace the agora loopback fields/flow:
  - Delete `agoraListener net.Listener` and the `net.Listen("tcp","127.0.0.1:0")` setup (`backend.go:126-133`), `agoraServeBackendTarget()` (`backend.go:413-427`), `collectAgoraConnectTargets` (`backend.go:447`), the `connectResolver` field (`backend.go:32`) and its wiring (`backend.go:87,91,100,215`).
  - Build the listener from `subsystem.Serve(ctx)` and bind `b.agoraServer` to `serve.Listener()` via the existing `serveHTTP(...,"agora")` (`backend.go:287`).
  - **Startup attach step** (replaces the deleted `collectAgoraConnectTargets`/`BootstrapConnects` flow): collect the *unique* `agora_tunnel` names across `transport.type: agora` backends and call `dialer.Attach(ctx, name)` once for each, before discovery. This is where the controller-side reservation is made — once per tunnel, not per session.
  - Inject the dial seam (the `AgoraDialClient` accessor, below) into discovery and the session factory in place of the resolver. The seam is now a pure accessor over the already-attached clients.
  - `Stop()`: `serve.Close(ctx)` (delete-iff-managed) + `dialer.Close(ctx)` (detach all) replace the listener-close/server-shutdown at `backend.go:345-357`.
- **`gateway/session.go`** — rewrite `connectAgoraBackend` (`session.go:214`): drop the `cs.resolver(cfg.ID)` lookup (`session.go:218`); get `httpClient := dialClient(cfg.Transport.AgoraTunnel)` (a pure fetch of the startup-attached shared client — no attach here) and build the SSE transport with it (host is a dummy; `DialContext` routes), mirroring `connectZrokBackend` (`session.go:173`). Remove the `resolver` field (`session.go:59,79`).
- **`gateway/session_factory.go`** — replace the `resolver aggregator.ConnectResolver` field/param (`session_factory.go:17,25`) with the dial seam.
- **`aggregator/backend.go`** — replace the resolver seam with the dial seam: delete `ConnectResolver` type (`backend.go:37`), `connectResolver` field (`:32`), `SetConnectResolver` (`:48`), `resolveAgoraLoopback` (`:253`); rewrite `connectAgoraBackend` (`:202`) to dial via the seam. **Keep aggregator decoupled from the `agora` package** — inject a seam, e.g. `AgoraDialClient func(tunnel string) (*http.Client, error)` (a pure accessor; no `ctx`, since attaching already happened at startup), the same way the resolver is injected today. The gateway binds it to `subsystem.Dialer().HTTPClient`.

### E. Bridge wiring (serve side only)

**`bridge/bridge.go`** — the bridge has no agora client/dial path (one subprocess per client). Delete `agoraListener net.Listener` (`bridge.go:35`), the loopback setup (`bridge.go:101-108`), and `agoraServeBackendTarget()` (`bridge.go:519-532`); bind `b.agoraServer` to `subsystem.Serve(ctx).Listener()` and close it on shutdown, mirroring Track D.

### F. mcp-tools / tools wiring (dial side)

- **`agora/client.go`** — the dial-only `Client` no longer returns a loopback address. Rework it onto the same `Dialer`: `Attach(ctx, service)` once at start, hand out `HTTPClient(service)`, and `Close(ctx)` to detach. (For a single-tunnel, single-process client this is just the gateway pattern with one tunnel.) Drop `dialTargetKey`/`ConnectTarget` usage.
- **`tools/client.go`** — `buildTransport` (`client.go:103-122`): replace the loopback-address SSE endpoint (`http://<loopback>/sse` + `http.DefaultClient`) with the agora `*http.Client` from the dialer and a dummy host, mirroring the zrok branch just below it. `newAgoraClient` (`client.go:137`) drops the `TunnelMode: "tcp"` default.

### G. Config — drop the mode knob, add the serve tunnel name

- **`agora/config.go`** — remove `TunnelMode` from `Config`; add `Tunnel string` to `ServeConfig` (create-or-bind name, defaults to `InstanceName`). `Advertisement` config unchanged.
- **`agora/identity.go`** — remove `TunnelMode` from `Identity`, `Defaults` (`TunnelMode`, `AllowedTunnelModes`), and `resolveIdentity`; delete `modeAllowed`. Add a single resolver for the serve-tunnel name (`cfg.Serve.Tunnel` if set, else `InstanceName`) and use it as the one source of truth for **both** `Serve`'s bind/create name and the catalog publish `Name` (per Track A's dial-key invariant) — they must never read the name from two different places.
- **Default wiring** — drop the `TunnelMode`/`AllowedTunnelModes` defaults passed from the gateway/bridge/tools entrypoints (`cmd/*`).
- Resulting serve surface (illustrative):
  ```yaml
  agora:
    serve:
      enabled: true
      tunnel: "mcp-gateway"   # bind if exists (persistent), else create+delete
      grants: []              # applied only on the create path
  backends:
    - transport: { type: agora, agora_tunnel: filesystem-relay }
  ```

### H. `go.mod` — local replace

Add `replace github.com/openziti/agora => /home/michael/Repos/nf/agora`; bump the `require` from `v0.1.0` to satisfy the graph (≥ `v0.1.2`; replace overrides the exact version). Import paths are unchanged, so the replace alone unblocks the build. Recorded as a deferred pre-merge swap to a tagged release.

### I. Tests

- **`agora/subsystem_test.go`** — remove `BootstrapConnects`/loopback/`stubConnectAddress` tests; add create-or-bind tests (Listen-hits-existing vs ErrNotFound→Create→Listen, managed flag), wrong-mode-bind error, `Close` order, publish-mode-is-`http`, and **publish `Name` equals the resolved serve-tunnel name** (assert it tracks `serve.tunnel` when set, not raw `instance_name`), against the reshaped `agoraOps` mock.
- **New `agora/serve_test.go` / `agora/dial_test.go`** — Serve managed/bound paths and wrong-mode error; Serve partial-failure unwind (Create-then-Listen-fails deletes the created tunnel; Listen-then-wrong-mode-fails closes the listener and leaves the bound tunnel intact); Dialer lifecycle (startup `Attach` once per unique name with the same name a no-op the second time; `HTTPClient` returns the cached client and errors on a never-attached name; `HTTPClient` performs no attach; `Close` detaches every attached name exactly once), and a real `ConnectTunnelConflict` surfaced as an error.
- **`agora/client_test.go`** — rewrite for the new `Attach`/`HTTPClient`/`Close` shape (no more loopback-address return).
- **`aggregator/backend_agora_test.go`** — rewrite `resolveAgoraLoopback` test → dial seam.
- **`gateway/backend_test.go`** — remove `TestCollectAgoraConnectTargets`.
- **`agora/identity_test.go`** — remove `tunnel_mode` validation tests.
- **`cmd/mcp-tools/lifecycle_test.go`** — agora target tests are config-level; adjust for the removed `TunnelMode` default, keep coverage.

### J. Docs (specified here; written at implementation per the pipeline)

- **`docs/current/agora.md`** — rewrite the transport description for Listen/Dial, create-or-bind serve, persistent named tunnels; refresh the smoke-test table; remove loopback/`tunnel_mode` references.
- **`docs/future/agora-deferred.md`** — retire the three dissolved deferrals (loopback hardening *dropped*, multiplexing-one-connect *dissolved*, persistent-named-share *delivered*); record the new deferrals (tagged-release swap, path unification, reconnect/resilience, cross-org dial policy).

## Critical files

- `agora/subsystem.go` — shrink Subsystem, reshape `agoraOps`, `WithRuntime:false`, publish mode constant, Close order (Track A)
- `agora/serve.go` — **new**, create-or-bind + managed delete + wrong-mode error (Track B)
- `agora/dial.go` — **new**, startup `Attach` (dedup by tunnel name) + per-session `DialContext` http client, detach-all on `Close` (Track C)
- `agora/config.go`, `agora/identity.go` — drop `tunnel_mode`, add `serve.tunnel` (G)
- `agora/client.go` — dial client returns http client, not loopback addr (F)
- `gateway/backend.go` — bind `Serve().Listener()`, delete loopback+resolver wiring (D)
- `gateway/session.go`, `gateway/session_factory.go` — dial by name, drop resolver (D)
- `aggregator/backend.go` — swap resolver seam for dial seam, keep decoupled (D)
- `bridge/bridge.go` — bind `Serve().Listener()`, delete loopback (E)
- `tools/client.go` — agora branch dials via http client (F)
- `go.mod` — `replace` + require bump (H)

## Verification

- **Unit/build.** `go build ./...` and `go test ./...` with the `replace` in place (discard any built binaries). New/rewritten tests per Track I; old loopback-allocation tests removed.
- **Manual smoke (live controller + Ziti fabric)**, refreshing the table in [../current/agora.md](../current/agora.md):
  - **Ephemeral Agora-only gateway**: `mcp-tools run --agora <tunnel>` lists and calls tools; confirm **no `127.0.0.1` Agora listener** exists in the process.
  - **Persistent named tunnel**: pre-provision via `tunnel.Create`, bind, restart the gateway, reconnect under the same name.
  - **Agora backend**: gateway with `transport.type: agora` against `mcp-bridge --network=agora`; discovery + tool calls route through; two backends on the same tunnel share one attachment.
  - **Dual listener**: zrok token and Agora tunnel both respond.
  - **HTTP mode**: `mcp-tools http --agora <tunnel> --bind 127.0.0.1:8080` (the user-facing local bridge is deliberate, not the removed internal-boundary loopback).

## Out of scope / deferred

- **Swap local `replace` → tagged agora release** (release-coordination follow-up; tracked so the temporary replace can't ship).
- **Unifying `mcp-tools`' two transport paths** (nearly free now, still optional).
- **Reconnect/resilience for long-lived serve** — thin primitives carry no heartbeat; a revoked tunnel surfaces as a `net.Listener`/`net.Conn` error, matching zrok's posture (the MVP). Adjacent to [gateway-wedge-resilience.md](gateway-wedge-resilience.md), not pulled in here.
- **Cross-org dial policy design** — `Dial` resolves cross-org via can-connect auth while `Listen` is same-org; shaping that surface is future product work.
- **Dynamic backend add/remove at runtime** — config is static at startup, so attach dedup tears down at shutdown; runtime churn is out of scope.
