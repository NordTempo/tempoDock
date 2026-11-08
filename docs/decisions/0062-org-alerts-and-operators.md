# 0062: Alerts belong to the org, operators run the platform

Status: accepted (decided by the owner, 2026-11-07). Amends
[0025](0025-platform-console.md) and [0027](0027-organizations.md).

## Context

ADR 0027's phase 2 planned that alerts in a project get its org, org
admins add channels for their org's alerts, and "platform channels keep
receiving every alert". Until now every alert was the platform's: only
operators saw them, and the console's alerts page mixed a full disk with
another org's crash loop.

The owner settled who cares about what. Org admins look after their
services: deploys, crashes, health, backups, errors. The operator runs the
platform: servers, the connections to Cloudflare, GitHub and storage, the
registry, backups of the control plane and Dock's updates, and how much
each org uses. An operator doesn't care about one org's crash loop, but
must be able to administer every org, including one they don't belong to
and a private one, without reading its contents.

## Decision

### Alerts have an owner

- **An alert about a project or service is its org's; one about a server
  or Dock itself is the platform's.** The owner is derived from what the
  alert names when it opens (`alertOrg`): its project's org, or its
  service's. A new kind falls into place by what it names; there is no
  list of kinds per scope.
  - Org alerts: `deploy_failed`, `crash_loop`, `unhealthy`, `uptime_down`,
    `domain_error`, a service's `backup_failed`, `job_failed`, `new_error`,
    `error_regressed`, `error_spike`, `traffic_drop`, `access_request`.
  - Platform alerts: `disk`, `memory`, `cpu`, `server_offline`,
    `connection_failed`, the control plane's `backup_failed`,
    `update_failed`.
- `alerts.org_id` holds the owner (NULL for the platform). Migration 0070
  gives existing alerts the org of the project or service they name.
- **Channels belong to an org or to the platform** (`alert_channels.org_id`,
  names unique per org) and receive only that owner's alerts. This
  replaces 0027's "platform channels keep receiving every alert". A
  resolution still goes to the channels that heard the alert open.
