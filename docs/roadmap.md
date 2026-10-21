# Roadmap

Each milestone ends with something usable on a real VPS.

## M0: Spike (validate the stack)

- Minimal Go binary: an HTTP endpoint, a SQLite file, and listing containers
  via the Docker SDK.
- Measure idle RAM and CPU of the daemon and Caddy on a small VPS.
- Try Caddy admin API route switching vs kamal-proxy for zero-downtime cutover.

**Done when:** we have measured numbers and confirmed or changed the
[tech stack](tech-stack.md).

## M1: Deploy loop (CLI only)

- `platform serve` with systemd unit and install script.
- `platform deploy`: upload source, build, start, health check, cut over.
- `platform rollback`, `platform releases`, `platform logs`, `platform status`.
- One domain per service with automatic HTTPS.
- Basic variables (`platform env set`).
- Single admin token.

**Done when:** a real app deploys from a laptop with zero downtime, and a broken
deploy leaves the previous version serving.

## M2: Railway model + web UI

- Projects, environments, services, environment and service variables,
  variable references.
- Per-environment Docker networks (private networking by service name).
- Web UI with full parity: overview, services, variables editor, releases,
  live logs, deploy and rollback.
- Scoped tokens and audit log.

**Done when:** everything is doable from either the UI or the CLI, and an agent
with a `deploy` token cannot delete anything.

## M3: Day-to-day comfort

- GitHub push-to-deploy (webhook per service/branch).
- Resource limits UI, basic metrics (CPU, RAM, disk per service).
- Disk pruning, log rotation, alerts.
- Clone an environment.

## M4: Data

- Volumes in the UI.
- Scheduled off-box backups and tested restore.
- Database templates (Postgres, Redis) as regular services with volumes and
  backup presets.

## Later (only when needed)

- Preview environments per pull request
- Human approval flow for selected actions
- Multiple servers
