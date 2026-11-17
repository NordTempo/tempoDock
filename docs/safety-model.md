# Safety model

The goal: an agent can ship code all day, and the worst outcome of a mistake is
a failed release that never received traffic.

## Agents get scoped tokens, not SSH

An agent with SSH effectively has root, so every rule becomes a suggestion. All
access goes through the daemon's API, which enforces permissions.

| Scope | Allows |
|---|---|
| `read` | Status, logs, releases, variable *names* |
| `deploy` | Deploy, rollback, restart, set variables, add domains, run commands |
| `admin` | Archive services and projects, delete archived projects (unlimited), manage members, tokens and locks, read secret values |

- Scopes act within one organization ([0027](decisions/0027-organizations.md)).
  Tokens can also be limited to specific projects and environments, so an
  agent can deploy `myapp/staging` but not `myapp/production`.
- Tokens are stored hashed, have a name, and can expire.
- Users have a role per organization that maps to the same scopes; the
  check is the same code path tokens use. Running the platform (servers,
  storage, backups, accounts, sign-in, updates) needs an **operator**,
  a user or token with the operator flag, not an org role
  ([0062](decisions/0062-org-alerts-and-operators.md)).
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
  Build logs, container logs (live, or kept from a failed release), live
  build log events and the log lines a deploy diagnosis quotes show the
  values of the release's environment (with references to shared and org
  variables resolved, and a multi-line value line by line) as `*****` to
  anyone else, in the UI, CLI and MCP alike.
  Values shorter than 6 bytes and the variables the platform injects
  (`PORT`, `DOCK_*`) aren't masked: short values are rarely secrets and
  masking them would shred the logs. The masking protects against
  accidental exposure, a value printed or echoed as is; an app that encodes,
  splits or transforms a value before printing it gets past it. Nor does any
  of this protect against a `deploy` token on purpose: it can ship code that
  sends its own environment anywhere. Treat `deploy` on an environment as
  access to that environment's secrets, and limit tokens to the environments
  they need (an agent with `myapp/staging` never sees production secrets).

## People sign in with a second factor

The UI controls production, so a password shouldn't be enough
([0057](decisions/0057-two-factor-sign-in.md), [guide](guide/two-factor.md)).

- Users add a passkey or an authenticator app; signing in then needs the
  passkey, a code from the app or a one-time recovery code. A passkey with
  the device's PIN or biometrics also signs in alone. Wrong factors spend
  from the same per (email, address) limit as passwords, so a guesser can't
  lock the owner out.
- Sensitive actions (revealing secrets, creating tokens, approving a `dock
  login`, managing people and accounts, changing sign-in policies and
  factors) need a sign-in or confirmation within the last 15 minutes. Tokens
  skip this: creating one was the check.
- Organizations can require a second factor of their members, and the
  platform of its operators. A `dock login` token counts only when the
  sign-in that approved it used one. API tokens are not affected.
- Losing the factor and the recovery codes needs shell access to the
  server: `sudo dock admin reset-2fa <email>`. There is no UI for it, so a
  stolen operator session can't strip someone's second factor.

## Non-destructive by default

- **Archiving a service** (`admin`) stops it and removes its routes; its
  volumes and records stay. There is no hard delete of a service.
- **Volumes are never deleted as a side effect** of any other operation.
  The one exception is a closed preview environment's volumes
  ([0056](decisions/0056-preview-environments.md)); after a move, the old
  server's volumes stay until someone removes them.
- **Deleting a project** is the one operation whose point is deleting
  volumes and history, so it takes three things: the project must be
  archived first, its name typed again, and an unlimited admin with a
  recent sign-in. Its activity and its backups stay
  ([0078](decisions/0078-deleting-archived-projects.md)).
