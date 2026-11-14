# 0027: Organizations and the platform operator

Status: accepted (decided by the agent; direction from the owner); superseded in part by [0062](0062-org-alerts-and-operators.md) (alerts and channels), [0064](0064-changing-an-org-slug.md) (slugs can change), [0065](0065-sign-in-is-the-platforms.md) (signup is the invite-only switch, no `signup` setting), [0080](0080-permissions-roles-groups-and-agents.md) (operators are a platform role with their own credentials; operator tokens are legacy)

## Context

[0008](0008-keep-doors-open.md) deferred organizations, billing and
hosting untrusted workloads. The owner now wants the door opened on
purpose: Dock may become a public service with pricing, where people sign
up, see their projects and deployments, and never think about servers or
backups. Before that it runs invite-only. Not now, but the shape of the
data should allow it, and the cheapest time to change that shape is
before launch, while the only data is our own.

[0025](0025-platform-console.md) put everything platform-wide in the
platform console and made admin scope the operator, with "no operator
flag" until Dock hosts several workspaces. That time is now.

The owner's model: projects live in organizations; organizations can have
their own variables; **everything is an organization**: a person who
signs up alone gets one, and can create more. Backups are not the user's
concern: they happen automatically and are managed in the platform
console.

| Force | Means here |
|---|---|
| Simple | One kind of organization. No "personal account vs team" split anywhere in the code. |
| Nothing breaks | Existing installs, tokens, CLI links and scripts keep working unchanged. |
| One permission path | Every check still goes through `Actor` ([0008](0008-keep-doors-open.md) item 3); orgs extend it, nothing bypasses it. |
| Performance | No extra queries per request beyond a constant few, an index for every `org_id` filter, nothing new running while idle. |

## Decision

### Organizations

- An **organization** (org) owns projects, manual API tokens, its alert
  channels, data policy and audit log. Environments, services, releases,
  variables, domains, volumes and backups belong to it through their
  project.
- **Everything is an org.** Whoever signs up gets an org named after
  them, as its admin (an install's first org, from bootstrap or the
  migration, is `default`). It is the same as any other
  org: others can be invited into it later. There is no "personal" flag.
- **Roles are per membership.** `read`, `deploy` and `admin` keep their
  meaning, now within one org (`org_members.role`). An org always keeps at
  least one admin.
- **Slugs** follow the project name rules (1-32 lowercase letters, digits,
  dashes) and are unique per install. Admins may change an org's slug;
  the old one keeps working until another org takes it
  ([0064](0064-changing-an-org-slug.md)). **Project names are unique per
  org**, not per install. Docker resources were already named by ID and
  generated hostnames already de-duplicate, so neither changes.

### The platform operator

The person running the install is a different role from an org admin.
`users.operator` (and `tokens.operator`) marks it. Operators manage the
platform: servers, placement pins, server statistics, the registry,
backups, the control plane backup, platform alert channels, and user
accounts (create, disable, delete, reset a password). Operators are not
implicitly members of every org.

On a self-hosted install the first user is the operator and the admin of
the first org, so a single team never notices the split.

### Backups are a platform service

Every stateful service is backed up automatically, to the platform's
repository, on the platform's schedule ([0019](0019-templates-and-backups.md)).
Configuring the repository, retention, the hour, per-service schedules
and revealing the repository password are **operator** actions, in the
platform console. Org members see their services' backups (read) and can
back up now and restore into a new service (deploy). `backup_settings`
stays one row per install.

### Choosing the org on a request

The API's paths do not change. Every authenticated operation takes an
optional `org` query parameter (the slug):

- A manual token belongs to one org; `org` may be omitted or must match.
- A UI session or `dock login` token acts for a user. With `org`, the
  user must be a member (otherwise: not found). Without it, the user's
  only org is used; with several, org-scoped operations fail and say to
  pass `--org` / `?org=`.
- The actor's scope is the user's role in that org, capped (for a `dock
  login` token) at the token's scope.

