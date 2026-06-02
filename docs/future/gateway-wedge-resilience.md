# Gateway Wedge Resilience — Spec

## Problem

`mcp-gateway` can enter a state where the process is alive, the zrok share is
still bound, and yet it has gone completely deaf: no log output, no new client
sessions accepted, no recovery on its own. The only fix is `pkill` and relaunch.
A user running a standalone gateway (persistent `share_token`, a single
`type: http` SSE backend) hit this twice in 24 hours, on two different builds.

The operational signature is the worst kind. A process that crashes is honest:
it disappears, a supervisor notices, something restarts. A process that wedges
*lies* — `pgrep` finds it, the share token still resolves, monitoring that only
checks "is the PID alive" stays green. Service is down and every cheap health
signal says it is up. The strongest diagnostic the user had was the absence of
diagnostics: a process that was actively doing IO emitted zero further log lines
for as long as it was left running.

The mechanism, at altitude: the gateway serves by running an HTTP server's accept
loop directly on the OpenZiti `edge.Listener` backing its zrok share. When the
underlying fabric channel for that share dies — a link failure, a terminator
loss, the latency-probe-timeout storm seen in one incident — the listener's
established terminators fall away, but its accept call does not return an error
and does not unblock. It simply waits forever for a connection that can no longer
arrive. The accept loop is parked on a dead listener. The SDK beneath it keeps
trying to re-establish terminators in place, but under a sustained fabric fault
that recovery makes no progress — and nothing in the gateway is watching for the
condition or able to rebuild the listener from scratch. The serving path has
failed in a way the serving path itself cannot see.

Two narrower failures travel alongside the wedge and deserve fixing in the same
pass, because they share its root disease — *trusting a connection that has gone
bad without ever checking*:

- **Backend sessions rot instead of re-handshaking.** Each client session holds
  its own connection to the backend MCP server. When a tool call against that
  connection fails, the gateway logs the error and moves on, keeping the dead
  session. On the next reconnect of the underlying SSE stream the gateway resumes
  POSTing JSON-RPC without re-sending the MCP `initialize` handshake; the backend
  rejects every message ("received request before initialization was complete")
  but answers each POST with `202 Accepted`, so the gateway has no in-band signal
  that it is talking into a void.

- **Degradation is invisible.** Backend reconnects are logged by the backend but
  never by the gateway. The fabric's own probe-timeout errors arrive as
  unstructured noise from a layer below the gateway. There is no heartbeat, so
  silence is indistinguishable from idleness, and no log when the accept loop's
  serve call returns. When the next incident happens, the log file again makes it
  impossible to tell what failed.

## Goals and Non-Goals

**Goals.**

- The serving path survives fabric faults. When the listener behind the share
  dies, the gateway detects it and re-establishes serving on the same persistent
  share token, in process, without operator intervention. This in-process
  self-heal applies in **both** standalone and orchestrator-managed deployments —
  the wedge is invisible to an orchestrator too (its IPC heartbeat keeps beating
  from a separate goroutine while the serve loop is dead), so the gateway healing
  itself is the recovery either way.
- When self-healing genuinely cannot succeed, the gateway fails *honestly* rather
  than lingering as a green-looking corpse. In standalone mode it logs the
  condition clearly and exits, so a supervisor can restart it. In managed mode it
  logs the same condition loudly and keeps retrying, leaving terminal recovery to
  the orchestrator that owns its lifecycle — the gateway never silently exits out
  from under its supervisor.
- Backend sessions re-handshake rather than rot: a connection that has gone bad
  is torn down and rebuilt (which re-runs `initialize`) instead of reused.
- The process is never silent. Degradation always produces a structured log line,
  and a periodic heartbeat makes liveness affirmative rather than inferred from
  the absence of errors.

**Non-goals.**

- Changing the `call_timeout` default. The user's long-running analytical queries
  exceeding the 60s default is a configuration fit, not a gateway defect; it is
  resolved with the existing `aggregator.connection.call_timeout` knob and is not
  in scope here.
- Surgery on the embedded SDK's logging. The fabric's latency-probe errors are a
  symptom emitted a layer below the gateway. Rather than intercept and reformat
  that log stream, the gateway acts on the *consequence* it can observe directly —
  the loss of established terminators.
- Changes to the IPC protocol itself. The in-process self-heal runs in managed
  mode too, but it does so without touching the orchestrator wire protocol: no new
  IPC messages, no `gateway.proto` change. The two IPC-coupled refinements — a
  `StatusReport` handshake when self-heal is exhausted, and surfacing listener
  health on the IPC heartbeat so the orchestrator's own view stops lying — are
  deferred (see Deferred). Both incidents occurred in standalone mode; the
  orchestrator lifecycle contract is unchanged here.

## Model

The fix is three independent reliability properties, deliberately decoupled so
each can hold even if another is degraded.

