# Gateway Wedge Resilience — Work Order

## Context

This work order implements [gateway-wedge-resilience.md](gateway-wedge-resilience.md).
It addresses a recurring production wedge in `mcp-gateway` where the process stays
alive but stops accepting client sessions, emits no logs, and never recovers.
User report: `~/Sandbox/mcp-gateway-wedge-2026-05-19.md`.

The work is additive and standalone-mode-focused. It does not change the
managed/IPC path, the `call_timeout` default, or the aggregator's discovery-time
connection logic.

### Verified root cause

The accept loop is `http.Server.Serve(b.share.Listener())` —
`gateway/backend.go:281` via `serveHTTP` (`gateway/backend.go:392`) — bound to an
OpenZiti `edge.Listener` returned by `sdk.NewListener` (`gateway/share.go:52,86`).

In `openziti/sdk-golang@v1.5.4`, `baseListener.AcceptEdge()`
(`ziti/edge/network/listener.go:65`) is:

```go
for !listener.closed.Load() {
    select {
    case conn, ok := <-listener.acceptC:
        if ok && conn != nil { return conn, nil }
        listener.closed.Store(true)
    case <-ticker.C: // 1s
    }
}
```

When the fabric channel dies, the per-router `multiListener.forward()` goroutine
exits and removes its child (`delete(self.listeners, child)`), so established
terminators fall to zero — but `acceptC` is never closed and `closed` is never
set. `Accept()` blocks forever with no error, `Serve()` never returns, `errCh`
and the `Run()` select never fire. Nothing rebuilds the listener.

`multiListener` (returned by the SDK) implements the exported
`edge.network.MultiListener` interface, which exposes `GetEstablishedCount() uint`,
`GetListenerCount() int`, and `IsClosed() bool` — a reflection-free health
signal. `sdk.NewListener(token, root)` may be called repeatedly on the same
persistent share token, so the listener can be rebuilt in process.

## Architectural decisions (settled)

- **Self-heal + fail-fast.** The watchdog rebuilds the listener on the same token
  in process; after a bounded number of consecutive rebuild failures it logs and
  triggers a clean `Run()` exit so a supervisor can restart the process.
- **Detection via the SDK's terminator count, not reflection.** Type-assert the
  live inner listener to a minimal local interface and poll
  `GetEstablishedCount()` / `IsClosed()`. No reflection into unexported fields, no
  heavy self-probe traffic. Degrade gracefully if the assertion ever fails.
- **HTTP server outlives the listener.** `http.Server.Serve` runs against a
  swappable wrapper listener so the underlying `edge.Listener` can be replaced
  without restarting the server.
- **Backend re-handshake on transport error only.** Reconnect+retry triggers on
  Go errors from `ClientSession.CallTool`'s backend call (transport/protocol
  failures), not on tool results carrying `IsError` (legitimate tool failures).
- **Scope: mcp-gateway.** `mcp-bridge` shares the wedge-prone pattern; its
  listener resilience is a deferred follow-on. The `version` subcommand lands on
  all three binaries now.

## Tracks

### A. Resilient swappable listener

**New file `gateway/resilient_listener.go`.** A `net.Listener` wrapper that lets
the underlying listener be hot-swapped beneath a running `http.Server.Serve`.

```go
type resilientListener struct {
    mu        sync.Mutex
    inner     net.Listener
    swapped   chan struct{} // closed+replaced on each Swap to wake blocked Accept
    closed    bool
    terminal  error         // set when the watchdog gives up; returned by Accept
    lastAccept atomic.Int64 // unix seconds of last successful Accept
}

func newResilientListener(inner net.Listener) *resilientListener
func (l *resilientListener) Accept() (net.Conn, error)
func (l *resilientListener) Swap(inner net.Listener)        // replaces inner, wakes Accept
func (l *resilientListener) Fail(err error)                 // sets terminal, wakes Accept
func (l *resilientListener) Close() error                   // closes current inner
func (l *resilientListener) Addr() net.Addr
func (l *resilientListener) SecondsSinceLastAccept() int64
```

`Accept()` behavior:
- Read the current `inner` under the lock, then call `inner.Accept()` outside the
  lock.
- On success: record `lastAccept`, return the conn.
- On error: if `terminal` is set, return it (this lets `Serve` exit cleanly for
  the fail-fast path). Otherwise the inner has been closed for a swap — wait on
  the `swapped` signal (and re-read `inner`) and retry. Do **not** propagate a
  transient inner-closed error to `http.Server`, which would stop `Serve`.

`Swap(inner)`: under the lock, replace `inner`, re-arm the `swapped` channel
(close the old one to wake any blocked `Accept`, install a fresh one).

`Fail(err)`: under the lock, set `terminal=err`, close current inner, wake
waiters — the next `Accept` returns `err`.

**Acceptance:** unit tests using loopback listeners (`net.Listen("tcp",
"127.0.0.1:0")`): (1) a conn dialed after a `Swap` to a new listener is accepted
through the wrapper; (2) after `Fail(err)`, `Accept` returns `err`; (3) `Close`
unblocks a parked `Accept`.