Why a parameter and not `/orgs/{org}/projects/...`: 54 of 92 paths would
move, breaking every existing client and token user for no gain in
safety (the actor carries the org either way). One mechanism serves REST,
SSE (`EventSource` can't set headers) and MCP. Vercel's API does the same
with `teamId`.

The CLI gets `--org`, `dock org list | create | use`, and records the org
in `dock link`. The web UI puts the org in its URLs (`/<org>/<project>/...`;
`/console`, `/account` and `/login` stay outside any org, so org slugs
can't take those names) and sends it as the `Dock-Org` header, since
adding a parameter means rebuilding the browser's request, which loses
its body. The switcher is the menu at the top of the sidebar (mock A,
picked by the owner): your orgs with your role in each, and a new one.

### What else becomes per org

| Thing | Rule |
|---|---|
| Projects | `org_id`; names unique per org. |
| Manual tokens | `org_id`; limits to a project or environment still apply. `dock login` tokens have none: they act for their user in each of the user's orgs. |
| Live events | Filtered by org, at the one place they are filtered by project today. |
| Audit | `org_id`; platform events (servers, accounts and logins, backup settings, alert channels, the system's own) have none. Org members read their org's log; operators read the platform's (`platform=true`, `dock audit --platform`). |
| Alerts | Phase 1: alerts and alert channels stay the platform's, for operators. Phase 2: alerts in a project get its org, org admins add channels that receive their org's alerts, and platform channels keep receiving every alert. |
| Data policy | Rules per org; a rule without a project is the org's default. |
| Servers | The platform's only, never an org's (decided by the owner): orgs and their members don't think about servers. They say whether an environment is production or non-production, and the operator's servers, placement and policies (which kinds get backups, for example) do the rest. Operators list servers with statistics; org members see only the names of those their environments run on. |

### Migration

Installs with any users or projects get one org, `default`. Every
project, manual token, data policy rule and audit event moves into it.
Every user becomes a member with their current role; admins become
operators. Unlimited manual admin tokens become operator tokens, so
automation that manages servers keeps working. Alert channels stay
platform channels. Fresh installs create their first org at bootstrap.

### Phases

1. **Orgs as the boundary** (this ADR): schema, `Actor`, permissions,
   API parameter, CLI (`--org`, `DOCK_ORG`, `dock org list | create |
   use`), MCP (`/api/v1/mcp?org=`). The web UI sends the org and, for a
   user with several, acts in their first until the switcher exists. One
   org per install in practice.
2. **Org UI and membership**: org switcher and org in UI URLs, org
   settings (mock A: a settings sidebar with Members, Variables, Tokens,
   Activity and General), invites by link (emailed in phase 3), leave
   and remove, role changes, org variables referenced as `${{org.KEY}}`
   (explicit only, never auto-injected, one value per key, a redeploy
   rolls a change out): done. Next: org alert channels. No servers per
   org (see Servers above).
3. **Signup**: an install setting `signup = closed | invite | open`.
   For now Dock is the owner's company's internal tool, on a public
   address but invite-only: no signup page, people join by an invite.
   Open signup comes with paying customers, email verification and a
   payment system, later.
4. **Hosted**: running strangers' code on servers we own needs its own
   ADR: sandboxed runtime and builds, per-org quotas, egress limits,
   metering and billing, abuse handling. Custom domain verification
   came first: with any account able to have an org, a hostname was
   first come, first served across orgs, so one could squat a name
   another wanted. Since [0042](0042-domain-verification.md) an org's
   custom hostname routes only once a TXT record proves it controls it,
   and a hostname another org has proved is refused.

## Consequences

- Supersedes [0025](0025-platform-console.md) item 2: the console is for
  operators, and an org's admin is not one.
- Per [0026](0026-ui-cli-parity.md), the CLI's org selection (`--org`,
  `dock org`) gets its UI with the org switcher in phase 2.
- Supersedes 0008's "organizations not built now" for organizations
  only; billing, quotas and untrusted workloads stay deferred to phase 4.
- Every org-scoped query gains an `org_id` filter with an index, and the
  actor resolves its org with one indexed lookup per request.
- Clients that never pass `org` keep working as long as their user has
  one org, which is every user of an install that doesn't create more.
- An operator is now needed for server and backup administration; an org
  admin who isn't one can no longer add servers. On self-hosted installs
  they are the same person.
