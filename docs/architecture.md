# Architecture

## Overview

```
 laptop / agent / CI                    server (VPS)
┌──────────────┐               ┌───────────────────────────────────────┐
│   dock CLI   │──HTTPS+token─▶│ dock serve (one Go binary)             │
└──────────────┘               │  ├─ HTTP API (OpenAPI) + SSE           │
┌──────────────┐               │  ├─ embedded web UI                    │
│   browser    │──────────────▶│  ├─ core: deploys, releases, rules     │
└──────────────┘               │  ├─ SQLite (state, audit log)          │
                               │  └─ node runtime                       │
                               │      ├─ Docker SDK ──▶ Docker Engine    │
                               │      ├─ BuildKit (capped container)    │
                               │      └─ Caddy config (socket) ─▶ Caddy  │
                               └───────────────────────────────────────┘
```

- The daemon is the only thing that talks to Docker, BuildKit and Caddy.
- The CLI and UI only talk to the daemon's API.
- Agents use the CLI (or the API directly) with a scoped token.
- This shows a single server. With more servers (production on Hetzner,
  everything else on the homelab), the other servers run only a node runtime.
  See [Servers](#servers).

## Two halves inside one binary

The code is split into a **control plane** (API, core logic, SQLite) and a
**node runtime** (Docker, builds, proxy) behind a Go interface. On the control
plane's own server the interface is a plain function call; for other servers
it's a client that sends the same calls over the node connection.

This keeps the core testable with a fake runtime, makes remote servers a
transport detail, and is where a hosted control plane would split later. See
[ADR 0008](decisions/0008-keep-doors-open.md).

## Data model

```
User            humans; log in to the UI, own tokens
Token           hashed, with scopes and optional project/environment limits
Server          a node: accepts production and/or non-production, disabled
                for placement, total memory, version, builder flag
ServerStat      1-minute (24 h) and 10-minute (7 d) points per server
ServiceStat     the same per service, across releases

Project
 ├─ Placement              per kind (production, non-production): the Server
 │                         its environments of that kind live on
 └─ Environment            production, staging, ...; a kind that never
     │                     changes; placed on its project's Server for that kind
     ├─ Variable           environment-level, shared by all services
     └─ Service            built from source or a prebuilt image
         ├─ Variable       service-level, overrides environment-level
         ├─ Domain         public hostname → service port, with TLS status
         ├─ Volume         named Docker volume + mount path
         └─ Release        immutable snapshot, one per deploy or config change
             └─ Event      build/deploy progress, status changes

AuditEvent      who did what, when, with which token
```

- A **Release** stores the image reference and the fully resolved config
  (variables, ports, limits, mounts, health check). Rolling back starts an old
  release exactly as it was.
- **Variable references** like `${{postgres.DATABASE_URL}}` are resolved when a
  release is created, so every release is self-contained.
- Where settings come from (repo file vs DB) is described in
  [ADR 0007](decisions/0007-config-db-plus-repo-file.md).
- **Staged changes.** Setting variables, changing service settings and adding
  volumes are staged next to the live values, Railway-style. Applying an
  environment's changes is one transaction followed by one `config` release per
  affected running service, including services whose variables reference a
  changed one. Each release records the changes it carried; if it fails they
  are staged again, so live values always match the active release. A source or image deploy of a service takes that
  service's own staged changes with it; environment-level variables wait for
  an explicit apply because they affect every service.

## Deploy flow

A deploy is a state machine persisted in SQLite, so it survives a daemon crash.

```
queued → building → starting → health_checking → active
                         └──────────┴───────────→ failed
```

1. Client uploads source (tarball) or names an image. Daemon takes the
   per-service lock and creates a Release in `queued`.
2. **building:** BuildKit build (or image pull). Build logs stream to clients
   and are kept as a file per release.
3. **starting:** start the new container next to the old one, on the
   environment's network, with resource limits.
4. **health_checking:** HTTP path or TCP port until it passes or times out.
5. **active:** switch the Caddy route, drain and stop the old container.
   **failed:** stop the new container. The old one never stopped serving.
6. Write an AuditEvent, release the lock, publish events to SSE subscribers.

If the daemon restarts mid-deploy, any release not yet `active` is marked
`failed` and its container stopped. The previous release keeps serving.
Stopping the daemon never stops app containers: in-flight deploys are
abandoned and finished by recovery on the next start, which also removes
containers of replaced releases left running. A release keeps its host port
until its container is confirmed removed.

**Stateful services** (any service with a volume, including databases) use a
recreate strategy instead: stop the old container, then start the new one,
because two processes must never share a volume. See
[ADR 0011](decisions/0011-stateful-services-and-databases.md).

## Servers

Details in [ADR 0010](decisions/0010-environments-on-servers.md).

```
 Hetzner (production)                     Homelab (non-production, builder)
 dock serve                    ◀────────  dock node
  ├─ control plane                outbound   ├─ node runtime
  ├─ registry                     conn.      ├─ BuildKit
  └─ node runtime                            └─ staging, dev, previews
 production environments
```

- One control plane, on the production server. Other servers run
  `dock node`, dial out to it, and receive commands over that connection.
- An environment lives entirely on one server: its services, volumes and
  private network. See [Placement](#placement).
- Builds run only on builder servers. Images go to the registry next to the
  control plane; `dock promote` ships a tested image to production without
  rebuilding.
- Hetzner never depends on the homelab. The homelab is flexible (rebooted,
  rebuilt, experimented on), which is why staging and dev live there and why
  production can't rely on it.
- If the homelab is down, production is unaffected. If the control plane is
  down, apps everywhere keep running; only deploys stop.

## Placement

Details in [ADR 0015](decisions/0015-server-pools-and-placement.md).

- Every environment is **production** or **non-production**, decided when
  it's created (`production` by name, or explicitly) and never changed.
- Servers **accept** production, non-production or both, and can be
  **disabled for placement** (nothing new lands there).
- A project keeps all its environments of one kind on **one server**,
  recorded as a placement `(project, kind) → server`. The first environment
  of a kind picks it: among online servers that accept the kind and aren't
  disabled, the most free memory (total memory, reported by the node,
  minus the limits of the services placed there, or the default limit),
  then fewer projects. One aggregate query loads every server's load, so
  placing is O(servers). Admins can **pin** a kind to a server before
  anything is placed. No eligible server: the environment isn't created and
  the error names the servers that accept the kind and why they're out.
- Core reaches a server through `runtimeFor(server)`: the local runtime for
  the control plane's own server, the node connection for others (M3);
  calls to a server that isn't connected fail with `server_offline` (503).

## Networking

- Each (project, environment) gets its own Docker network. Services reach each
  other by service name, like Railway's private networking.
- Services with a domain publish their port on `127.0.0.1` only. Caddy proxies
  to it, so app containers are never directly exposed.
- Public firewall: 22, 80, 443. The daemon API and UI listen on Tailscale by
  default.

## Domains

Users add domains from the CLI or UI; nobody edits proxy config. Details in
[ADR 0009](decisions/0009-dynamic-domains-and-tls.md).

```
CLI / UI ──add domain──▶ API ──▶ Domain row in SQLite
                                     │
                     render full Caddy config from DB
                                     │
                         load into Caddy (atomic swap)

first HTTPS request for a new hostname:
Caddy ──"is this hostname ours?"──▶ daemon ask endpoint ──▶ yes: issue certificate
                                                        └─▶ no: refuse
```

- Any hostname works: apex, subdomains, several per service, and generated
  subdomains under a configured base domain.
- DNS can be set up before or after adding the domain; status updates live.
- Release cutovers use the same render-and-load path.

## Docker as the runtime source of truth

- Every container gets labels: `dock.project`, `dock.env`,
  `dock.service`, `dock.release`, and `dock.control_plane`, the control
  plane's install ID (created once in its database). Networks and volumes
  get it too. A control plane ignores resources labelled for another one
  (no drift, never removed), so two can share a Docker host, though only
  one of them can build there: the `dock-buildkit` container belongs to
  the runtime that created it ([install](guide/install.md)). Resources
  from before the label count as its own only when every other label
  matches its records; new containers get the label on the next deploy.
- Containers run with `restart: unless-stopped`, so they survive daemon and
  host restarts without the daemon's help.
- On start-up the daemon reconciles SQLite (desired) against Docker labels
  (actual), reports drift, and never touches containers it didn't create.

## Live data without idle cost

- An in-process event bus feeds SSE streams to the UI and CLI.
- The daemon subscribes to Docker's event stream instead of polling.
- Resource statistics are the one periodic job. Once a minute the node
  runtime reads `/proc` (host CPU, memory, load, disk of Docker's data
  root, which is what "disk" means in the API and UI, and of the data dir
  when it is on another filesystem) and each active container's cgroup
  v2 files (`cpu.stat`,
  `memory.current` minus inactive file cache, `memory.max`, `io.stat`),
  found at `system.slice/docker-<id>.scope` or `docker/<id>` depending on
  Docker's cgroup driver. No Docker API calls. Core turns counters into
  rates and writes one point per server and service in one transaction;
  each 10-minute boundary averages the last ten points and prunes points
  older than 24 hours (1-minute) and 7 days (10-minute), all as range
  queries on time-first keys. It then publishes the samples on the event
  bus as `usage` events, one per server (operators only) and one
  per environment with its services, so open pages update their numbers
  and append the new point to their charts without polling. Watching a
  server or service live samples every 3 seconds, and only while someone
  watches.
- App logs are read from Docker (`local` log driver with rotation). We don't
  copy or store them.

## Process layout per server

| Process | Managed by | Restarting it affects |
|---|---|---|
| Docker Engine | systemd | Everything (rare) |
| Caddy | systemd, `caddy run --resume` | Traffic, briefly |
| dock daemon (`serve` or `node`) | systemd | Only the API/UI, or that node's link to the control plane |
| BuildKit (builder servers only) | Docker, capped CPU/RAM | Only builds |
| Registry (control plane server only) | Docker | Image pushes and pulls |
| App containers | Docker restart policy | That app |

Upgrading the platform means swapping the binary and restarting the systemd
unit. Apps and routing are unaffected.

## tempoBase, a second binary

The Data panel also ships on its own as `tempobase`
([ADR 0024](decisions/0024-tempobase-standalone.md)): a small server for
people who only want to browse, query and safely change Postgres data.

```
 browser / agent                 tempobase (one Go binary)
┌──────────────┐               ┌────────────────────────────────────┐
│ UI, MCP, API │──────────────▶│ HTTP API, embedded UI, MCP         │
└──────────────┘               │ datapanel (shared with Dock)       │──▶ Postgres
                               │ its own SQLite: users, connections │
                               └────────────────────────────────────┘
```

- The panel's rules (policy, setup, changesets, masking, cursors, audit
  summaries) live once, in `internal/datapanel`, behind a generic `Host`
  interface. Dock's core and tempoBase each implement it over their own
  store, so a fix lands in both.
- tempoBase never links Dock's daemon: no `core`, `api`, `store`, `node`,
  `mcp` or `cli`, no Docker, BuildKit or gRPC. `make tempobase-deps`
  checks it in CI.
- One web project with two entries (`web/` and `web/tempobase/`) sharing
  `features/data`. Dock's UI is style A and tempoBase's style G, on
  system fonts ([design](design/README.md)). One version and one release for both binaries; the
  image is `ghcr.io/nordtempo/tempobase`.

## Code layout

```
cmd/dock/        entry point: CLI commands, `dock serve`, `dock node`
cmd/tempobase/   tempoBase's entry point: `tempobase serve`, `admin`
internal/apitypes/   the API's JSON contract (requests, responses, events, error
                     codes); no server dependencies, so the CLI imports it
internal/api/        HTTP handlers (Huma), auth middleware, SSE, internal
                     loopback endpoints (Caddy `ask`)
internal/core/       domain logic: projects, environments, services, staged
                     changes, variables, releases, deploy state machine, domains,
                     permissions, audit, event bus
internal/store/      SQLite, migrations, sqlc queries
internal/secrets/    AES-GCM encryption of values at rest, key file
internal/node/       runtime interface, local implementation (Docker, BuildKit, Caddy),
                     remote client + node-side server for the node connection
internal/node/stats/ resource statistics from /proc and cgroup files
internal/node/fake/  in-memory runtime for tests and `make dev`
internal/cli/        CLI commands, output formatting (human and --json)
internal/data/       database engines for the Data panel (data/pg: Postgres)
internal/datapanel/  the Data panel's rules behind a Host interface; Dock and tempoBase
internal/tempobase/  tempoBase's server: config, auth, connections, its SQLite store
web/                 React app; build output embedded into the binary
web/tempobase/       tempoBase's app, sharing web/src/features/data
docs/                design docs; docs/guide is Dock's guide, docs/tempobase tempoBase's
```

- `core` depends on the `store` package and the `node.Runtime` interface,
  never on Docker or HTTP directly. Every core operation takes the calling
  actor and checks permissions itself (one code path for users and tokens).
- Organizations ([ADR 0027](decisions/0027-organizations.md)): the actor
  acts in one org, named by the API's `org` parameter (a token's own, or
  a user's only one, when left out), with its role there. Projects, manual
  tokens, the data policy and the audit log are per org; lookups by name
  only find the actor's org's, and live events are filtered by org.
  Operators (`users.operator`, `tokens.operator`) run the platform:
  servers, backups, alerts and user accounts.
- Tests: unit tests for `core` with a fake node runtime; integration tests in CI
  against a real Docker Engine; a small set of UI tests where logic is
  non-trivial.
