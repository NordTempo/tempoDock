# Safety model

The goal: an agent can ship code all day, and the worst outcome of a mistake is
a failed release that never received traffic.

## Agents get scoped tokens, not SSH

An agent with SSH effectively has root, so every rule becomes a suggestion. All
access goes through the daemon's API, which enforces permissions.

| Scope | Allows |
|---|---|
| `read` | Status, logs, releases, variable *names* |
| `deploy` | Deploy, rollback, restart, set variables, add domains |
| `admin` | Destroy services, delete volumes, manage tokens, read secret values |

- Tokens can be restricted to specific projects and environments, so an agent
  can deploy `myapp/staging` but not `myapp/production`.
- Tokens are stored hashed and can expire.

## Non-destructive by default

- **Destroying a service** stops it and archives it. Real deletion happens after
  a grace period (e.g. 7 days) or through an explicit `admin` command.
- **Volumes are never deleted as a side effect** of any other operation.
- **`plan` before `apply`:** config changes show a diff first, and changes that
  add or remove volumes or domains are called out clearly.
- Later: optional human approval for chosen actions (push notification or UI
  prompt) instead of a hard deny.

## Every change is reversible

- Code deploys and config changes both create immutable releases.
- New releases only receive traffic after passing a health check.
- Rollback re-activates a previous release through the same health-checked path.
- The last N releases' images are kept on disk for instant rollback.

## Concurrency

- One deploy at a time per service (lock in SQLite).
- One build at a time per server, to protect RAM and CPU.

## Keeping the box alive

- **Resource limits** on every app container (memory, CPU), with defaults.
  Headroom is reserved for Docker, Caddy and the daemon.
- **Disk hygiene:** Docker log rotation (`max-size`, `max-file`) and automatic
  pruning of unused images beyond the rollback window. Alert on disk usage.
- **Platform isolation:** the daemon and Caddy are installed by the bootstrap
  script, never deployed through the platform. An agent cannot deploy over the
  control plane.

## Audit

Every mutating API call writes an AuditEvent: actor (token name), action,
target, before/after summary, and timestamp. Visible in the UI and CLI.

## Backups

- Scheduled off-box backups of volumes and the platform's SQLite file (restic to
  S3 or B2).
- Restore must be tested, not assumed.
