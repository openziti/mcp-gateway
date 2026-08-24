---
title: integration-file environment precedence
state: inbox
created: 2026-08-20
tags: [defect]
source: terminus/6c829eb6de9d
---

`agora.ResolveConfig` expands the main configuration before the integration file fills blanks. An explicitly configured value such as `api_endpoint: "${AGORA_API_ENDPOINT}"` therefore becomes empty when the variable is unset and is overwritten by the integration file, despite the documented rule that main-config values take precedence.

Preserve field presence through the merge: expand only `IntegrationFile` before loading it, merge the file into the otherwise unexpanded main config, then expand the merged configuration once. Keep literal empty fields eligible for integration-file defaults, and add a regression test showing that an unset variable in an explicitly configured field is not replaced while environment references supplied by the integration file still expand.
