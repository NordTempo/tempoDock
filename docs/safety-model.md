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
| `admin` | Destroy services, delete volumes, manage users and tokens, read secret values |

- Tokens can be limited to specific projects and environments, so an agent can
  deploy `myapp/staging` but not `myapp/production`.
- Tokens are stored hashed, have a name, and can expire.
- Users have a role that maps to the same scopes. In v1 that's mostly "admin",
  but the check is the same code path tokens use.
- **Two kinds of token, two lifetimes.** A token from `dock login` acts for
  the user who approved it: every request checks that user, so it stops
  working the moment they are disabled or deleted, and its scope is capped
  at their *current* role. Disabling, deleting or changing or resetting
  their password also revokes it for good. A token created by hand (Tokens
  page, `dock token create`, bootstrap) belongs to no user: it keeps its
  scope and keeps working after whoever created it is disabled, deleted or
  demoted, until it expires or is revoked. When someone leaves, revoke the
  tokens they created as well as disabling them.
- `dock login` approval needs the code from the terminal typed into the
  page; the link carries no code. Anyone who can reach the API can start a
  login, so a link that approved with one click would let them phish a
  signed-in admin for a token. Wrong codes lock code entry for minutes.
- Secret values are hidden from `read` and `deploy` in the API, UI, logs and
  events. Every variable value is secret; only `admin` can reveal them.
  Build logs, container logs (live, or kept from a failed release) and live
  build log events show the values of the release's environment (with
  references to shared and org variables resolved, and a multi-line value
  line by line) as `*****` to anyone else, in the UI, CLI and MCP alike.
  Values shorter than 6 bytes and the variables the platform injects
  (`PORT`, `DOCK_*`) aren't masked: short values are rarely secrets and
  masking them would shred the logs. The masking protects against
  accidental exposure, a value printed or echoed as is; an app that encodes,
  splits or transforms a value before printing it gets past it. Nor does any
  of this protect against a `deploy` token on purpose: it can ship code that
  sends its own environment anywhere. Treat `deploy` on an environment as
  access to that environment's secrets, and limit tokens to the environments
  they need (an agent with `myapp/staging` never sees production secrets).

## Non-destructive by default

- **Destroying a service** stops and archives it. Real deletion happens after a
  grace period (default 7 days) or through an explicit `admin` command.
- **Volumes are never deleted as a side effect** of any other operation.
- **Diffs before risky changes:** changes that touch domains, volumes or limits
  show what will happen first, in both CLI and UI.
- Client flags like `--yes` skip confirmation prompts, never permission checks.

## Locked environments

An admin can lock an environment ([0044](decisions/0044-environment-lock.md)),
typically production once people use it. Tokens (API tokens, `dock login`
tokens, MCP: every agent) can still read everything in it, but every change
they try is refused with `environment_locked`: deploys, rollbacks,
restarts, variables, settings, domains, volumes, backups, archive and
unlocking it. Their data access there is capped at proposing changesets.
People in the web UI are not affected, and an admin can open a locked
environment for a while.

What a token tried becomes an **access request**: the exact call and its
arguments, sealed, with who asked and why. An admin approves or rejects
it in the web UI; approved, it runs as the token, through every check it
would pass then (a revoked token or a call that no longer applies fails
safely), and the audit log records the token and the approver. Tokens
never approve. GitHub pushes and `dock deploy` uploads into a locked
environment wait as requests too: production's deploy approval. The check is core's `checkEnv`, which every
operation inside an environment passes, so new actions are covered without
code of their own.

A token can't escape the lock through accounts either. Tokens never
create, reset, enable, disable or delete user accounts; a person does that
from a signed-in session. While any environment of an organization is
locked, its tokens also can't invite members, change roles or create admin
or operator tokens. Revoking, removing members and deploy or read tokens
stay open.

## Every change is reversible

- Code deploys and config changes both create immutable releases.
- New releases only receive traffic after passing a health check.
- Rollback re-activates a previous release through the same health-checked path.
- Images for the last N releases (default 5) are kept for instant rollback.