- **Who sees what:** an org's members read its open alerts and history
  (`GET /alerts`, the org's Alerts page); members with deploy resolve
  them; its unlimited admins manage its channels. The platform's alerts
  and channels are for operators, with `platform=true` (`dock alerts
  --platform`, the console's Alerts page). Live `alert` events follow the
  same rule.
- **The console shows nothing about org alerts**, not even a count per
  org: they are the org admins' job.
- **Migration of channels:** existing channels stay platform channels. So
  that an install's own team keeps hearing about its services, each is
  copied into every org with an operator among its admins. The copy is
  sealed for its own row, so the daemon makes it on start
  (`copyPlatformChannels`, from `alert_channel_copies`) and audits it in
  the org's log. A platform channel's sealed settings keep their AAD
  (its name); an org channel's is `<org ID>/<name>`.

### Platform alerts cover what the operator runs

All from what Dock collects or does anyway, with no new poller or timer
(docs/performance.md, "idle means idle"):

- **Servers:** disk (as before), **memory** above `[alerts]
  memory_warning_percent` (90) for `memory_minutes` (5), critical above
  `memory_critical_percent` (97), and **CPU** above `cpu_percent` (90) for
  `cpu_minutes` (15; `cpu_percent = 0` turns it off), from the statistics
  tick's samples, with a 5 point hysteresis. Offline as before.
- **Connections** (`connection_failed`), opened when a call Dock makes
  fails and resolved by the next that works:
  - a Cloudflare connection whose token Cloudflare refuses or lacks a
    permission (`cloudflare:<id>`);
  - the GitHub App, when Dock can't sign in as it or get an installation
    token (`github:app`); a removed installation is the org's business,
    not the App failing;
  - backup storage that a backup job can't reach (`storage:<url>`);
  - the registry: a failing prune (`registry:prune`) or a pushed image the
    registry can't find (`registry:push`).
  A connection nobody uses isn't checked, and can't be seen failing.
  Incoming webhook signature failures don't raise alerts: anyone with a
  hook's URL could cause them.
- **Dock itself:** the control plane's backup (as before) and a **failed
  update** (`update_failed`): the control plane's (`update:dock`) or a
  server's (`update:<server ID>`), resolved by the next update that works
  there.

### Operators administer every org

Operators aren't members of every org, and must not need to be. The
console gets an **Organizations** page (mock B: a list sorted by usage
with a detail column) listing every org with its owner and admins,
members, created date, project and service counts, status (active,
suspended, no admin) and resource use: CPU, memory, disk and volumes,
backup storage, registry images and bandwidth, all from statistics Dock
keeps, in a fixed number of grouped queries.

Operator actions on any org, each audited in the platform's log and the
org's: edit its settings, manage its members (including giving an org
without an admin one), suspend it (deploys and changes are refused, apps
keep running) and unsuspend it, stop its services, and delete it (typing
its name to confirm).

An org's content stays hidden from operators: variables, logs, data, code
and the inside of its projects. **Join as operator** is the way in when
it is needed: it makes the operator an admin of the org for a limited
time, writes an audit entry the org's admins see, and the membership
expires by itself. Permissions are checked in core through `Actor`, as
everything else.

How it works (`internal/core/operator.go`, `/console/orgs/...`):

- **Use per org** in a fixed number of queries: orgs with their member,
  project and service counts in one pass, their admins, each service's
  org, CPU a week ago (the 10-minute points), the newest backup of each
  service, 30 days of hourly traffic and the registry's kept images. CPU
  and memory now come from the statistics in memory; volumes from the
  servers' last disk readings, kept per service. The Organizations page
  asks for stale servers (read over a minute ago) to be read again in the
  background, at most once a minute each; the overview doesn't. Images
  are a count: the registry doesn't size images per service cheaply.
- **Owner** is an org's first admin. **No admin** is an org without an
  admin of its own; an operator who joined doesn't count, and may leave
  while the org keeps one.
- **Operator actions** reuse the org's own operations (rename and slug
  ([0064](0064-changing-an-org-slug.md)), roles,
  removing members) with the operator acting as an admin there
  (`operatorIn`), built only by these functions, so it never reaches the
  org's content. Each is audited in the org's log and the platform's.
  Adding a member takes an existing account.
- **Suspended** (`orgs.suspended_at`): the actor of a member or token
  carries it, and any scope above read is refused with a conflict that
  says the operator suspended it. Every deploy, pushes and previews too,
  passes `startRelease`, which checks the org however it was asked for.
  Apps keep running; reads work.
- **Stop services** stops each running service as `dock stop` does.
- **Delete** needs the slug typed. An org without projects is deleted as
  its admins delete it. One with projects keeps their history, since
  projects are never deleted: every project is archived (containers
  stopped, volumes and releases kept), members, invites, variables,
  registry credentials and policy removed, tokens revoked, and the org is
  marked deleted and hidden from every list.
- **Join as operator** adds a membership with `expires_at` (1 to 24
  hours, people only). Lookups ignore it from then on, so access ends at
  once without a timer; the hourly retention pass removes the row and
  audits it. A live event stream opened while a member keeps streaming
  until it reconnects.
- No CLI yet for these: they are the console's (ADR 0026 asks for UI
  with every CLI command, not the reverse). A `dock` command can follow
  when operators script them.

### Servers are the operator's (2026-11-08)

Org members have no use for which servers run their apps, and server
names and addresses are the platform's business. Only operators see
them (`Actor.seesServers`):

- **The server list, server pages and placements** are operator-only.
  The UI and the CLI stop asking for them for everyone else.
- **Environments** carry `server_status` for everyone (whether deploys can
  reach the server: `online`, `offline`, ...) and `server`, the name,
  only for operators. Deploy buttons, the "server offline" problem and
  `dock status` read the status. A service's `server_offline` keeps its
  status, without the name.
