# Roadmap

Each milestone ends with something usable on a real server. Later milestones
are deliberately vaguer; we refine them as we learn.

## Where we start

**M0 → M1 on the homelab.** Prove the stack's footprint, then build the
smallest loop that is genuinely useful: deploy an app from the CLI with zero
downtime and safe rollback. Everything before M3 runs on the homelab, so
production is never at risk while the platform is young. The UI comes in M2,
once the API it sits on is real. Production joins in M3.

## M0: Spike and skeleton

- Go module, `cmd/dock`, CI (lint, test, build), README for local dev.
- `dock serve`: one API endpoint, SQLite file, list containers via the
  Docker SDK, embedded "hello" web page.
- Measure idle RAM/CPU of the daemon, Caddy, an idle BuildKit and the registry
  on a small VM in the homelab.
- Spike Caddy: render config from a small DB, atomic reload during traffic,
  on-demand TLS with the `ask` endpoint.
- Spike capped BuildKit: build a Node app with a memory limit, confirm running
  apps stay healthy during the build.

**Done when:** measured numbers are in [experience](experience.md).

## M1: Deploy loop (CLI)

- Install script: checks Docker, installs Caddy, BuildKit, binary, systemd unit,
  creates the first admin token.
- `dock login`, `dock link`, `dock deploy` (source or `--image`).
- Deploy state machine with health-gated cutover and crash recovery.
- `rollback`, `releases`, `logs`, `status`, all with `--json`.
- Domains: add any hostname from the CLI, generated subdomains, automatic
  HTTPS, live DNS and certificate status.
- Service variables (`dock env set/list/unset`).
- Optional `dock.toml` for build and health check settings.

**Done when:** a real NordTempo app deploys to the homelab from a laptop and
from an agent with zero downtime, and a broken deploy leaves the previous version
serving.

## M2: Railway model + web UI

- Projects, environments, services; environment and service variables;
  variable references.
- Per-environment Docker networks (private networking by service name).
- Users, scoped tokens, audit log.
- Web UI with full parity: project overview, services, variables editor,
  releases, live logs and deploy progress, deploy and rollback, command palette.

**Done when:** everything is doable from either the UI or the CLI, and an agent
with a `deploy` token cannot delete anything.

## M3: Production + homelab

- `dock node` mode, node join tokens, the outbound node connection,
  version compatibility checks.
- Environment placement (production vs non-production servers).
- Registry next to the control plane; builds only on builder servers.
- `dock promote staging production`.
- Per-server status in the UI and CLI.
- Move the control plane to the Hetzner server; homelab joins as a node.

**Done when:** a NordTempo app runs staging on the homelab and production on
Hetzner, production is deployed by promotion, and taking the homelab offline
doesn't affect production.

## M4: Day-to-day comfort

- GitHub push-to-deploy (webhook per service and branch).
- Resource limits in the UI; stored, downsampled metrics per service.
- Disk pruning, alerts (disk, failed deploys, crash loops).
- Clone an environment. Export a project's config.

## M5: Data

- Volumes in the UI.
- Scheduled off-box backups and tested restore.
- Database templates (Postgres, Redis): regular services with volumes and
  backup presets ([ADR 0011](decisions/0011-stateful-services-and-databases.md)).
- Non-destructive restore into a new service; `dock connect` tunnels.

## M6: Ready for others (if we go open source)

- Polished install and upgrade path, public docs, license.
- Multiple users with roles in the UI.
- Telemetry-free by default.

## Later (only when needed)

- Preview environments per pull request (on the homelab)
- Moving an environment between servers
- Human approval flow for selected actions
- Hosted control plane managing customer-owned servers (paid offering)
