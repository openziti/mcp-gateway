---
title: defaulted publish refuses bridge startup
state: inbox
created: 2026-08-25
tags: [defect]
---

`mcp-bridge` refuses to start on a config that never mentions `advertisement.publish`. `AdvertisementPublish` returns `true` when `Publish` is nil, and `Config.Validate` then treats that default as an operator choice:

```go
if c.AgoraPublishEnabled() && !c.AgoraServeEnabled() {
    return fmt.Errorf("agora.advertisement.publish requires agora.serve.enabled for mcp-bridge")
}
```

So a bridge with `agora.enabled: true` serving over zrok fails at startup, blaming a key the operator never wrote. Only an explicit `publish: true` should be a hard error; a defaulted one should reach the subsystem's notice-and-continue path. `agora/config.go` already carries an explicitness helper that checks `Advertisement.Publish != nil` — `Validate` just doesn't use it.

## why

The rule this repo applies to configuration is that explicit settings fail loudly and defaults do not. This inverts it: the loud failure names a setting that exists only as a default, so the error message sends the operator looking for a line that isn't in their file. It is also the one case where a default can make a bridge unstartable.

## background

The fix is small — swap the effective-value check for the explicitness helper — but it changes startup behavior, so it wants a judgment about whether the current strictness was intended. Worth confirming against the subsystem's `serveWanted`/`publishWanted` handling in `agora/subsystem.go`, which already degrades gracefully when publication is wanted but serving is not.

Surfaced by terminus while reviewing unrelated work in the same tree.
