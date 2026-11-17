# Architecture

## Overview

```
 laptop / agent / CI                    server (VPS)
┌──────────────┐               ┌───────────────────────────────────────┐
│ dock CLI,    │──HTTPS+token─▶│ dock serve (one Go binary)             │
│ MCP client   │               │  ├─ HTTP API (OpenAPI) + SSE, /mcp     │
└──────────────┘               │  ├─ embedded web UI and console        │
┌──────────────┐               │  ├─ core: deploys, releases, rules     │
│   browser    │──────────────▶│  ├─ SQLite (state, audit log)          │
└──────────────┘               │  └─ node runtime                       │
                               │      ├─ Docker SDK ──▶ Docker Engine    │
                               │      ├─ BuildKit (capped container)    │
                               │      └─ Caddy config (socket) ─▶ Caddy  │
                               └───────────────────────────────────────┘
```

- The daemon is the only thing that talks to Docker, BuildKit and Caddy.
- The CLI, the UI and the MCP endpoint only talk to core through the API.
- Agents use the CLI, the API or MCP ([ADR 0017](decisions/0017-mcp-endpoint.md))
  with a scoped token.
- This shows a single server. With more servers, the others run only a
  node runtime (`dock node`). See [Servers](#servers).

## Two halves inside one binary

The code is split into a **control plane** (API, core logic, SQLite) and a
**node runtime** (Docker, builds, proxy) behind the `node.Runtime`
interface. On the control plane's own server the interface is a plain
function call; for other servers it's a client that sends the same calls
over the node link.

This keeps the core testable with a fake runtime (`internal/node/fake`,
also behind `make dev`), makes remote servers a transport detail, and is
where a hosted control plane would split later. See
[ADR 0008](decisions/0008-keep-doors-open.md).

## Tenancy: organizations and operators

[ADR 0027](decisions/0027-organizations.md), [0062](decisions/0062-org-alerts-and-operators.md),
[0065](decisions/0065-sign-in-is-the-platforms.md).

- **Organizations** own projects, API tokens, org variables
  (`${{org.KEY}}`), the data access policy, alert channels, Cloudflare
  connections, registry credentials, GitHub installations and the audit
  log. A user is a member of one or more with a role (`read`, `deploy`,
  `admin`); an API token belongs to one org.
- Every core call acts in one org, named by the API's `org` parameter (a
  token's own, or a user's only one, when left out). Lookups by name only
  find that org's, and live events are filtered by org (`canSee`).
- **Operators** (`users.operator`, `tokens.operator`) run the platform:
  servers, placement, the registry, storage connections, backups,
  updates, the public address, sign-in (passwords, SSO providers, the
  two-factor policy), accounts and platform alerts. They administer every
  org without being a member; such actions go into both audit logs.
- The **platform console** (`/console` in the UI) is the operators' area
  ([ADR 0025](decisions/0025-platform-console.md)). Accounts and sign-in
  are the platform's, not an org's.

## Data model

```
User            accounts: password, passkeys/TOTP, SSO identities; operator flag
Org             members (user + role), invites, org variables, settings
Token           hashed, with scope, org, optional project/environment limits
Server          a node: accepts production and/or non-production, disabled
                for placement, total memory, version, builder flag, ingress
ServerStat      1-minute (24 h) and 10-minute (7 d) points per server
ServiceStat     the same per service, across releases

Project         belongs to an org
 ├─ Placement              per kind (production, non-production): the Server
 │                         its environments of that kind live on
 └─ Environment            production, staging, pr-12, ...; a kind that never
     │                     changes; lock, log drains, previews settings
     ├─ Variable           shared (environment-level), used by every service
     └─ Service            built from source or a prebuilt image
         ├─ Variable       service-level, overrides shared
         ├─ Domain         hostname → service port, TLS and verification status
         ├─ Volume         named Docker volume + mount path; snapshots, backups
         ├─ Job, Run       scheduled jobs and one-off commands
         ├─ UptimeCheck    probed from another server
         └─ Release        immutable snapshot, one per deploy or config change
             └─ Event      build/deploy progress, status changes

AuditEvent      who did what, when, with which token (per org, and the platform's)
```

