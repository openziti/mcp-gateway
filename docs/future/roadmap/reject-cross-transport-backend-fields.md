---
title: reject cross-transport backend fields
state: researching
created: 2026-08-25
tags: [defect]
milestone: v0.1.x
---

Only `agora` backends reject transport fields they cannot honor. `validateAgoraTransport` refuses every stdio, zrok, and http field and names the offense; `stdio` and `zrok` have no equivalent, so `share_token`, `agora_tunnel`, `endpoint`, `headers`, and `tls` under a stdio backend, or `command`, `args`, and `endpoint` under a zrok backend, all pass validation and are then ignored at connect time. Extend per-transport validation to reject every populated field the selected transport cannot use, following the agora pattern, and cover `http`/`https` in the same sweep.

## why

An explicit operator setting that is silently discarded is the failure this repo already decided to close for `transport.protocol` on stdio backends. That fix landed one field; the same hole is five fields wide on stdio and wider on zrok. The config says one thing, the backend does another, and nothing complains.

Surfaced by terminus while reviewing unrelated work in the same tree, so it was carded rather than folded into a session-isolation change.
