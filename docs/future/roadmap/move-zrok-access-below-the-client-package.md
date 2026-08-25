---
title: move zrok Access below the client package
state: researching
created: 2026-08-25
tags: [enhancement]
milestone: v0.1.x
log:
  - stamp: 2026-08-25
    note: surfaced by terminus during the mcp-tools session-isolation work — docs/journal/2026-08-25.md
---

`gateway/session.go` and `aggregator/backend.go` both import `tools` for `tools.Access`, the zrok access primitive. Server-side packages reaching up into the client package inverts the layering, and it makes `tools` unable to import `gateway` at all. Move `Access` into a leaf package that `aggregator`, `gateway`, and `tools` can all import, and drop the upward edge.

## why

The inversion is load-bearing rather than cosmetic: it decided the shape of a fix. Sharing the Streamable HTTP session lifecycle with `mcp-tools` required extracting `gateway.StreamableSessions` into the `streamable` package, because the direct reuse — `tools` importing `gateway` — would have closed an import cycle. That extraction is defensible on its own (three consumers, not a gateway concern), but it was chosen against a constraint this inversion creates. The next piece of shared machinery will meet the same wall.

Nothing user-visible depends on it, which is why it was deliberately kept out of the session-isolation card rather than widening that diff across three packages.
