---
title: agora serve resilience
state: inbox
created: 2026-07-24
tags: [enhancement]
source: docs/future/agora-deferred.md
---

The thin Listen/Dial primitives carry no heartbeat, retry, or managed status: a revoked tunnel surfaces as a `net.Listener`/`net.Conn` error, exactly as zrok's listener does — matching zrok's posture is the MVP, and it holds today. Any active-healing layer for long-lived agora serve is a separate concern, adjacent to the gateway-wedge-resilience spec (docs/future/gateway-wedge-resilience.md); if that work lands, weigh whether its watchdog/rebuild mechanism extends to the agora listener rather than growing an agora-only variant.
