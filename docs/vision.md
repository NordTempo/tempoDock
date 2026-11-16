# Vision

## Problem

You want your apps and their data on servers you choose, and deploying to
them should feel as easy as on a hosted platform: projects, environments,
previews, logs and domains, from a CLI or a web UI. More and more of that
deploying is done by AI agents, so it has to be safe to let them: limits the
server enforces, not instructions they may ignore. And the platform should
take as little of the server as possible, leaving it to the apps.

## Who it's for

1. **Now:** NordTempo. We host our own apps on our own VPSes, deployed by people
   and by agents.
2. **Now:** an open-source project others self-host (AGPL-3.0,
   [ADR 0082](decisions/0082-open-source-license.md)).
3. **Possibly later:** paid hosting, most likely a hosted control plane managing
   servers customers bring.

We build for (1) and (2) without closing the door on (3). See
[ADR 0008](decisions/0008-keep-doors-open.md) for what that means concretely,
and what we deliberately don't build yet.

## Pillars

| Pillar | Means |
|---|---|
| **Lightweight** | The platform uses a small, predictable slice of the server. Idle means idle. |
| **Fast** | Commands, pages and deploys feel instant. Nothing waits on polling. |
| **Maintainable** | Small codebase, boring tech, clear boundaries, focused tests. |
| **Great to use** | CLI and UI are both first-class, with full parity. See [experience](experience.md). |
| **Safe** | Agents can ship all day; the worst outcome of a mistake is a failed release that never got traffic. |

## Goals

- One-command deploys of any Dockerized app to your own server.
- Projects, environments, services and variables, with private networking
  between services in the same environment.
- UI and CLI parity. Everything you can do in one, you can do in the other
  ([ADR 0026](decisions/0026-ui-cli-parity.md)).
- Agent-safe by design, enforced server-side.
- Own your data. No external control plane required.

## Non-goals (for now)

- Clustering: spreading one environment across servers, or automatic failover
  (we do place whole environments on different servers and move them
  without downtime when asked, see
  [ADR 0010](decisions/0010-environments-on-servers.md) and
  [0063](decisions/0063-moves-without-downtime-and-server-drains.md))
- Kubernetes, Nomad, Swarm or any orchestrator layer
- Billing, open signup, or hosting untrusted customer workloads
  (organizations exist, invite-only,
  [ADR 0027](decisions/0027-organizations.md); hosting is its own design)
- Managed databases beyond "a container with a volume and backups"
- Building our own reverse proxy or container runtime

## Principles

1. **Running apps never depend on the platform being up.** The daemon can crash,
   restart or upgrade while apps keep serving traffic.
2. **Every change is a release.** Code deploys and config changes both produce an
   immutable release that is health-checked before it gets traffic and can be
   rolled back.
3. **Nothing destructive by default.** Deleting is rare, explicit, and requires
   elevated permissions.
4. **One API, two thin clients.** The CLI and UI contain no business logic.
5. **Boring, small technology.** One binary and SQLite before more services.
