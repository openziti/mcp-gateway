---
title: runtime backend add and remove
state: inbox
created: 2026-07-24
tags: [feature]
source: docs/future/agora-deferred.md
---

Backend config is static at startup: the dialer attaches each unique agora tunnel once at startup and detaches everything at shutdown — no per-backend ref-counting. Backends that come and go while the process runs would need ref-counted attachments (and a config/API surface for the churn itself). Revisit if runtime backend churn becomes a requirement.
