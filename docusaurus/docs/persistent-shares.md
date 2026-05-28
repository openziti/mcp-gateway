---
title: Use persistent shares
sidebar_label: Persistent shares
---

# Use persistent shares

By default, share tokens are ephemeral — they disappear when the process exits. For production use,
create persistent shares that survive restarts and keep a stable token.

1. Create a persistent share by running this once to reserve a named token:

    ```bash
    zrok2 create share my-gateway
    ```

    Share names must be 3–32 characters, lowercase alphanumeric and hyphens (`[a-z0-9-]`). If you omit
    the name, zrok generates a random token.

2. Reference the token in your config:

    **mcp-gateway** — set `share_token` at the top level:

    ```yaml
    share_token: "my-gateway"

    aggregator:
      name: "my-dev-tools"
      version: "1.0.0"
    ```

    **mcp-bridge** — pass `--share-token`:

    ```bash
    mcp-bridge --share-token my-bridge mcp-filesystem ~/Documents
    ```

3. When you no longer need the share, delete it:

    ```bash
    zrok2 delete share my-gateway
    ```
