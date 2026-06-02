# Gateway Resilience Deferred Work

The gateway wedge-resilience implementation lands the in-process `mcp-gateway`
listener watchdog, backend session re-handshake, liveness logging, and version
commands. The follow-on work below remains intentionally future-scoped.

## mcp-bridge Listener Resilience

`mcp-bridge` still serves zrok traffic with the same accept-loop-on-listener
shape that made the gateway vulnerable. The resilient listener and watchdog
mechanism should be adapted in a separate pass, because bridge creates a
per-client subprocess and has different shutdown pressure.

## Structured Probe-Storm Event

The OpenZiti SDK can emit latency-probe timeout storms below the gateway. The
gateway now acts on the observable consequence, established terminator loss, but
it does not intercept and coalesce those SDK logs into a gateway-level
`transport degraded` event.

## Managed-Mode IPC Refinements

The in-process watchdog runs in managed mode, but the IPC wire protocol is still
unchanged. Two refinements remain deferred:

- report self-heal exhaustion through a `StatusReport` handshake so an
  orchestrator can restart knowingly.
- include listener health, such as established terminators and seconds since
  last accept, in IPC heartbeats so the orchestrator's view reflects listener
  wedges directly.

## Supervisor Packaging

Standalone gateways now fail honestly after exhausted listener rebuilds. The
operator-facing docs name the supervisor requirement, but a ready-to-use systemd
unit or restart-loop recipe remains a follow-on packaging/documentation task.