## Domains

- A hostname belongs to at most one service, so one app can't take over
  another's domain.
- Certificates are only issued for hostnames that exist in the DB, so pointing
  a random domain at the server can't trigger certificate issuance.
- Only the daemon can change proxy config (Unix socket, not a network port).

## Servers

- Nodes join with a one-time token and then authenticate with their own
  credential, which can be revoked from the control plane.
- Nodes only execute commands from the control plane; they expose no API of
  their own to the network.
- Each server accepts production, non-production or both. With production
  servers that accept only production, a runaway staging app or build can't
  starve production. (A lone server accepts both until a second one joins.)
- Server and user administration (accepted kinds, pins, statistics,
  password resets) needs an `admin` token that isn't limited to a project.

## Network isolation

Deployed code is untrusted, and servers often sit on a home LAN or a
tailnet. Apps and builds reach the public internet, nothing private:

- **Firewall by default.** Traffic from app networks and from BuildKit to
  private, CGNAT/Tailscale, link-local and IPv6 ULA addresses is dropped, and
  so is traffic to the server itself (API, SSH), except Caddy's ports and
  DNS. LAN hosts an app needs go on an explicit allowlist
  (`[network] allow_private`). Root installs the rules
  (`dock-firewall.service`); the daemon can't change them.
- **Environments are isolated.** Each environment has its own Docker
  network, with addresses from a dedicated pool. Containers reach their own
  environment by name and nothing in other environments.
- **Only loopback ports.** Published app ports bind to 127.0.0.1, where only
  Caddy uses them (Docker 28+ keeps them off the LAN).
- **Least privilege on the host.** Only the daemon process gets Docker
  access. Caddy runs as its own user without it, so a Caddy compromise
  doesn't become root.

BuildKit runs privileged to build images, so the firewall contains build
steps but not a BuildKit escape. See the [install guide](guide/install.md#app-firewall)
for the exact rules.

## Concurrency

- One deploy at a time per service (lock in SQLite).
- One build at a time per server, to protect RAM and CPU.

## Keeping the box alive

- **Resource limits** on every app container, with defaults. Headroom is reserved
  for Docker, Caddy and the daemon.
- **Builds are capped** in their own BuildKit container (CPU, RAM, low priority).
- **Disk hygiene:** log rotation via the `local` log driver, automatic pruning of
  images outside the rollback window and of old build cache, retention of
  build logs and the audit log, disk usage alerts
  ([0020](decisions/0020-retention-and-alerts.md)).
- **Alerts:** full disks, offline servers, crash loops, failed deploys and
  certificate failures reach admins by webhook, Slack or email.
- **Platform isolation:** the daemon, Caddy and BuildKit are installed by the
  install script, never deployed through the platform. An agent cannot deploy
  over the control plane.

## Audit

Every mutating API call writes an AuditEvent: actor (user or token name),
action, target, summary of the change, and timestamp. Visible in the UI and CLI.

## Backups (M5)

- Every stateful service is backed up daily with restic
  ([0019](decisions/0019-templates-and-backups.md)): template databases with
  their own tool (`pg_dump`, Redis snapshots), other volumes file by file.
- Off-box once an S3-compatible repository is configured; until then backups
  stay on the server and say "not off-box" wherever they show.
- Restores never overwrite a live database: they create a new service.
- Restore is tested, not assumed: the integration suite backs up and restores
  Postgres and Redis on real Docker.
- The control plane backs itself up daily into the same repository
  ([0021](decisions/0021-control-plane-backup-and-move.md)): a consistent
  SQLite snapshot (`VACUUM INTO`, never the live WAL file), the keys and the
  config. `dock admin backup` writes the same archive to a file, 0600; it
  holds every secret, and says so.
- A moved control plane can't come back by accident: `dock admin backup
  --move` retires it and `dock serve` refuses to start there.
