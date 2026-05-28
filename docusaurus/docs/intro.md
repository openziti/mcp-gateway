---
title: NetFoundry MCP Gateway overview
sidebar_label: Overview
description: >
  NetFoundry MCP Gateway enables secure, isolated access to Model Context Protocol (MCP) tools across
  distributed systems without exposing public endpoints.
---

# NetFoundry MCP Gateway overview

NetFoundry MCP Gateway enables secure, isolated access to Model Context Protocol (MCP) tools across
distributed systems without exposing public endpoints. The project is open source and can be found at:
[github.com/openziti/mcp-gateway](https://github.com/openziti/mcp-gateway).

## The problem it solves

MCP servers typically run locally via stdio. To access tools on remote machines or share them across
a team, you'd normally need to expose endpoints — creating security risks. NetFoundry MCP Gateway solves this
by running everything over OpenZiti's overlay network, so services never listen on public IPs and
require cryptographic identity to access.

## Components

NetFoundry MCP Gateway consists of three tools that can be used independently or together:

- **`mcp-bridge`**: Takes a local stdio MCP server and exposes it over a zrok private share on the
  overlay network.
- **`mcp-gateway`**: Aggregates multiple backends (stdio, HTTP, or other bridges) into a single
  secure zrok share, with namespacing and tool filtering.
- **`mcp-tools`**: Connects an MCP client to a remote share, bridging it back to stdio or a local
  HTTP endpoint.

## How it works

All traffic between components travels over an OpenZiti overlay network via [zrok](https://zrok.io).
Nothing is ever exposed on a public IP. Only authorized parties with a valid zrok environment can
connect to a share. This model functions transparently through NATs and firewalls.

The gateway creates an isolated session for each connecting client. Each client gets dedicated
backend connections — no shared state, no cross-talk between sessions.

## Quick example

1. Share a local MCP server over the overlay:

    ```bash
    mcp-bridge mcp-filesystem ~/Documents
    ```

    ```text title="Output"
    {"share_token":"a1b2c3d4e5f6"}
    ```

2. Connect to it from anywhere with a zrok-enabled environment:

    ```bash
    mcp-tools run a1b2c3d4e5f6
    ```

