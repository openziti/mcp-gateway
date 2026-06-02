# Gateway Wedge Resilience — Work Order

## Context

This work order implements [gateway-wedge-resilience.md](gateway-wedge-resilience.md).
It addresses a recurring production wedge in `mcp-gateway` where the process stays
alive but stops accepting client sessions, emits no logs, and never recovers.
User report: `~/Sandbox/mcp-gateway-wedge-2026-05-19.md`.

The work is additive. The in-process listener self-heal runs in **both**
standalone and orchestrator-managed deployments — the wedge is invisible to an
orchestrator too (its IPC heartbeat beats from a separate goroutine while the serve
loop is dead). Only the fail-fast *process exit* is gated to standalone; in managed
mode the watchdog keeps retrying and logs loudly, leaving terminal restart to the
orchestrator. This work does **not** touch the IPC wire protocol (no new messages,
no `gateway.proto` change), the `call_timeout` default, or the aggregator's
discovery-time connection logic. Two IPC-coupled managed refinements
(`StatusReport`-on-escalation, listener-health on the IPC heartbeat) are deferred
to a follow-on.

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
and the `Run()` select never fire. The SDK's `listenerManager.run()` loop
(`ziti.go:2307`) does keep attempting in-place recovery — `makeMoreListeners()`
~every 1s, a session refresh ~every 5–10s when no routers are usable, a 60s
per-child establish deadline — but under a sustained fault that recovery makes no
progress, and nothing in the gateway rebuilds the listener from scratch. A fresh
`sdk.NewListener` is what recovers: it builds a new `listenerManager`, session,
and `acceptC`, sidestepping the wedged state.

`multiListener` (returned by the SDK) implements the exported
`edge.network.MultiListener` interface, which exposes `GetEstablishedCount() uint`,
`GetListenerCount() int`, and `IsClosed() bool` — a reflection-free health
signal. `sdk.NewListener(token, root)` may be called repeatedly on the same
persistent share token, so the listener can be rebuilt in process.

## Architectural decisions (settled)

- **Self-heal in both modes; fail-fast exit standalone-only.** The watchdog
  rebuilds the listener on the same token in process — this runs in standalone and
  managed deployments alike, since rebuilding only re-binds a listener to the
  (orchestrator-owned) share and never touches share ownership or the IPC path.
  After a bounded number of consecutive rebuild failures it logs the unrecoverable
  condition; in standalone mode it then triggers a clean `Run()` exit so a
  supervisor can restart the process, while in managed mode (`Orchestrator != nil`)
  it does **not** exit — it keeps retrying and leaves terminal restart to the
  orchestrator. The gate is `config.Orchestrator == nil`, **not** the `Share.managed`
  flag / `ShareToken`: a persistent `share_token` with no orchestrator (the wedged
  deployment) is standalone and must fail-fast.
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
    mu         sync.Mutex
    inner      net.Listener
    generation uint64       // bumped on each Swap; lets Accept distinguish a swap from a genuine error
    closed     bool         // set by Close() for terminal graceful shutdown (http.Server.Shutdown)
    terminal   error        // set by Fail() when the watchdog gives up; returned by Accept
    lastAccept atomic.Int64 // unix seconds of last successful Accept; seeded at construction
}