Around this sit alerts, error issues, access requests, analytics sites,
GitHub and Cloudflare connections and backup runs; their shapes are in
`internal/store/migrations` and the ADRs.

- A **Release** stores the image reference and the fully resolved config
  (variables, ports, limits, mounts, health check). Rolling back starts an old
  release exactly as it was.
- **Variable references** like `${{postgres.DATABASE_URL}}` and
  `${{org.KEY}}` are resolved when a release is created, so every release
  is self-contained. Values are sealed with AES-GCM before they reach
  SQLite, bound to their row and column ([ADR 0041](decisions/0041-sealed-values-bound-to-their-place.md)).
- Where settings come from (repo file vs DB) is described in
  [ADR 0007](decisions/0007-config-db-plus-repo-file.md) and
  [dock.toml](dock-toml.md). A whole project's config can be exported and
  applied as a [project file](project-file.md).
- **Staged changes.** Setting variables, changing service settings and adding
  volumes or domains are staged next to the live values.
  Applying an environment's changes is one transaction that also queues
  one `config` release per affected running service, including services
  whose variables reference a changed one; it is refused while the server
  is offline, and a service already deploying keeps its own changes
  staged. Each release records the changes it carried; if it fails (or
  the daemon stops before it finishes) they are staged again, so live
  values always match the active release. A source or image deploy of a
  service takes that service's own staged changes with it (redeploying
  the services that reference a changed variable, as an apply does), and
  a failed one puts the image setting back; shared variables wait for an
  explicit apply because they affect every service.
