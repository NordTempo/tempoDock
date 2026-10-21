# 0002: Single binary with embedded UI

Status: accepted

## Context

A Node server for the UI would add RAM, a runtime, and another process to
manage. The UI doesn't need server-side rendering: it's an authenticated app
behind a login.

## Decision

- The React UI is a static single-page app, built with Vite and embedded into
  the Go binary with `go:embed`. The daemon serves it alongside the API.
- The same binary is the CLI (`dock deploy`) and the server
  (`dock serve`).

## Consequences

- Install and upgrade are "replace one file, restart one unit".
- The UI costs nothing at idle.
- CLI and server versions ship together; the API is versioned (`/api/v1`) and
  the CLI checks compatibility with the server it talks to.
- The binary is bigger (UI assets add a few MB). Acceptable.
