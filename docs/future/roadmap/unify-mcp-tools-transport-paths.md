---
title: unify mcp-tools transport paths
state: inbox
created: 2026-07-24
tags: [enhancement]
source: docs/future/agora-deferred.md
---

`mcp-tools` keeps its zrok and agora dial paths parallel and uncombined. The Listen/Dial migration made them structurally symmetric (`Serve`↔`Share`, `Dialer`↔`Access`), so merging is now nearly free — but the parallel design keeps existing zrok invocations (and Claude Desktop configs) unchanged. Unify only when a concrete use case needs one client to span both fabrics.
