---
title: token output shares stdout with logs
state: researching
created: 2026-08-25
tags: [defect]
milestone: v0.1.x
---

`mcp-bridge` and `mcp-gateway` call `dl.Init` without `SetOutput(os.Stderr)`, so structured log records and the share-token object both land on stdout, and the token object is pretty-printed across several lines rather than emitted as one. README and getting-started both present that token as the thing you capture programmatically, but `mcp-bridge ... | jq -r .share_token` does not work against what the binaries actually emit. `mcp-tools` already routes its logs to stderr, so the trifecta is inconsistent with itself.

Send bridge and gateway logs to stderr and emit the token as a single line, keeping stdout the machine-readable channel the docs describe. Found while building the e2e smoke suite, which has to run a streaming JSON decoder over stdout and filter on the `level` field to recover the token.
