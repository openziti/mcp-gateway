---
title: isolate mcp-tools HTTP backend sessions
state: inbox
created: 2026-08-25
tags: [defect]
---

`mcp-tools http` creates one fabric-side MCP client session in `Client.Start`, builds one proxy server whose handlers close over that session, and returns that same server for every local Streamable HTTP session. Multiple local agents therefore share one remote gateway or bridge session instead of receiving isolated backend state. This predates the move from SSE to Streamable HTTP; the transport change did not create it.

Separate discovery and transport ownership from frontend-session ownership. Keep the zrok access or Agora attachment for the lifetime of `mcp-tools`, but create a fresh fabric MCP client session and proxy server for each stateful frontend session. Close that backend session when its frontend session terminates, expires, fails initialization, or the process shuts down. Stateless HTTP mode needs the equivalent per-request ownership. The stdio `mcp-tools run` path remains a single frontend session and does not need fan-out.

Test two simultaneous HTTP clients for isolation, then cover graceful `DELETE`, abandoned-session expiry, failed initialization, and server shutdown. Found as a blocking session-isolation finding in the fourth Terminus review of the Streamable HTTP fabric transport migration and deferred deliberately because it is a pre-existing architectural defect rather than part of the wire-surface flip.
