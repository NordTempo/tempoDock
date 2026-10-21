# Vision

## Problem

Railway is great to use but expensive at scale, and your apps and data live on
someone else's infrastructure. Existing self-hosted options (Coolify, Dokploy,
Dokku, Kamal) cover parts of this, but none are designed around AI agents
deploying on your behalf with enforced limits.

## Goals

- **One-command deploys** of any Dockerized app to your own VPS.
- **UI and CLI parity.** Everything you can do in one, you can do in the other.
- **Railway's model:** projects, environments, services and variables, with
  private networking between services in the same environment.
- **Agent-safe by design.** Agents can deploy and roll back, but cannot destroy
  apps, volumes or data. This is enforced server-side, not by convention.
- **Lightweight.** The platform must use a small, predictable slice of the VPS.
  If the platform starves the apps, it has failed.
- **Own your data.** Everything lives on your server. No external control plane.

## Non-goals (for now)

- Multiple servers or clustering
- Kubernetes, Nomad, Swarm or any orchestrator layer
- Teams, billing or multi-tenant isolation
- Managed databases in the first versions (plain containers + volumes first)
- Building our own reverse proxy

## Principles

1. **Running apps never depend on the platform being up.** The daemon can crash,
   restart or upgrade while apps keep serving traffic.
2. **Every change is a release.** Code deploys and config changes both produce an
   immutable release that is health-checked before it gets traffic, and can be
   rolled back.
3. **Nothing destructive by default.** Deleting is rare, explicit, and requires
   elevated permissions.
4. **One API, two thin clients.** The CLI and UI contain no business logic.
5. **Boring, small technology.** Prefer one binary and SQLite over more services.