- **Moves** show org members that the environment is moving and its step,
  not from or to where; the move's notes and errors are the operator's.
  Its live event goes out twice, with servers to operators and without
  to the rest (`busEvent.noServers`).
- **Text an org sees names no server**: error messages, release events,
  build logs ("the build server"), the org's audit log, log drain lines
  (syslog's HOSTNAME is `project.environment`) and live usage events.
  Remedies only an operator can apply say "a platform operator".
- **Domain DNS records keep the public IP** an A record must point at:
  that address is public in DNS anyway, and a custom domain needs it.

### The platform sets defaults and limits; orgs decide within them

The owner's rule for everything else in the console: the platform runs
infrastructure and sets defaults and limits; orgs own their people,
access, connections, policies and activity within those limits.

- **Activity in three levels.** The console's Activity shows the
  platform's log only: servers, accounts and logins, platform settings,
  updates, backups, and operators' actions on orgs. An org's Activity
  shows its own; project and service tabs are filtered views of it. What
  an operator does to an org is written to both. The console's mixed
  "organization or platform" switch is gone: an operator sees an org's
  own activity by joining it.
- **Accounts are the platform's, roles the org's.** The console's Users
  page lists every account with the orgs it belongs to and owns, and
  manages accounts (create, disable, delete, reset password) and who
  operates the platform (`PUT /console/users/{id}/operator`; one who can
  sign in always remains). Roles are set in each org's Members. Dock has
  no per-project permissions for people (only tokens are limited to a
  project or environment), so none moves; project and environment rules
  that exist are the data access policy's, now in the org's settings.
- **Data access:** each org edits its own rules (its default, projects,
  environments) in its settings. The platform keeps a default (where an
  org set nothing, in place of the built-in rules) and a ceiling (the most
  any org rule or token gives) per subject and environment kind
  (`/console/data-policy`, migration 0074). Org rules above the ceiling
  are refused; older ones are capped where they apply. Org admins keep
  full access. With neither set, nothing changes for existing installs.
- **Analytics:** the store and its server stay the platform's, and so do
  the platform's site defaults. Each org sets defaults for its new sites
  (`/org/analytics/defaults`); a new site starts with them, existing
  sites keep their settings.
- **GitHub and Cloudflare:** the GitHub App stays the platform's; an org
  installs it for itself and manages its own installations. An org's
  Cloudflare connections and domains are its own; the platform keeps the
  connections for Dock's own address and server tunnels.
- **Org settings** (mock S2) hold every section in one grouped column
  with counts and marks: General, Variables; Members, Tokens, Single
  sign-on; GitHub, Cloudflare, Registries; Data access, Analytics
  defaults.

### The console overview answers "is the platform healthy?"

Its first screen (mock O): open platform alerts, the servers and their
use, the state of each connection from the last time Dock used it, and
the top orgs by usage.

### The console has no current org

Nothing in the console depends on the org the operator was in. Its API
calls go without `Dock-Org` (the session's `/me` keeps it, to know where
to go back), its pages load no org's projects, and its sidebar is headed
"Platform" with no org switcher. An org's information shows only when
the operator picks it on the Organizations page. "Back to projects"
returns to the org they came from. Links that leave the console don't
preload, since a preload from inside it would read without the org.

### One place for errors

Errors are for org members (the plan of 2026-11-05): issues grouped by
fingerprint with a count, first and last seen and a trend, from frontend
errors (Analytics), backend errors parsed from the log stream in one
streaming pass with bounded memory, and 5xx spikes from traffic; linked to
the release where each first appeared, with "N new errors since this
release" on the release page, and alerts on a new issue and on a
regression. The org gets an **Errors** page (mock B: a facet tree of
projects and services with counts, issues grouped by service) with its
open alerts on top; a service's Errors tab is the same view filtered to
it. One indexed table and a fixed number of queries, for 100+ services.

How it works:

- **An issue** is one fingerprint in one service (`error_issues`, unique
  per service), with its count, first and last seen, the release numbers
  it was first and last seen in (0 when none runs), its latest sample (at
  most 8 KB, secret values masked) and its errors per hour for 7 days
  (`error_counts`). It is **new** while it was first seen in the
  service's active release or in the last 24 hours, **regressed** once
  seen again after it was resolved (until it is resolved again),
  **resolved** by a person (`POST /errors/{id}/resolve`, deploy, audited)
  or by itself after 7 days without an error (the hourly retention
  pass), and **ongoing** otherwise. Resolved issues not seen for 30 days
  are deleted. Issues of archived services are hidden.
- **Logs.** Every running service container has one log follower,
  started from container start events, daemon start and a server's
  reconnect (the machinery log drains had, now for every container; a
  drain is one more consumer of its lines). `internal/errparse` reads
  each line as it arrives: a stated error level (JSON `level`, `lvl`,
  `severity` and pino's numbers, logfmt `level=`, a console level
  column), as the logs pane judges lines, so stderr alone and info lines
  that say "error" are not errors; a Go panic and its goroutine trace,
  a Python traceback and a JavaScript or Java exception with its `at`
  frames are one error each, at most 50 lines and 8 KB; error words only
  without a stated level. An HTTP 5xx in an access line is traffic's,
  not a log error. An error ends at the first line that doesn't continue
  it, when the stream ends, or on the tick after 2 s without a line. A
  plain line costs a lock and a byte scan, no allocation. A remote
  server's followers are streams on its node link: at most 150 per
  server outside environments with drains.
- **The fingerprint** is a hash of the error's type, its message with
  numbers, hex IDs, UUIDs and quoted values made alike (addresses and
  ports become `0.0.0.0:0`), and its top three frames normalized the
  same way, so their line numbers are zeros: an issue survives a deploy
  that moved its code, and a fix elsewhere in the file doesn't split it.
- **Frontend** issues are Dock Analytics' error groups: the forwarder
  reads each error of a batch it sends the store, under the service and
  release the ingest stamped on it (only from the server the service
  runs on), fingerprinted `fe:<group>` with the store's own group, so
  counts are exact and an issue links to its analytics group. Analytics
  already alerts on a new group (`new_error` with the site and group), so
  frontend issues don't raise a second one; their regressions do.
- **Traffic** issues: a minute in which a service answered at least 5
  requests with a 5xx status, and at least 5% of its requests, counts
  its 5xx answers on the service's `traffic:5xx` issue ("5xx answers").
  Traffic counts status per service, not per path, so there is one such
  issue per service.
- **Writing.** Sources add to a sink in memory without blocking: at most
  50 fingerprints per service and 2,000 in all between two ticks. The
  statistics tick writes it in one transaction; a tick opens at most 10
  issues per service and a service has at most 1,000, so a flood of
  unique errors can't blow up the table.
- **Alerts.** A new issue from logs or traffic opens `new_error`
  (`errors:<service>:new:<issue>`), a regression `error_regressed`, both
  the service's org's, with the issue in `target.error_issue`. Each
  resolves itself after a day or when the issue is resolved; a service
  has at most 3 open, so a broken deploy is a few alerts.
- **Reading.** `GET /errors` takes project, environment, service, state
  (open, resolved, all) and limit (200, at most 500): the most recently
  seen issues of the filter in one indexed query, ordered new, regressed,
  ongoing, each by last seen, their trends from one range query over the
  org's counts, and facets (open issues, new or regressed ones and the
  last 24 hours' errors per service) for the environment filter only.
  `GET /errors/{id}` adds the sample. A release's `new_errors` counts the
  issues first seen in it. `dock errors` and the MCP tool `list_errors`
  read the same.

## Consequences

- Org admins hear about their own services through their own channels;
  operators' channels get only what operators act on.
- `GET /alerts` and the alert channel operations act on the caller's org
  unless `platform=true`. Clients that listed alerts as an operator now
  get their org's; `--platform` gets the old platform view minus other
  orgs' alerts.
- A deploy token can read its org's alerts; before, only operators could.
- The console's overview and Alerts page no longer show org problems.
  A full disk no longer shows in a project's problems for its members:
  servers are the operator's.
- Supersedes the "Alerts" row of 0027's table and its phase 2 "Next: org
  alert channels". Amends 0025's console: Organizations joins it, and its
  overview is about platform health.

### Connections

The owner's rule: the platform runs infrastructure and sets defaults and
limits; orgs own their own people, access, connections, policies and
activity within those limits.

- **GitHub:** the App stays the platform's. Operators create and remove
  it, see every installation and assign or move one between orgs. An
  org's unlimited admins see the App and their org's installations
  (`GET /github/org`, `dock github installations`), get an install link
  for their own org (`POST /github/installations` without `org`, `dock
  github install`) and unassign their own installations. An installation
  another org has is refused as before. Audited in the org's log.
- **Cloudflare:** a connection is the platform's (`org_id` NULL) or an
  org's own. The platform's run server tunnels (picked by `--cloudflare`,
  join tokens) and serve Dock's own names; `shared` lets every org's
  domains use its zones. An org's own serve only its domains. Operators
  manage the platform's with `platform=true` (`dock cloudflare
  --platform`), an org's unlimited admins their own, as with alert
  channels. Migration 0073 makes existing connections the platform's,
  shared, so installs keep working; a new platform connection isn't
  shared until an operator says so. Stopping sharing keeps the domains
  already in its zones.
- **Zones:** an org's hostname looks in the org's own zones first, then
  in shared platform zones, never another org's; a name under a server's
  base domain uses any platform zone. `GET /cloudflare/zones` lists only
  those, each once. An org's own connection takes only zones Cloudflare
  serves (active): a zone added to an account but not delegated proves
  nothing, and domains from Cloudflare need no TXT proof
  ([0042](0042-domain-verification.md)).
- **A zone may be on several connections** (amended by the owner,
  2026-11-07): the platform's and any number of orgs' own, so a zone can
  run the platform's tunnels and names and serve one org's domains
  without the other orgs seeing it, and two orgs may both hold it.
  Migration 0076 makes zone names unique per connection. Of one owner's
  connections holding a zone, the oldest is used. `shared` stays all
  orgs or none; an org adds the zone itself instead of the platform
  sharing it with chosen orgs.
- **A hostname is still one org's:** a domain from Cloudflare is
  verified at once, and a verified hostname is unique across orgs
  ([0042](0042-domain-verification.md)), so the second org is refused
  (`hostname_taken`). Its record (`cloudflare_records`, one per
  hostname) stays with the connection that made it: saving one with
  another connection changes nothing while the domain lives. All
  connections share the install's `dock:<install>` comment, so this row
  is what keeps one org's connection off another's records.
- **When a zone leaves a connection** (a new token or a refresh no
  longer reaches it, or the connection is removed), its records move to
  another connection holding the same zone that the domain's org may
  use, and are routed again with it (the CNAME is found by its comment).
  With none they fail saying what to do, and move once a connection
  gets the zone or a platform one is shared. A failed record is never
  retried with a connection that lacks its zone. A new token or a
  refresh reports the zones added and dropped and the domains that
  failed. A connection a server tunnel uses can't be removed; Change
  token (`dock cloudflare set NAME --platform --token-stdin`) narrows
  its zones instead.
- **Tunnels stay the platform's:** a domain in an org's zone goes through
  the server's tunnel, which must be in the zone's Cloudflare account. A
  server without one gets it made with a platform connection in that
  account, or the domain is refused saying so. So an org's own
  connection in an account the platform has no tunnel in can't route
  domains yet.
- **Failures:** an org's own connection failing is the org's
  `connection_failed` alert (its key `cloudflare:<id>`, its target
  `cloudflare:<name>`), for its members and channels; a platform
  connection's stays the platform's. A refresh or a new token retries the
  connection's failed domains, since `dock cloudflare retry` is the
  operator's.
- **Sealed tokens:** a platform connection's keeps its AAD (its name), an
  org's is `<org ID>/<name>`, as channels.