### B. Listener re-establishment

**`gateway/share.go`** — add:

```go
func (s *Share) Relisten() (net.Listener, error)
```

Close the old `s.listener` (best-effort; log on error), call
`sdk.NewListener(s.token, s.root)`, store it on `s.listener`, and return it.
Reuses the existing `root`/`token` already held on `Share`. Works in both
standalone and managed `Share` construction since both retain `root` and `token`.

**Acceptance:** `Relisten()` returns a usable `net.Listener` for the same token;
covered indirectly by the watchdog test via a `relisten` callback seam (the unit
test injects a fake to avoid a live fabric).

### C. Listener watchdog

**New file `gateway/watchdog.go`.** A goroutine started in `Backend.Run()` and
cancelled in `Stop()`.

```go
type listenerHealth interface {
    GetEstablishedCount() uint
    IsClosed() bool
}

type watchdog struct {
    resilient    *resilientListener
    relisten     func() (net.Listener, error) // wraps Share.Relisten
    healthOf     func(net.Listener) (listenerHealth, bool)
    cfg          ResilienceConfig
    // ...
}
```

Loop every `cfg.PollInterval` (default 10s):
- Resolve `listenerHealth` from the current inner listener via `healthOf`
  (type assertion). If unavailable, log once at warn (`listener health interface
  unavailable, count-based detection disabled`) and rely on `IsClosed()` + the
  heartbeat only.
- Dead if `IsClosed()` is true, or `GetEstablishedCount() == 0` sustained for
  `cfg.ZeroEstablishedGrace` (default 60s — matches the SDK's own 1-minute
  establish deadline so the watchdog doesn't race the SDK's auto-rebind). Track
  the first-seen-zero timestamp; reset it whenever the count recovers.
- On dead: log `share listener lost, rebuilding` (with token, last established
  count) → `resilient.Close()` the wedged inner (unblocks within ~1s) →
  `relisten()` → `resilient.Swap(new)`. On success log `share listener rebuilt`
  and reset the failure counter.
- On a `relisten()` error: increment the consecutive-failure counter, log it,
  back off (e.g. `cfg.PollInterval`), and retry next tick. After
  `cfg.MaxRebuildFailures` (default 5), log `share listener unrecoverable,
  shutting down for restart` at error level and call `resilient.Fail(err)` so
  `Serve` returns and `Run` exits cleanly.

**Acceptance:** unit tests with a fake `listenerHealth` whose count is driven to
0: (1) detection fires only after the grace window, not on a transient zero; (2)
a successful injected `relisten` results in a `Swap` and counter reset; (3)
`MaxRebuildFailures` consecutive `relisten` errors call `Fail`. No live fabric
required — `relisten` and `healthOf` are injected seams.

### D. Liveness + connect logging

- **`serveHTTP` (`gateway/backend.go:392`)** — before pushing to `errCh`, log the
  serve-loop return explicitly (`with` label + error): a listener exit is never
  silent. Keep the existing `errCh` semantics.
- **Heartbeat** — in `Run()`, start a goroutine (interval `cfg.HeartbeatInterval`,
  default 5m, cancelled in `Stop()`) logging `gateway alive` with
  `active_sessions` (`SessionFactory.ActiveSessionCount()`, already exists at
  `gateway/session_factory.go:74`), `established_terminators` (from the watchdog's
  `listenerHealth`, when available), and `seconds_since_last_accept`
  (`resilientListener.SecondsSinceLastAccept()`).
- **Per-session backend connect logging** — in `ClientSession.connectBackend`
  (`gateway/session.go:116`), log each connect at info/debug with backend `id`,
  `type`, and endpoint/target. This also covers the Track E reconnect path, making
  incident-A reconnect churn visible gateway-side.

**Acceptance:** with a backend that flaps, the gateway log shows the
(re)connects; a long-idle gateway emits periodic `gateway alive` lines; a serve
loop return always produces a log line.

### E. Backend session re-handshake

**`gateway/session.go` `CallTool` (~:319).** Wrap the backend call: when
`backend.session.CallTool` returns a non-nil Go `error` (transport/protocol
failure, distinct from a `*mcp.CallToolResult` with `IsError`), attempt one
reconnect+retry:

1. Log `backend session error, reconnecting` (session_id, backend, error).
2. Under a per-backend guard (only one in-flight reconnect per backend; honor
   `cs.ctx`), close the old `sessionBackend` and call `connectBackend` again —
   this re-runs MCP `initialize` via `mcp.Client.Connect` — and replace the entry
   in `cs.backends`. Guard the `cs.backends` map mutation with `cs.mu` (note:
   today `cs.backends` is read in `CallTool` without a lock at `:309`; the
   reconnect write makes locking necessary — add minimal `cs.mu` coverage around
   the map read+write, keeping the existing `closed` check semantics).
3. Retry the call once on the fresh session. If it still fails, return the error
   as today (log `tool call failed`).