func newResilientListener(inner net.Listener) *resilientListener
func (l *resilientListener) Accept() (net.Conn, error)
func (l *resilientListener) Swap(inner net.Listener)        // closes the old inner, installs the new one, bumps generation (NON-terminal)
func (l *resilientListener) Fail(err error)                 // terminal: sets terminal=err, closes inner; next Accept returns err (fail-fast)
func (l *resilientListener) Close() error                   // terminal: graceful shutdown; closes inner, next Accept returns net.ErrClosed
func (l *resilientListener) Addr() net.Addr
func (l *resilientListener) SecondsSinceLastAccept() int64
func (l *resilientListener) Current() (net.Listener, uint64) // current inner listener + its generation, read atomically under the lock
```

`newResilientListener` seeds `lastAccept` with the current time, so before the
first accepted connection `SecondsSinceLastAccept()` reads as time-since-startup
rather than ~57 years from the unix epoch — the first heartbeat on an idle gateway
stays sane.

The wrapper has exactly one wake mechanism — **closing the current inner
listener** is what unblocks a goroutine parked in `inner.Accept()`; the
`generation` counter then tells `Accept` whether that unblock was a swap (retry on
the new inner) or terminal (return). `Close` and `Fail` are the only terminal
operations; `Swap` is never terminal, so the watchdog can rebuild without stopping
`Serve`.

`Accept()` behavior:
- Under the lock: if `closed`, return `net.ErrClosed`; if `terminal != nil`,
  return `terminal`. Otherwise snapshot `inner` and `generation` (as `gen`), then
  unlock.
- Call `inner.Accept()` outside the lock.
- On success: record `lastAccept`, return the conn.
- On error: re-acquire the lock. If `closed`, return `net.ErrClosed`; if
  `terminal != nil`, return `terminal`. Else if `generation != gen`, the inner was
  swapped underneath us — re-read `inner` and retry. **Else the same inner errored
  for a non-swap reason with no terminal state set: return the error**, so
  `http.Server.Serve` exits honestly instead of the wrapper masking a genuine
  fault. (Because `Swap` closes-old and installs-new atomically under the lock, a
  swapped `Accept` always finds the replacement present — no separate wait state is
  needed.)

`Swap(new)`: under the lock, close the old `inner` best-effort (this is what
unblocks a parked `Accept`), set `inner = new`, and bump `generation`. Never sets
`closed`/`terminal`. The watchdog calls `Swap` directly with the rebuilt listener;
there is no separate "close the wedged inner" step.

`Fail(err)`: under the lock, set `terminal = err` and close the current `inner`
(unblocking `Accept`). The next `Accept` returns `err`, driving the fail-fast exit
through `serveHTTP`'s error path.

`Close()`: under the lock, set `closed = true` and close the current `inner`
(unblocking `Accept`); idempotent. The next `Accept` returns `net.ErrClosed`,
which `serveHTTP` already treats as a clean stop. This is the terminal path
`http.Server.Shutdown` ends up on — distinct from the non-terminal `Swap`.

**Acceptance:** unit tests using loopback listeners (`net.Listen("tcp",
"127.0.0.1:0")`): (1) a goroutine is **parked in `Accept` blocked on the old
inner** before `Swap` is called — then `Swap` to a new listener, dial the new
listener, and assert the *parked* `Accept` returns that connection (proving a
mid-swap inner-close is retried, not propagated to `http.Server`); (2) after
`Fail(err)`, `Accept` returns `err`; (3) `Close`
unblocks a parked `Accept`, which returns `net.ErrClosed`; (4) a genuine
`inner.Accept()` error with no swap and no terminal state set propagates out of
`Accept` (the wrapper neither masks it nor spins).

### B. Listener re-establishment

**`gateway/share.go`** — add:

```go
func (s *Share) Relisten() (net.Listener, error)
```

Call `sdk.NewListener(s.token, s.root)` to build a **new** listener and return it
**without closing the current `s.listener`** — `resilientListener.Swap` is the sole
closer of the served inner (see Track A's invariant), so closing here would race
the parked `Accept` and stop `Serve`. Update `s.listener` to the new listener only
**after** `sdk.NewListener` succeeds; on error, return the error and leave the
existing `s.listener` untouched, so a failed rebuild keeps the old listener in
place. Reuses the existing `root`/`token` already held on `Share`. Works in both
standalone and managed `Share` construction since both retain `root` and `token`.

**Acceptance:** `Relisten()` returns a usable `net.Listener` for the same token;
covered indirectly by the watchdog test via a `relisten` callback seam (the unit
test injects a fake to avoid a live fabric).

### C. Listener watchdog

**New file `gateway/watchdog.go`.** A goroutine started in `Backend.Run()` and
cancelled in `Stop()`.

```go
// closeChecker is satisfied by the base edge.Listener, so closed-listener
// detection is essentially always available.
type closeChecker interface {
    IsClosed() bool
}

// establishedCounter is the richer MultiListener capability used for count-based
// wedge detection; its assertion may fail (e.g. after an SDK change), in which
// case the watchdog degrades to closed-only detection rather than going blind.
type establishedCounter interface {
    GetEstablishedCount() uint
}

