# 0001: Go for the daemon and CLI

Status: accepted

## Context

The daemon runs on the same server as the apps, so its footprint is taken
directly from them. We also want easy installs, first-class Docker integration,
fast iteration, and code that stays maintainable when much of it is written by
agents.

Memory numbers below are rough estimates for a small idle HTTP service. M0
measures the real ones.

| | Go | Rust | TypeScript (Bun) | TypeScript (Node) |
|---|---|---|---|---|
| Idle RAM (est.) | ~10-25 MB | ~5-15 MB | ~30-60 MB | ~40-80 MB |
| Distribution | Static binary, trivial cross-compile | Static binary, harder cross-compile | `bun build --compile`, large binary | Needs a runtime |
| Docker client | Official SDK | Community (`bollard`) | Community (`dockerode`) | Same |
| Compile speed | Fast | Slow | n/a | n/a |
| Ecosystem | Docker, BuildKit, Caddy, containerd are Go | Good | Good | Good |

## Decision

Go, for both the daemon and the CLI, shipped as one binary.

## Consequences

- One static binary to install, upgrade and distribute for every platform.
- We use the same Docker types the Docker Engine itself uses.
- UI types can't be shared natively with Go. We solve that with a generated
  client ([0005](0005-openapi-api-with-generated-clients.md)).
- The UI stays in TypeScript, so the project has two languages.
