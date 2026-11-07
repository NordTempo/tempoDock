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
    `error_spike`, `traffic_drop`, `access_request`.
  - Platform alerts: `disk`, `memory`, `cpu`, `server_offline`,
    `connection_failed`, the control plane's `backup_failed`,
    `update_failed`.
- `alerts.org_id` holds the owner (NULL for the platform). Migration 0067
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
- **Operator actions** reuse the org's own operations (rename, roles,
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

### The console overview answers "is the platform healthy?"

Its first screen (mock O): open platform alerts, the servers and their
use, the state of each connection from the last time Dock used it, and
the top orgs by usage.

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