type watchdog struct {
    resilient    *resilientListener
    relisten     func() (net.Listener, error)                          // wraps Share.Relisten
    healthOf     func(net.Listener) (closeChecker, establishedCounter) // two assertions; either may be nil
    cfg          ResilienceConfig
    failFast     bool // config.Orchestrator == nil: exit on exhausted rebuilds; managed keeps retrying
    // ...
}
```

The two capabilities are deliberately separate because they have different
availability: `IsClosed()` lives on the base `edge.Listener` (essentially always
present), while `GetEstablishedCount()` lives only on the richer `MultiListener`
(the one that can be absent). Bundling them into one assertion would make a missing
count silently disable closed-detection too — the failure `c3` guards against.

Loop every `cfg.PollInterval` (default 10s):
- Resolve health from the current inner listener — read the listener **and its
  generation together** via `resilient.Current()` (one atomic snapshot under the
  wrapper's lock, so the generation always matches the listener being probed — this
  is what the first-seen-zero keying attributes against). Type-assert the listener
  via `healthOf` into a `closeChecker` and an `establishedCounter`, either of which
  may be nil. If `establishedCounter` is nil, log once at warn
  (`established-count health unavailable, count-based rebuild disabled`) and
  continue with closed-listener detection plus the heartbeat. If even
  `closeChecker` is nil (not expected for an `edge.Listener`), log once and rely on
  the heartbeat only.
- Dead if `closeChecker.IsClosed()` is true, or — when `establishedCounter` is
  present — `GetEstablishedCount() == 0` sustained for `cfg.ZeroEstablishedGrace`
  (default 90s, set comfortably past the SDK's own in-place recovery cycle, whose
  longest leg is the 60s per-child establish deadline, so the watchdog rebuilds
  only after that recovery has demonstrably failed rather than racing it at the
  same deadline). Because the check runs on the poll loop, the listener is declared
  dead at `ZeroEstablishedGrace + up to PollInterval` after the count first hits
  zero — so the effective window is ~90–100s at the defaults, already clear of the
  SDK's 60s. Track the first-seen-zero timestamp **keyed to the resilient
  listener's current `generation`**: whenever the observed generation changes (a
  `Swap` happened), discard any prior first-seen-zero before evaluating, so a
  freshly rebuilt listener always starts a **full fresh grace window**. This is
  essential — a brand-new listener legitimately reports zero established terminators
  for a moment at birth while it connects to routers; without the generation key the
  stale timestamp would make the next poll declare the replacement dead immediately
  and churn rebuilds. **On any observed
  healthy state reset both the first-seen-zero timestamp and the
  consecutive-rebuild-failure counter** — "healthy" means `establishedCounter`
  reports `> 0` (or, when only `closeChecker` is available, not closed). This covers
  the case where the SDK heals the *existing* listener in place while our rebuilds
  were failing: the counter must not carry across a recovery, so
  `MaxRebuildFailures` means consecutive failures *within one outage*, not
  cumulative over the process lifetime (which matters especially in managed mode,
  where the gateway never exits and runs indefinitely). When `establishedCounter`
  is absent, only the `IsClosed()` condition can declare the listener dead.
- On dead: log `share listener lost, rebuilding` (with token, last established
  count) → `relisten()` to build the fresh inner → `resilient.Swap(new)` (Swap
  closes the wedged inner, unblocking the parked `Accept` within ~1s, and installs
  the replacement; it is non-terminal, so `Serve` keeps running). On a successful
  swap log `share listener rebuilt` and reset the **first-seen-zero timestamp** so
  the new generation gets a full fresh grace window (it reports zero terminators at
  birth) — but **do not reset the consecutive-failure counter here**: a swap only
  means `sdk.NewListener` returned an object, not that the new generation actually
  established. Build the replacement before swapping, so a failed `relisten()`
  leaves the old inner in place rather than a closed-but-unreplaced gap.
- **Counting recovery attempts (this gates escalation).** The consecutive-failure
  counter counts recovery attempts that did **not** produce a healthy listener. It
  increments on either failure mode: (a) a `relisten()` error — no new listener was
  built (log it, back off e.g. `cfg.PollInterval`, retry next tick); or (b) a
  swapped-in generation that burns its **full fresh grace window still at zero**
  established terminators — the rebuild was syntactically successful but never came
  up. It resets to zero **only on a genuinely healthy observation**
  (`establishedCounter > 0`, or — with only `closeChecker` — not closed); a bare
  swap never resets it. So under a sustained fault where `sdk.NewListener` keeps
  handing back dead-on-arrival listeners, the counter still climbs to the threshold
  and the watchdog cannot loop through fresh grace windows forever.
- On reaching `cfg.MaxRebuildFailures` (default 5) consecutive failed attempts,
  escalate — branching on `failFast`:
  - **Standalone** (`failFast`, i.e. `config.Orchestrator == nil`): log `share
    listener unrecoverable, shutting down for restart` at error level and call
    `resilient.Fail(err)` so `Serve` returns and `Run` exits cleanly for a
    supervisor to restart.
  - **Managed** (`!failFast`): log `share listener unrecoverable, awaiting
    orchestrator restart` at error level and **keep retrying** (do not `Fail`/exit)
    — terminal recovery belongs to the orchestrator that owns the lifecycle. The
    watchdog continues self-healing if the fabric later recovers.
    (The `StatusReport` handshake that would notify the orchestrator explicitly is
    a deferred IPC-coupled refinement; see Context / Out of scope.)

**Acceptance:** unit tests with a fake `establishedCounter` whose count is driven
to 0: (1) detection fires only after the grace window, not on a transient zero; (2)
a successful injected `relisten` results in a `Swap` and a first-seen-zero reset
(fresh grace window), and the consecutive-failure counter resets only once the
generation reports `> 0` established — not on the bare swap; (3) with
`failFast` set, `MaxRebuildFailures` consecutive `relisten` errors call `Fail`, and
with `failFast` unset (managed) the same errors do **not** call `Fail` and the
watchdog keeps retrying; (4) with a nil
`establishedCounter` but a `closeChecker` that reports closed, a rebuild still
triggers, and with a nil `establishedCounter` that is not closed, no count-based
rebuild occurs (closed-only degradation); (5) `relisten` fails a few times below
`MaxRebuildFailures`, then the count recovers above zero (SDK in-place heal), then a
later outage drives the count to zero again — verify the failure counter restarted
from zero so the later outage does not prematurely escalate; (6) after a successful
`relisten`/`Swap` the replacement listener reports zero established terminators
initially — verify the watchdog does **not** immediately declare it dead but waits a
full fresh grace window (the first-seen-zero timer was reset / re-keyed to the new
generation); (7) a sustained fault where every rebuilt generation stays at zero
through its grace window — verify each counts as a failed recovery attempt (the
swap does not reset the counter) so the counter climbs to `MaxRebuildFailures` and
standalone escalates to `Fail` rather than looping through fresh grace windows
forever. No live fabric required — `relisten` and `healthOf` are injected seams.

### D. Liveness + connect logging

- **`serveHTTP` (`gateway/backend.go:392`)** — before pushing to `errCh`, log the
  serve-loop return explicitly (`with` label + error): a listener exit is never
  silent. Keep the existing `errCh` semantics.
- **Heartbeat** — in `Run()`, start a goroutine (interval `cfg.HeartbeatInterval`,
  default 5m, `0` disables, cancelled in `Stop()`) logging `gateway alive` at info
  with
  `active_sessions` (`SessionFactory.ActiveSessionCount()`, already exists at
  `gateway/session_factory.go:74`), `established_terminators` (resolved **fresh each
  emission** from `resilient.Current()`'s listener via the `establishedCounter`
  assertion, when present — never a counter cached from a pre-`Swap` listener, which
  would read stale after a rebuild; the heartbeat ignores the generation component),
  and `seconds_since_last_accept`
  (`resilientListener.SecondsSinceLastAccept()`).
- **Per-session backend connect logging** — in `ClientSession.connectBackend`
  (`gateway/session.go:116`), log each connect lifecycle event at **info** with
  backend `id` and `type` (reserve `debug` for verbose endpoint/target detail), so
  connect and reconnect are visible at the default log level. This also covers the
  Track E reconnect path, making incident-A reconnect churn visible gateway-side
  and keeping the never-silent goal at default verbosity.

**Acceptance:** with a backend that flaps, the gateway log shows the
(re)connects; a long-idle gateway emits periodic `gateway alive` lines; a serve
loop return always produces a log line.

### E. Backend session re-handshake

**`gateway/session.go` `CallTool` (~:319).** Wrap the backend call: when
`backend.session.CallTool` returns a non-nil Go `error` that is *retryable*
(transport/protocol failure, distinct both from a `*mcp.CallToolResult` with
`IsError` and from a timeout/cancellation), attempt one reconnect+retry.

**Retryable-error predicate.** Grounded against
`modelcontextprotocol/go-sdk@v1.1.0`: `ClientSession.CallTool` returns errors in
three buckets (see `mcp/transport.go`'s `call()`):

- a **dead transport** — two sub-cases that surface *different* Go errors,
  confirmed by reading the SDK. A call **issued after** the connection is already
  torn down is wrapped as the exported sentinel `mcp.ErrConnectionClosed`
  (`mcp/transport.go`'s `call()` maps the jsonrpc2 `ErrClientClosing`/
  `ErrServerClosing` shutdown errors to it). But the call that was **in flight at
  the moment the stream dropped** is retired with the *raw reader error* —
  `io.EOF` for a clean SSE drop (`internal/jsonrpc2/conn.go`: `readIncoming` retires
  outstanding calls with the read `err`; `SSEClientTransport` does **not**
  transparently reconnect, so the drop is observable, not a silent hang) — and
  `call()` wraps that as `calling %q: %w` **without** converting it to
  `ErrConnectionClosed`. A broken (non-clean) drop can instead surface
  `io.ErrUnexpectedEOF` or `net.ErrClosed`;
- a **timeout or cancellation** — `context.DeadlineExceeded` / `context.Canceled`,
  joined via the caller `ctx`;
- a **JSON-RPC error response** from the backend (invalid params, unknown tool, a
  backend's pre-initialization rejection, etc.) — surfaced as a wrapped
  `*jsonrpc2.WireError`, which lives in the SDK's `internal/` tree and so cannot be
  imported, type-asserted, or have its `.Code` read by the gateway.

The retryable set is therefore the **dead-transport class**:
`errors.Is(err, mcp.ErrConnectionClosed)` **or** `errors.Is(err, io.EOF)` **or**
`errors.Is(err, io.ErrUnexpectedEOF)` **or** `errors.Is(err, net.ErrClosed)` —
reconnect+retry on any of these. Including `io.EOF` is what makes incident A
self-heal on the *first* failing call (the in-flight-at-drop case); without it the
first failure would fall through non-retryably and only the *second* call would
reconnect. In this code path these errors unambiguously mean the transport reader
died — cleanly distinct from a `*WireError` (protocol response, backend alive) and
from the context errors below.

- Do **not** reconnect on `context.DeadlineExceeded` / `context.Canceled` (use
  `errors.Is`): the `call_timeout` fired on a legitimately long-running tool, or the
  caller cancelled — neither is a dead session, and replaying would silently re-run
  a side-effecting tool (out of scope via `call_timeout`). Return the error and
  **leave the backend session connected**. The timeout is applied via `callCtx`
  derived from the caller `ctx`, so the predicate catches both the per-call deadline
  and an upstream cancellation.
- Do **not** reconnect on a JSON-RPC error response: a backend that *answers* with
  a protocol error is alive, not a dead session. These arrive as opaque
  `WireError`s, so the gateway neither classifies them by code nor (it cannot,
  short of brittle message matching on an internal type) special-cases any of them
  — it returns them as today. In particular there is **no special case** for the
  "before initialization" rejection: it is not a client sentinel in v1.1.0 (the
  server-side check is commented out), and a genuinely interrupted stream —
  incident A's actual trigger — surfaces as `io.EOF` (in-flight) or
  `mcp.ErrConnectionClosed` (next call) and is caught by the dead-transport class
  above regardless.

On a retryable error:

1. Log `backend session error, reconnecting` (session_id, backend, error).
2. Acquire the per-backend guard (only one in-flight reconnect per backend; honor
   `cs.ctx`). Then **pre-connect recheck** — under `cs.mu`, confirm the map entry is
   still the same failed `sessionBackend` (and `!cs.closed`). If another concurrent
   call already replaced it, do **not** reconnect — release the guard and retry the
   call on the current (already-fresh) backend. Only when the entry is still the
   failed one, close that old `sessionBackend` and call `connectBackend` again
   (re-running MCP `initialize` via `mcp.Client.Connect`) — note this does network
   I/O + the handshake **with `cs.mu` released**, so `Close()` or a peer reconnect
   can land during it. Therefore **post-connect recheck** — after `connectBackend`
   returns, re-acquire `cs.mu` and install the new `sessionBackend` **only if
   `!cs.closed` and the map entry is still the failed backend**. If the session
   closed or the entry changed underneath, **close the newly-created backend outside
   the lock and discard it** (do not publish) — otherwise it leaks into a dead or
   superseded session.
3. Retry the call once on the fresh session. If it still fails, return the error
   as today (log `tool call failed`).

**`cs.backends` ownership.** Every access to `cs.backends` is under `cs.mu` (or via
a snapshot taken under it): the `CallTool` read (today unlocked at `:309`), the
reconnect re-check/replacement above, and `Close()`. `Close()` today iterates the
map outside the lock — change it to set `closed` and snapshot-or-clear the map
under `cs.mu`, then close the snapshotted backends outside the lock. The
pre-connect recheck, the **post-connect recheck**, and the locked `Close()`
snapshot together close the races: a second reconnect clobbering a backend a peer
call already replaced, and a session teardown landing *before* or *during* a slow
`connectBackend` (the post-connect check catches the during-connect case and
discards the freshly-built backend rather than leaking it into a closed session).

Keep the existing `call_timeout` context wrapping. A reconnect that fails to
connect returns the original call error — and logs the failed recovery attempt at
warn (`backend reconnect failed`, with backend `id`, `session_id`, the original
call error, and the reconnect error), so an exhausted recovery is never silent in
the log even though the API surfaces the original error.

**Acceptance:** unit test with a stub backend whose session returns
`mcp.ErrConnectionClosed` once then succeeds — the gateway reconnects exactly once
and the retried call succeeds; a stub that always returns `mcp.ErrConnectionClosed`
reconnects at most once and surfaces the error. Cover the whole dead-transport
class: a stub returning `io.EOF` (the in-flight-at-drop case) — and a wrapped
`io.ErrUnexpectedEOF` / `net.ErrClosed` — likewise reconnects. Verify **no**
reconnect when: the
backend returns a normal `IsError` result; the call fails with a non-
`ErrConnectionClosed` Go error standing in for a JSON-RPC/`WireError` protocol
response; or the call fails with `context.DeadlineExceeded` / `context.Canceled`
(in every no-reconnect case the error surfaces and the session stays connected).
Concurrency cases: two calls against the same backend failing transport-side at once
reconnect it **at most once** (the loser retries on the replacement — no leaked or
double-closed session); a `Close()` racing an in-flight reconnect neither
double-closes nor leaks a session; and a `Close()` (or peer replacement) that lands
**while `connectBackend` is in progress** results in the freshly-built backend being
closed and discarded, never published into the closed/superseded session.

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
| `zero_established_grace` | `90s` | sustained-zero window before declaring dead (effective fire time is this `+ up to poll_interval`) |
| `max_rebuild_failures` | `5` | consecutive rebuild failures before escalation (standalone: fail-fast exit; managed: loud log, keep retrying) |
| `heartbeat_interval` | `5m` | `gateway alive` cadence (0 disables) |

When the block is absent, defaults apply (watchdog on). `watchdog_enabled: false`
preserves today's behavior exactly (no wrapper swap logic active; still safe to
keep the resilient wrapper in the serve path as a passthrough). **Validation:** when
the watchdog is enabled, require **positive** `poll_interval`, `zero_established_grace`,
and `max_rebuild_failures` (a zero `poll_interval` is a `time.Ticker` panic, not a
clean error — reject it at config load); `heartbeat_interval: 0` is the one
documented disable sentinel (otherwise positive). Reject negative durations.

**Acceptance:** a config without a `resilience:` block runs with the watchdog
enabled and defaults; `watchdog_enabled: false` disables rebuild/heartbeat.

## Wiring summary (`gateway/backend.go`)

- `Run()`: wrap `b.share.Listener()` in `newResilientListener`, `serveHTTP` that
  wrapper for the zrok server, start the watchdog (bound to the wrapper + a
  `relisten` closure over `b.share.Relisten`) and the heartbeat goroutine. The
  watchdog's **self-heal runs in both modes**; pass it the standalone flag
  (`config.Orchestrator == nil`) so only standalone escalates to `resilient.Fail`
  on exhausted rebuilds (managed keeps retrying — see Track C). The heartbeat (a
  log line, no IPC) also runs in both modes. The agora local listener (if any) is
  unaffected.
- **Shared teardown ordering (all exit paths).** The cancel-then-join ordering must
  hold on *every* path that tears the gateway down, not just `Stop()`. Today `Run()`
  itself shuts down the HTTP servers on its own exits — `ctx.Done()` (signal
  handling) and the `errCh` serve-error path — and the CLI calls `Stop()` only
  *after* `Run()` returns; so without care a Ctrl-C would let `Run` close the
  listener/share while the watchdog is still ticking, racing `relisten()`/`Swap`
  against shutdown. Factor teardown into a **single idempotent helper** (guarded by
  `sync.Once`) that always runs in this order: **(1) cancel and join the watchdog
  and heartbeat goroutines, then (2) shut down the HTTP servers, then (3) close the
  share.** Every exit — `Run`'s `ctx.Done`, `Run`'s `errCh`, and `Stop()` — calls
  that one helper, so the ordering is guaranteed regardless of which path fires and
  the watchdog can never return or install a listener after shutdown has begun.
- `Stop()`: delegates to the shared teardown helper (it does not re-implement the
  sequence).

**Acceptance (teardown wiring):** a focused test for the shared teardown helper
asserting the order **cancel+join watchdog/heartbeat → shut down HTTP servers →
close share** holds, and that it is idempotent (safe to call more than once),
exercised across all three trigger paths — `Run`'s `ctx.Done` (signal),  `Run`'s
`errCh` (serve error), and `Stop()`. A cancel-then-join regression only surfaces in
shutdown races, so it needs its own test rather than relying on the listener/watchdog
unit tests.

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
- `go test ./...` including the new unit tests from Tracks A, C, E and the shared
  teardown-wiring test (cancel-then-join ordering across `ctx.Done`/`errCh`/`Stop`).
- Manual `version` check on all three binaries.
- **Wedge repro (manual, live zrok/ziti):** per report §"Repro pointers" — (a) to
  exercise the Track E reconnect, force a genuine transport error on an in-flight
  call (drop/close the backend connection mid-call, or a protocol error); expected:
  one `backend session error, reconnecting` then a successful retry. Separately, a
  backend that 202-accepts POSTs but never responds must **time out without
  reconnecting** (the `call_timeout`/cancellation path), exercising the other half
  of the retryable predicate. (b) force terminator loss / link failure behind the
  share, to exercise Tracks A–C. Expected: `share listener lost, rebuilding` →
  `share listener rebuilt`, new client sessions accepted without a manual restart,
  heartbeat visible throughout; and under sustained failure, the standalone clean
  fail-fast exit with the unrecoverable log (in managed mode the same unrecoverable
  log without exit, the watchdog still retrying).

## Out of scope

- `mcp-bridge` listener resilience (same pattern; deferred follow-on).
- Probe-storm structured event (intercepting the embedded SDK logger).
- Supervisor/systemd packaging guidance for the fail-fast leg (belongs in the
  docs pass at implementation time).
- **IPC-coupled managed refinements** (deferred follow-on): a `StatusReport`
  handshake when self-heal is exhausted so the orchestrator restarts knowingly, and
  surfacing listener health on the IPC heartbeat so the orchestrator's view stops
  lying. Both touch `gateway.proto` / the IPC wire protocol, which this work leaves
  unchanged. (Note: in-process self-heal in managed mode is **in** scope — only
  these protocol changes are deferred.)
- `call_timeout` default change; IPC wire-protocol / `gateway.proto` changes.
