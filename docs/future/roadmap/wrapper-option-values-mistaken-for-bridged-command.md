---
title: wrapper option values mistaken for bridged command
state: researching
created: 2026-08-25
tags: [defect]
milestone: v0.1.x
---

`mcp-bridge` infers its Agora capability tag by scanning the wrapper's args for the first token that is neither a flag nor a known subcommand. That token is not always the command: for `mcp-bridge docker run -v /host:/data mcp-server-filesystem`, the scan skips `run` and `-v`, then takes `/host:/data` — the *value* of `-v` — and publishes the tag `data`. Teach the scan to skip values belonging to known value-taking wrapper options before it decides, and cover a wrapper invocation carrying one.

## why

The tag is discovery metadata other parties read to decide what a bridge serves. A wrong tag is worse than a missing one: `data` names nothing anyone can act on, and it is published by default with no signal that inference went astray. `--agora-capability-tag` overrides it, but only for an operator who noticed.

## background

The inference lives in `bridge/bridge.go`, below the `AgoraCapabilityTag` override and the `isWrapperCommand` check. Value-taking options vary per wrapper (`docker -v/-e/-w/--mount`, `npx -p`, and so on), so the fix needs a small per-wrapper table rather than a single global list — or a rule that stops at the first token following the wrapper's subcommand that does not begin with `-` and is not preceded by a value-taking option.

Surfaced by terminus while reviewing unrelated work in the same tree.
