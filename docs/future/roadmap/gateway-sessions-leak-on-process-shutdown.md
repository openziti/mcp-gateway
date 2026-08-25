---
title: gateway sessions leak on process shutdown
state: researching
created: 2026-08-25
tags: [defect]
milestone: v0.1.x
log:
  - stamp: 2026-08-25
    note: found by terminus alongside the mcp-tools session-isolation work — docs/journal/2026-08-25.md
---

On SIGINT or SIGTERM, `mcp-gateway` cannot terminate the remote MCP sessions its clients own, so every fabric-backed backend it was talking to holds that client's dedicated resources until its own idle timer expires. Two causes, both needing the treatment `tools/client.go` already received:

1. `gateway/backend.go` passes `b.mainCtx` — the signal-driven process context — to `SessionFactory.CreateSession`, and `gateway/session.go` derives every backend session from it. A Streamable HTTP session builds its terminating `DELETE` on the context it was connected with, so by the time cleanup runs that context is already cancelled and the DELETE never leaves.
2. `Backend.Stop` releases the Agora subsystem — detaching every dial tunnel — in the same teardown that closes the session factory, so the attachment can disappear before the sessions that need it.

Connect backend sessions on a context detached from the process context, cancelled only by their own `Close`; bound `Stop`'s wait for in-flight session creation rather than the handshake; and close the session factory before releasing any transport those sessions ride on.

## why

This is the gateway half of a defect already fixed in `mcp-tools`, and it hides the same way: the failure is invisible from the gateway, because the leaked state lives on the far side. The graceful client-disconnect path was fixed on 2026-08-25 by closing backends before cancelling the session context; that repair does nothing for process shutdown, where the context arrives already cancelled.

## background

The `tools` work is the worked example, and the journal entry for 2026-08-25 records the reasoning in full: why the SDK's single context for handshake and connection lifetime forces a choice between an interruptible handshake and a guaranteed-live cleanup context, why `context.AfterFunc` over the handshake reintroduces the bug, and why the bound belongs on the shutdown wait instead.

Testing needs the same shape as `TestGatewayReleasesChainedBridgeOnClientDisconnect` in `e2e/lifecycle_test.go` — a gateway pointed at an `mcp-bridge` over the fabric, watching the *bridge's* child count. Stdio backends cannot exercise this path at all, since cancelling is how a subprocess dies.