- **Staged changes:** variables, settings, volumes and domains are staged
  and shown (`dock changes`, the UI's bar) before they go live together.
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
- A custom hostname routes only after a TXT record proves the organization
  controls it ([0042](decisions/0042-domain-verification.md)), so one org
  can't claim another's domain.
- Only the daemon can change proxy config (Unix socket, not a network port).

## Servers

- Nodes join with a one-time token and then authenticate with their own
  credential, which can be revoked from the control plane.
- Nodes only execute commands from the control plane; they expose no API of
  their own to the network.
- Each server accepts production, non-production or both. With production
  servers that accept only production, a runaway staging app or build can't
  starve production. (A lone server accepts both until a second one joins.)
- Server and account administration (accepted kinds, pins, statistics,
  password resets) is for operators only; members see their environment's
  server status, not the servers.

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

- **Builds don't share secret-dependent layers.** BuildKit leaves secret
  values out of its layer cache keys, so build steps that mount a secret
  are keyed by a hash of their environment and the secrets' values. A
  project with the same Dockerfile and its own `PIP_INDEX_URL` can't hand
  another environment or org a layer built from its own index. The
  environment's part of the key holds a value only the control plane can
  work out, and a mount counts as a secret's however the Dockerfile
  writes it (quotes, escapes, variables). Not covered: a secret mounted
  by a base image's own `ONBUILD RUN` step, which Dock doesn't see.
- **Image pulls go to public registries.** A server's Docker daemon pulls
  images from the host, outside the firewall. So an image's registry must
  not be the server itself, link-local or cloud metadata, or a private
  address outside `[network] allow_private`. Literal addresses are refused
  when an image is set, and the registry's name is resolved and checked
  before every pull. The platform registry is Dock's own and always
  allowed.

BuildKit runs privileged to build images, so the firewall contains build
steps but not a BuildKit escape. See the [install guide](guide/install.md#app-firewall)
for the exact rules.

Known limit: Docker resolves a registry's name again for the pull, and
follows the registry's token realm and blob redirects wherever they point.
So a registry that rebinds its DNS, or a public one that answers with a
realm or redirect to a private address, can still make the daemon send
GET requests there. They are blind: the daemon only takes a token or a
blob with the right digest from them, so a response's content doesn't
reach the user. Closing this needs a firewall rule for the Docker daemon's
own traffic, which would have to allow the platform registry and every
registry operators allow. It isn't contained enough to add yet.

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
- **Alerts** ([0062](decisions/0062-org-alerts-and-operators.md)): an
  organization's (failed deploys, crash loops, unhealthy services, failed
  backups and jobs, uptime, domains) go to its channels; the platform's
  (full disks, memory, CPU, offline servers, failed updates) to the
  operators' channels. Channels are webhooks, Slack or email. An org's
  channels, status page hooks and log drains never reach the server
  itself, cloud metadata or private addresses outside `[network]
  allow_private`; only the platform's channels may reach the LAN.
- **Platform isolation:** the daemon, Caddy and BuildKit are installed by the
  install script, never deployed through the platform. An agent cannot deploy
  over the control plane.

## Audit

Every mutating API call writes an AuditEvent: actor (user or token name),
action, target, summary of the change, and timestamp. Each organization
has its log; the platform's (servers, accounts, logins, backup settings)
is for operators, and an operator's action on an org goes into both.
Visible in the UI and CLI (`dock audit`, `--platform`).

## Backups

- Every stateful service is backed up daily with restic
  ([0019](decisions/0019-templates-and-backups.md)): template databases with
  their own tool (`pg_dump`, Redis snapshots), other volumes file by file.
- Off-box once an operator moves them to a storage connection's bucket
  ([0039](decisions/0039-backups-on-storage-connections.md)); until then
  backups stay on the server and say "not off-box" wherever they show.
- A template database restores into a new service, never over the live
  one; switching to it is an explicit `dock service switch-references`
  ([0061](decisions/0061-one-restore-model.md)). Restoring a snapshot in
  place takes a safety snapshot first.
- Restore is tested, not assumed: the integration suite backs up and restores
  Postgres and Redis on real Docker.
- The control plane backs itself up daily into the same repository
  ([0021](decisions/0021-control-plane-backup-and-move.md)): a consistent
  SQLite snapshot (`VACUUM INTO`, never the live WAL file), the keys and the
  config. `dock admin backup` writes the same archive to a file, 0600; it
  holds every secret, and says so.
- A moved control plane can't come back by accident: `dock admin backup
  --move` retires it and `dock serve` refuses to start there.