**1. The serving listener survives fabric faults.** The gateway stops treating
its share listener as a permanent fixture. The HTTP accept loop runs against an
indirection that can have its underlying listener swapped beneath it, so the
listener can be rebuilt without ever tearing down the HTTP server. A watchdog
observes the listener's health through the signal the fabric SDK already exposes —
the count of established terminators — and treats a sustained drop to zero (while
the accept loop is parked) as the wedge. On detecting it, the watchdog rebuilds
the listener on the same persistent token and swaps it in. Self-heal is the
primary path and runs in both standalone and managed deployments; a bounded number
of consecutive rebuild failures escalates honestly — in standalone mode to a clean
exit a supervisor can restart, in managed mode to a loud log while the watchdog
keeps retrying and leaves terminal recovery to the orchestrator. Recovery is
in-process and seamless when it can be; loud and never silent when it can't.

**2. Backend sessions re-handshake rather than rot.** A backend connection is
provisional, not permanent. When a tool call fails because the backend
connection itself is dead — a closed or broken transport stream — the gateway
discards that backend session and reconnects, which necessarily re-runs the MCP
`initialize` handshake, then retries the call once. This is deliberately the
narrowest trigger: a tool that returns an error result, a backend that *answers*
with a protocol error (the session is alive), and a call that simply exhausted its
time budget are all left alone — surfaced as-is with the connection intact — so a
legitimately slow tool is never silently replayed and a live-but-complaining
backend is never needlessly churned. The gateway never resumes
talking to a session whose initialization state it cannot vouch for.

**3. The process is never silent.** Every transition that matters becomes a
structured log line: the serve loop returning, a backend (re)connecting, the
listener being lost and rebuilt, the watchdog giving up. A periodic heartbeat
reports active sessions, established terminators, and time since the last accepted
connection — so an operator (or a log-scraping alert) can tell a healthy idle
gateway from a wedged one, which today they cannot.

These are independent on purpose. A gateway whose backend sessions are healthy
can still suffer a fabric fault; a gateway that cannot rebuild its listener should
still be loudly diagnosable on the way down. Coupling them would let one
degradation mask another — exactly the failure mode this work exists to end.

## Scenarios

**Incident A — the poisoned backend session.** A client's backend SSE connection
is interrupted; on the next tool call the session is in a bad state and the call
fails at the transport level. *Before:* the gateway logs the failure, keeps the
dead session, and on subsequent activity POSTs into a never-initialized backend
session forever, each POST `202`-accepted and silently rejected. *After:* the
first transport-level failure triggers a teardown and reconnect of that backend
session — re-running `initialize` — and the call is retried once on the fresh,
properly handshaked session. The gateway logs the reconnect, so the churn is
visible from the gateway side, not only from the backend's warnings.

**Incident B — terminator loss / probe storm.** The fabric links behind the share
degrade; the SDK emits a burst of latency-probe timeouts and the share's
terminators fall away. *Before:* the accept loop parks forever on a listener that
will never yield another connection; the process goes silent and serves no new
clients until killed. *After:* the watchdog observes established terminators drop
to zero and stay there past the grace window, logs `share listener lost`,
rebuilds the listener on the same token, swaps it under the still-running HTTP
server, and logs `share listener rebuilt`. New client sessions are accepted again
with no manual restart. Throughout, the heartbeat continues to report state, so
even the brief outage is legible in the log. If the fabric is so broken that
rebuilds keep failing, the watchdog escalates after a bounded number of attempts:
in standalone mode it logs an unrecoverable-shutdown line and exits cleanly,
handing recovery to a supervisor instead of lingering; in managed mode it logs the
same line and keeps retrying, leaving the hard restart to the orchestrator rather
than exiting out from under it.

## Deferred (and Why)

- **`mcp-bridge` listener resilience.** `mcp-bridge` serves over zrok with the
  same accept-loop-on-`edge.Listener` pattern and is vulnerable to the identical
  wedge. The resilient-listener and watchdog mechanism is built to be reusable
  there, but applying it is deferred to a follow-on: both incidents were on the
  gateway, and bridge's per-client-subprocess model warrants its own pass rather
  than a rushed graft. The shared `version` subcommand does land on all three
  binaries now.

- **Probe-storm as a first-class structured event.** Turning the fabric's
  latency-probe timeouts into a single structured "transport degraded" event
  would require intercepting the embedded SDK's logger. It is deferred because the
  watchdog already acts on the observable consequence (terminator loss); the
  structured probe event would be additional diagnostic polish, not a behavior
  the recovery path depends on.

- **IPC-coupled managed refinements.** In-process self-heal runs in managed mode
  now, but two refinements that would make the orchestrator a first-class partner
  in recovery are deferred, because both touch the IPC wire protocol the rest of
  this work leaves alone. First, a `StatusReport` handshake when self-heal is
  exhausted, so the orchestrator restarts the gateway *knowingly* rather than
  inferring a crash. Second, surfacing listener health (established terminators,
  seconds-since-accept) on the IPC heartbeat, so the orchestrator's own view stops
  lying during a wedge the way cheap monitoring does today. Until then, managed
  mode self-heals the common case and logs loudly on the rare unrecoverable one,
  leaving terminal restart to the orchestrator's existing lifecycle control.

- **Supervisor guidance for the fail-fast leg.** The honest-exit fallback assumes
  something restarts the process. The user currently runs under `nohup … &
  disown` with no supervisor. Documenting a systemd unit or restart-loop wrapper
  to pair with the fail-fast behavior is deferred to the docs pass that
  accompanies implementation, kept out of this spec so the recovery design isn't
  entangled with deployment packaging.