- **Locked environments** ([ADR 0044](decisions/0044-environment-lock.md)):
  tokens can't change anything in them; what they try becomes an access
  request a person approves. The check is core's `checkEnv`. See the
  [safety model](safety-model.md#locked-environments).

## Deploy flow

A deploy is a state machine persisted in SQLite, so it survives a daemon crash.

```
queued → building → starting → health_checking → active → superseded
                         └──────────┴───────────→ failed
```

1. Client uploads source (tarball), names an image, or a push arrives from
   GitHub. Core takes the per-service lock and creates a Release in
   `queued`.
2. **building:** BuildKit build on a builder server (Dockerfile or
   Railpack), or an image pull. Build logs stream to clients and are kept
   as a file per release.
3. **starting:** the pre-deploy command, if any, runs in a one-off
   container of the new image ([ADR 0045](decisions/0045-pre-deploy-command.md)),
   after a snapshot when asked. Then the new container starts next to the
   old one, on the environment's network, with resource limits.
4. **health_checking:** HTTP path or TCP port until it passes or times out.
5. **active:** switch the Caddy route (the service's upstream file, so
   Caddy doesn't reload, [ADR 0091](decisions/0091-switches-without-reloads.md)),
   drain the old container for 12 s and stop it (the old release becomes `superseded`). From here Docker
   runs `dock-probe` inside the container as the ongoing health check;
   core restarts the service when it keeps failing
   ([ADR 0043](decisions/0043-ongoing-health-checks.md)).
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

Other ways a release starts: rollback, restart, `service start`, a config
apply, `dock promote` (an image by digest, no rebuild) and preview
environments, which clone a base environment per GitHub pull request and
deploy its commits ([ADR 0056](decisions/0056-preview-environments.md)).
GitHub pushes arrive through Dock's GitHub App, one App shared by every
org with installations assigned to orgs
([ADR 0031](decisions/0031-github-app.md), [0066](decisions/0066-github-apps-for-many-orgs.md)),
or through a per-service webhook ([ADR 0018](decisions/0018-git-push-deploy.md)).
GitHub delivers to `/hooks/github/` on `[hooks] public_host` or Dock's
public address; Caddy routes only that path to a loopback listener.

## Servers

Details in [ADR 0010](decisions/0010-environments-on-servers.md) and the
node protocol, [ADR 0014](decisions/0014-node-protocol.md).

```
 control plane server                      other servers
 dock serve                    ◀────────  dock node
  ├─ control plane                outbound   ├─ node runtime
  ├─ registry (127.0.0.1:5000)    link       ├─ registry proxy (127.0.0.1:5000)
  └─ node runtime                            └─ BuildKit on builders
```

- One control plane. Other servers run `dock node`, dial out to it and
  receive calls over that connection: TLS 1.3 with pinned keys, then
  HTTP/2 with the roles reversed (the node is the HTTP server, so it never
  listens). Keepalive pings after 15 s of silence; reconnects back off 1 s
  to 30 s. Nodes join with a one-time token from `dock server add`.
- An environment lives entirely on one server: its services, volumes and
  private network. See [Placement](#placement).
- Builds run only on builder servers. Images go to the registry next to
  the control plane (CNCF Distribution in a container, on local disk or a
  storage connection's bucket, [ADR 0034](decisions/0034-registry-object-storage.md),
  reached through the daemon's storage gateway, which takes pushes onto
  local disk first, [ADR 0084](decisions/0084-registry-storage-gateway.md));
  nodes push and pull through a proxy on their own loopback that tunnels
  over the link, so image references are the same everywhere.
  `dock promote` ships a tested image to production without rebuilding.
- Running apps never depend on the control plane: if it is down, apps
  everywhere keep running; deploys stop. NordTempo runs production on a
  Hetzner VPS and non-production on a homelab, which production never
  depends on.
- Environments move between servers with their volumes, and a server can
  be drained, without downtime: volumes copy in passes while services
  serve, then requests are held for a few seconds for the switch; the old
  server forwards the service's port for a while
  ([ADR 0053](decisions/0053-moving-environments.md), [0063](decisions/0063-moves-without-downtime-and-server-drains.md)).

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
  placing is O(servers). Operators can **pin** a kind to a server before
  anything is placed. No eligible server: the environment isn't created and
  the error names the servers that accept the kind and why they're out.
- Core reaches a server through `runtimeFor(server)`: the local runtime for
  the control plane's own server, the node link for others; calls to a
  server that isn't connected fail with `server_offline` (503).

## Networking

- Each (project, environment) gets its own Docker network, with addresses
  from a dedicated pool. Services reach each other by service name.
- Services with a domain publish their port on `127.0.0.1` only. Caddy proxies
  to it, so app containers are never directly exposed. The address is in a
  file per service (`/var/lib/dock-upstreams`) that Caddy reads per
  request, so a switch changes the file, not Caddy's config.
- A root-installed firewall (`dock-firewall.service`) keeps apps and builds
  off private networks and the host ([safety model](safety-model.md#network-isolation)).
- Public firewall: 22, 80, 443. The daemon's API and UI listen on a private
  address (Tailscale or loopback). An operator can also give Dock a public
  address: Caddy serves it over HTTPS and proxies it to a loopback listener
  ([ADR 0029](decisions/0029-public-address.md)).
- A server's ingress is `direct` (a public IP) or `tunnel`: cloudflared
  carries public traffic from Cloudflare to Caddy, either as
  `dock-cloudflared.service` with a tunnel made in Cloudflare's dashboard
  ([ADR 0013](decisions/0013-homelab-ingress-cloudflare-tunnel.md)) or as
  a `dock-cloudflared` container whose tunnel the control plane creates or
  adopts through a Cloudflare connection ([ADR 0036](decisions/0036-cloudflare-api.md)).

## Domains

Users add domains from the CLI or UI; nobody edits proxy config. Details in
[ADR 0009](decisions/0009-dynamic-domains-and-tls.md) and
[0042](decisions/0042-domain-verification.md).

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
  subdomains under a server's base domain.
- A custom hostname routes only after a TXT record proves the org controls
  it; generated hostnames, Cloudflare ones and operators' need none.
- Domains from a connected Cloudflare zone get their proxied CNAME and
  tunnel route made by Dock; Cloudflare connections can also hold
  redirects at Cloudflare's edge ([ADR 0068](decisions/0068-redirects-on-cloudflare-zones.md)).
- DNS can be set up before or after adding the domain; status updates live.
- Release cutovers use the same render-and-load path.
- A service's domains can require a password or a Dock login
  ([ADR 0030](decisions/0030-access-protection.md)).
- Requests to service domains are counted on each server from Caddy's
  access log and collected with the minute statistics: requests, errors,
  time taken and the most requested paths
  ([ADR 0033](decisions/0033-traffic-statistics.md), [0040](decisions/0040-traffic-counts-requests.md)).

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

The rules and every timer are in [performance](performance.md).

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
  watches. Alerts, retention, traffic, error issues and health restarts
  ride on the same tick.
- Uptime checks ([ADR 0055](decisions/0055-uptime-checks.md)) have their
  own scheduler: one goroutine with a heap of checks and one timer to the
  next due one, armed only while checks exist. Another server probes over
  the node link (`probe`); each result is one upsert into the minute's row
  of `uptime_stats`, which the statistics tick rolls into hour rows.
- App logs are read from Docker (`local` log driver with rotation) while
  the container exists. When dock removes a container (a release
  replaced or failed, drift, an archive), it stops it, has its server
  stream the whole Docker log through gzip into
  `<data dir>/logs/<service>/<release>.jsonl.gz`, then removes it
  ([ADR 0046](decisions/0046-log-history-and-drains.md)). The release
  records that server (`logs_server_id`), and an earlier release's logs
  are read from there, through the node link for a remote server. Each
  service keeps `[logs] keep_mb_per_service`, each server
  `keep_mb_per_server`, for `keep_days`, and nothing is written on a disk
  with less than 1 GB or 5% free; the hourly
  retention pass applies them and drops archives of services that no
  longer exist. Each running container's log is also followed for error
  issues and for an environment's log drains.

## Dock Analytics

[ADR 0037](decisions/0037-analytics.md), [0059](decisions/0059-analytics-area-and-slimmer-service-page.md).

- Caddy routes `/_dock/a/*` on analytics-enabled hostnames to an ingest
  listener on loopback in every daemon. Events go to a capped spool on
  that server first.
- The store, `dock-analytics` (its own Go module with CGO and DuckDB,
  `cmd/dock-analytics`), runs as a container on one server, started the
  first time analytics is turned on. `dock` never links DuckDB
  (`make analytics-deps`).
- The control plane forwards each server's spool to the store over the
  node link, exactly once by spool position, and reads dashboards from it
  over loopback HTTP.

## Image scanning

[ADR 0074](decisions/0074-image-vulnerability-scanning.md). Every live
image is scanned for vulnerabilities on a builder, never next to a build
or deploy: each build, deploy, wake and move counts itself busy, a scan
starts only after a quiet minute, one at a time, and a busy cancel kills
its job container. Syft makes the image's SBOM once, inside a container of
the image itself; the control plane keeps it gzipped in the data dir, and
Grype matches it against the builder's vulnerability database. One scan
per image digest, shared by every release that runs it; the overview, the
org's Images page, `dock vulns` and MCP `list_vulnerabilities` read it.
Once a day a builder gets a newer database and live images are matched
again from their SBOMs; a new critical in a live release alerts. An org's
ignores (a reason and an expiry) apply when findings are read, since a
scan is shared across orgs. SBOMs export as CycloneDX or SPDX, converted as they
stream; a Dockerfile build records its base image, whose tag the daily
refresh checks; air-gapped installs import the database instead.

## Backups and updates

- Stateful services are backed up daily by restic job containers, to a
  repository on the service's server or in a storage connection's bucket
  ([ADR 0019](decisions/0019-templates-and-backups.md), [0039](decisions/0039-backups-on-storage-connections.md)).
  Volume snapshots stay on the server ([ADR 0047](decisions/0047-volume-snapshots.md));
  restores follow one model ([ADR 0061](decisions/0061-one-restore-model.md)).
- The control plane backs itself up daily the same way, and `dock admin
  backup` / `restore` move it to another server
  ([ADR 0021](decisions/0021-control-plane-backup-and-move.md)).
- Updates ([ADR 0028](decisions/0028-updates-from-the-console.md)): the
  daemon finds releases at `[updates] source` (GitHub releases, or a
  directory), downloads and verifies one, and a root updater (`dock admin
  update`, started by `dock-update.path`) swaps the binary and units,
  checks health and rolls back if needed. Nodes are then updated over
  their links.

## Process layout per server

| Process | Managed by | Restarting it affects |
|---|---|---|
| Docker Engine | systemd | Everything (rare) |
| Caddy (`dock-caddy.service`) | systemd, `caddy run --resume` | Traffic, briefly |
| dock daemon (`dock.service` running `serve`, or `dock-node.service` running `node`) | systemd | Only the API/UI, or that node's link to the control plane |
| Updater (`dock admin update`, root) | systemd, `dock-update.service` started by `dock-update.path` when the daemon asks | Runs only during an update |
| App firewall (`dock-firewall.service`) | systemd, oneshot as root | Nothing; it installs the rules |
| BuildKit (builder servers only) | Docker, capped CPU/RAM | Only builds |
| Registry (control plane server only) | Docker | Image pushes and pulls |
| cloudflared (tunnel servers): `dock-cloudflared.service`, or the `dock-cloudflared` container Dock runs | systemd or Docker | Traffic through the tunnel |
| Analytics store (one server, when analytics is on) | Docker, 512 MB limit | Dashboards; events wait in the spools |
| Job containers (backups, snapshots, volume copies, `dock run`, jobs, pre-deploy, image scans on builders) | Docker, removed when done | That job |
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
  store, so a fix lands in both. `internal/httpkit` and `internal/mcpkit`
  are the HTTP and MCP plumbing both servers share.
- tempoBase never links Dock's daemon: no `core`, `api`, `store`, `node`,
  `mcp` or `cli`, no Docker, BuildKit or gRPC. `make tempobase-deps`
  checks it in CI.
- One web project with two entries (`web/` and `web/tempobase/`) sharing
  `features/data`. Dock's UI is style A and tempoBase's style G, on
  system fonts ([design](design/README.md)). One version and one release
  for both binaries; the image is `ghcr.io/nordtempo/tempobase`.

## Code layout

```
cmd/dock/            entry point: CLI commands, `dock serve`, `dock node`, `dock admin`
cmd/tempobase/       tempoBase's entry point
cmd/dock-analytics/  the analytics store: its own module (CGO, DuckDB), run as a container
cmd/dock-probe/      the health probe Docker runs inside app containers (embedded in dock)
cmd/dock-sync/       copies volumes between servers in job containers (embedded in dock)
internal/apitypes/   the API's JSON contract; no server dependencies, so the CLI imports it
internal/api/        HTTP handlers (Huma), auth, SSE, the MCP mount, loopback endpoints
internal/mcp/        the MCP endpoint's tools, over core
internal/core/       domain logic: orgs, projects, environments, services, staged
                     changes, variables, releases, deploy state machine, domains,
                     permissions, audit, event bus, alerts, backups, analytics forwarding
internal/store/      SQLite: migrations, sqlc queries
internal/node/       runtime interface; docker/, build/ (BuildKit), caddy/, remote/
                     (node link), stats/, traffic/, uptime/, collect/ (analytics
                     spool), logarchive/, firewall/, probe/, fake/
internal/cli/        Cobra commands, human and --json output
internal/registry/   the platform registry next to the control plane; gateway/ in front of its bucket
internal/analytics/  analytics events, store interface, client and ingest (stdlib only)
internal/backup/     restic jobs; internal/cpbackup the control plane's own backup
internal/update/     finding and verifying releases; internal/updater applies them
internal/cloudflare/ the slice of Cloudflare's API Dock uses
internal/datapanel/  the Data panel's rules behind a Host interface; Dock and tempoBase
internal/data/       database engines for the Data panel (data/pg: Postgres); data/conncache their connection cache
internal/tempobase/  tempoBase's server: config, auth, connections, its SQLite store
internal/repoconfig/ dock.toml; internal/projectfile project files; internal/compose import
internal/secrets/    AES-GCM encryption of values at rest, key file
web/                 React app; build output embedded into the binary
web/tempobase/       tempoBase's app, sharing web/src/features/data
site/                the landing page (Astro, static), deployed on Dock
docs/                design docs; docs/guide is Dock's guide, docs/tempobase tempoBase's
```

- `core` depends on the `store` package and the `node.Runtime` interface,
  never on Docker or HTTP directly. Every core operation takes the calling
  actor and checks permissions itself (one code path for users and tokens).
- Tests: unit tests for `core` with the fake runtime and a temp SQLite;
  integration tests (`-tags integration`) in CI against a real Docker
  Engine, Caddy and BuildKit; UI tests where logic is non-trivial.
