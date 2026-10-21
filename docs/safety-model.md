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
- Secret values are hidden from `read` and `deploy` in the API, UI, logs and
  events. That protects against accidental exposure, not against a `deploy`
  token on purpose: it can ship code that prints its own environment. Treat
  `deploy` on an environment as access to that environment's secrets, and
  limit tokens to the environments they need (an agent with
  `myapp/staging` never sees production secrets).

## Non-destructive by default

- **Destroying a service** stops and archives it. Real deletion happens after a
  grace period (default 7 days) or through an explicit `admin` command.
- **Volumes are never deleted as a side effect** of any other operation.
- **Diffs before risky changes:** changes that touch domains, volumes or limits
  show what will happen first, in both CLI and UI.
- Client flags like `--yes` skip confirmation prompts, never permission checks.
- Later: optional human approval for chosen actions instead of a hard deny.

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
- Production and non-production environments live on different servers, so a
  runaway staging app or build can't starve production.

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
  images outside the rollback window and of old build cache, disk usage alerts.
- **Platform isolation:** the daemon, Caddy and BuildKit are installed by the
  install script, never deployed through the platform. An agent cannot deploy
  over the control plane.

## Audit

Every mutating API call writes an AuditEvent: actor (user or token name),
action, target, summary of the change, and timestamp. Visible in the UI and CLI.

## Backups (M5)

- Scheduled off-box backups of volumes and the platform's SQLite file (restic to
  S3-compatible storage).
- Restore is tested, not assumed.