Keep the existing `call_timeout` context wrapping. A reconnect that fails to
connect returns the original call error.

**Acceptance:** unit test with a stub backend whose session fails once then
succeeds — the gateway reconnects exactly once and the retried call succeeds; a
stub that always fails reconnects at most once and surfaces the error. Verify no
reconnect is attempted when the backend returns a normal `IsError` result.

### F. `version` subcommand

Add a `version` command printing `build.String()` (`build/metadata.go`) to all
three binaries; `--version` keeps working.

- **mcp-gateway** (`cmd/mcp-gateway/`) and **mcp-tools** (`cmd/mcp-tools/`) are
  subcommand CLIs — add a `version.go` registering a `version` cobra command in
  `init()`, mirroring `cmd/mcp-gateway/run.go`'s pattern.
- **mcp-bridge** (`cmd/mcp-bridge/main.go`) has `RunE` on the **root** with
  `Args: cobra.MinimumNArgs(1)` (it takes a positional command to bridge). Adding
  a `version` child command makes cobra dispatch `mcp-bridge version` to the child
  rather than the root `run`. Document the one edge: a user could no longer bridge
  a stdio command literally named `version` via the bare positional form
  (vanishingly rare; `--version` and an explicit path still work). Confirm
  `mcp-bridge <other-command>` still runs the root unchanged.

**Acceptance:** `mcp-gateway version`, `mcp-bridge version`, `mcp-tools version`
each print the build string and exit 0; `--version` still works; normal
`mcp-bridge <command>` invocation is unaffected.

### G. Config

**`gateway/config.go`** — add an optional block (e.g. `resilience:`) to `Config`
with on-by-default values applied in the config defaults path:

| Field | Default | Meaning |
|---|---|---|
| `watchdog_enabled` | `true` | master switch for the listener watchdog |
| `poll_interval` | `10s` | health poll cadence |
| `zero_established_grace` | `60s` | sustained-zero window before declaring dead |
| `max_rebuild_failures` | `5` | consecutive rebuild failures before fail-fast exit |
| `heartbeat_interval` | `5m` | `gateway alive` cadence (0 disables) |

When the block is absent, defaults apply (watchdog on). `watchdog_enabled: false`
preserves today's behavior exactly (no wrapper swap logic active; still safe to
keep the resilient wrapper in the serve path as a passthrough). Validate
non-negative durations.

**Acceptance:** a config without a `resilience:` block runs with the watchdog
enabled and defaults; `watchdog_enabled: false` disables rebuild/heartbeat.

## Wiring summary (`gateway/backend.go`)

- `Run()`: wrap `b.share.Listener()` in `newResilientListener`, `serveHTTP` that
  wrapper for the zrok server, start the watchdog (bound to the wrapper + a
  `relisten` closure over `b.share.Relisten`) and the heartbeat goroutine. The
  agora local listener (if any) is unaffected.
- `Stop()`: cancel the watchdog and heartbeat before shutting down the HTTP
  servers and the share.

## Critical files

- `gateway/resilient_listener.go` — **new** (Track A)
- `gateway/watchdog.go` — **new** (Track C)
- `gateway/share.go` — `Relisten()` (Track B)
- `gateway/backend.go` — `Run`/`Stop` wiring, `serveHTTP` log, heartbeat (D, wiring)
- `gateway/session.go` — `CallTool` reconnect+retry, `connectBackend` log (D, E)
- `gateway/config.go` — `resilience:` block + defaults (Track G)
- `cmd/mcp-{gateway,bridge,tools}/version.go` — **new** (Track F)

## Verification

- `go build ./...` (discard binaries; clean up any built to `bin/`).
- `go test ./...` including the new unit tests from Tracks A, C, E.
- Manual `version` check on all three binaries.
- **Wedge repro (manual, live zrok/ziti):** per report §"Repro pointers" — (a) a
  backend that 202-accepts POSTs but never responds, to exercise Track E; (b)
  force terminator loss / link failure behind the share, to exercise Tracks A–C.
  Expected: `share listener lost, rebuilding` → `share listener rebuilt`, new
  client sessions accepted without a manual restart, heartbeat visible throughout;
  and under sustained failure, a clean fail-fast exit with the unrecoverable log.

## Out of scope

- `mcp-bridge` listener resilience (same pattern; deferred follow-on).
- Probe-storm structured event (intercepting the embedded SDK logger).
- Supervisor/systemd packaging guidance for the fail-fast leg (belongs in the
  docs pass at implementation time).
- `call_timeout` default change; managed/IPC path changes.

## Open questions

1. **Heartbeat at info vs debug.** Default `5m` at info keeps the log honest about
   liveness but adds steady volume. If operators find it noisy, demote to debug or
   lengthen the default — decide during review.
2. **Resilient wrapper when `watchdog_enabled: false`.** Recommend keeping the
   wrapper in the serve path as a transparent passthrough (so the only difference
   is whether the watchdog goroutine runs), rather than branching the serve wiring
   on the flag. Confirm this is acceptable.
