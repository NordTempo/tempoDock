# 0080: Permissions, roles, groups and agents

Status: accepted (decided by Saile, 2026-11-14, from the plan the agent
wrote; built in phases). As each phase ships, the records it changes get
"superseded in part by 0080" in their status lines: [0027](0027-organizations.md)
(roles per membership), [0038](0038-own-tokens.md) (who creates tokens),
[0044](0044-environment-lock.md) (the lock), the data access policy of
[0016](0016-database-panel.md), and [0062](0062-org-alerts-and-operators.md)
(operators).

## Context

Saile: "we should really look into getting a better permissions system.
Both for different user accesses, but also for agents. I feel the way we
have it currently is both unclear how to use, and also bad with very
small customization."

Who may do what was decided by eight mechanisms set on seven pages: an
org role (`read`, `deploy`, `admin`), the operator flag, a token's scope,
a token's project or environment limit, the environment lock, access
requests, the data access policy (with per-token overrides and the
platform's default and ceiling), and admin-only rules in code. Checked
against the code:

| Need | Today |
|---|---|
| A contractor deploys one project's staging and never sees production variables | No: roles are org-wide, people can't be limited, and the lock binds tokens, never a person in the browser |
| An agent reads logs org-wide, deploys only previews, proposes data changes | Partly: one scope and one limit per token; "previews only" can't be said |
| Only two people approve production | No: every admin approves every request |
| Viewers see nothing secret | Partly: read reads every log, trace and error sample |
| `dock update` from a context holding an org admin token | Refused, without saying which credential the platform needs |

And a leak: `dock run` needs deploy and its output wasn't masked, so
`dock run printenv` showed every secret value to anyone who could deploy,
while the agents guide said deploy can't read them.

| Force | Means here |
|---|---|
| One check per request | Core decides in `Actor` checks, as now; nothing bypasses them |
| Clear | One page answers "what can Kari do?", and every refusal says why and what would allow it |
| Small parts | Few concepts; roles are building blocks, groups are job functions |
| Agents never exceed people | A person's agent can't do what the person can't, now or later |
| The platform apart | Running the platform is never an org permission |
| Nobody gains or loses silently | The migration keeps everyone's access exactly, and proves it |
| Performance | One indexed query per request, no N+1, no new timers |

## Decision

| Part | Is |
|---|---|
| Permission | A fixed list in code (`deploy`, `logs.read`, `variables.read_secret`, `data.write`, `approve`, ...), about 40, each at environment, project or org level, marked when it reaches secret values. A feature adds its own; every check names one |
| Area role | A built-in building block, the same in every org, one per area with levels: Deploy (view, operate, edit), Variables and secrets (view, edit, reveal), Logs, errors and traces (view, edit), Data panel (schema, read, propose, write, full), Domains, Analytics, Status pages (view, post, edit), Alerts and Inbox (view, resolve, edit), Projects, Org settings, Members and access, Usage and billing, Activity; plus Approver and Full access (every permission, also later ones) |
| Custom role | Per org: permissions and other roles. Includes can't loop |
| Group | Per org, a job function: a set of role grants, and members (people and org agents). Groups don't nest |
| Grant | A role, a where (the org, a project, one environment) and optionally environment kinds (Production, Other, Previews); in a group, or directly on one person or org agent for a one-off, optionally ending at a time. Grants only add |
| Default groups | Seeded in every org, editable, renamable and removable: Admins (Full access), Developers (today's deploy), Viewers (today's read), Approvers (Approver). The org always keeps one person with Full access |
| Effective access | Per person, per project and environment, with the grants that give it: on the members page and the person's account page, and `dock access show` |
| Personal agent | Belongs to one person. Access is the owner's live access ∩ the owner's agent setting ∩ the org's agent rules. `dock login` makes one |
| Agent setting | Per person per org, on the account page: the area roles switched on or off for all their agents, a level per environment kind, and "ask me first" per role. Starts at everything the person has, minus Approver. One agent can be narrowed further, never widened |
| Org agent | CI and bots, owned by the org: groups and grants like people, managed by people with `agents.manage` |
| Agent rules | The org's ceilings, per area role and kind: the highest level agents may use and from which level it needs approval. They apply to every agent; org agents can be on a bypass list. Fixed in code: agents never approve, never loosen protection, never manage accounts, never create an agent with more than they have |
| Protected environment | Replaces the lock: changes from people and agents (or agents only) wait for its approvers (holders of `approve` there, or named people and groups). Loosening it needs a current approver |
| Platform | Separate: platform permissions, built-in platform roles (Operator, Platform viewer), platform grants, and platform tokens (`dock_p_…`, from `dock login --platform`) that never act in an org. An operator reaches an org's content only by joining it for a while, audited in both logs |
| Refusals | Every forbidden error carries a refusal: what decided it (role, scope, limit, operator, person; later grants, owner, setting, agent rules, protection, platform), what was needed, what the caller has, the fix, and who can change it |

The plan with its mocks: <https://v5da3nqin6ym.postplan.dev>.

### The check

Each request reads the caller's grants once: one `UNION ALL` of its
groups' grants and its direct grants by indexed lookups, and for a
personal agent its owner's rows and agent setting in the same round trip.
Everything after is in memory: checks, list filters, live event filters
and effective access. Custom roles and agent rules are cached per org and
replaced when core writes them (one process, one writer). Grants aren't
cached, so removing someone from a group, or an owner losing access,
takes effect on the next call; long streams re-check on `access.changed`.

### The migration

Members join the default group of their role; tokens become org agents
with the grants of their scope at their limit's place; `dock login`
tokens become personal agents; the data policy's org default sets the
seeded groups' data grants and its raising rules become grants; locks
become protection for agents; operators get platform grants, and
operator tokens keep working as legacy tokens until split. A data rule
that lowers access can't be expressed by adding, so the upgrade stops on
it (preflight). A table test runs every operation under both evaluators,
and one release runs both and decides with the old one before the new
one decides.

### Phases

1. **Refusals explain themselves, and run output is masked** (this
   record's first PR): `apitypes.Refusal` on forbidden errors from the
   `Actor` checks, `Core.Explain` adds the org's admins for people, the
   CLI prints the reason with the fix as its hint, MCP tool errors carry
   it, and the web UI's disabled actions say why. `dock run` output is
   masked live and when read, and its likely cause for everyone, like
   logs, for callers who can't reveal secrets.
2. **Platform split** (built): platform permissions and the Operator
   and Platform viewer roles (`users.platform_role`), platform tokens
   (`dock_p_`), `dock login --platform` kept beside the org login (the
   CLI retries a call the server refuses as the platform's with it),
   Console › Tokens with split, legacy tokens (the bootstrap token, and
   operators' `dock login` tokens until they expire) that are both. A
   platform credential acts on an org's resources (moving an environment,
   pinning a project) in the named org or the install's only one, with no
   org scope there. Two details settled while building: an operator's
   domains skip TXT verification in their org login too (trust in the
   person, not running the platform), and the bootstrap token stays both
   so install scripts keep working with the one token they get.
3. **The model, shadowed** (built): the permission list, area roles and
   seeded groups in code (`perms.go`); `actionPerms` names the
   permission each of today's checks means (until checks take one);
   beside every check, the model's answer from today's state as the
   migration will make it (`shadow.go`), with disagreements counted and
   logged once, `dock platform access-diff` for operators. The
   equivalence test is every core, API and CLI test: their `TestMain`
   fails on any disagreement but the data policy's lowering rules, which
   the migration's preflight stops on. `TestPermissionsFitScopes` checks
   every entry against today's scopes, `TestEveryCheckHasAPermission`
   that no check lacks one (see phase 4 for how). Settled while building: Developers
   also hold Analytics edit (goals, funnels, sites), Status pages post
   and Logs edit (resolving errors), as today's deploy does; analytics
   defaults are Org settings; a change to a project needs a grant on the
   whole project, a read one anywhere in it. Tables and the migration
   come with phase 4, from the same mapping.
4. **Groups decide** (4a built: the back end and the CLI; 4b: the web
   pages). Groups, their members and grants (migration 0107) decide a
   person's checks; the core seeds every org's four groups at start and
   puts members in them by role, and a role change (`dock org role`, an
   invite) is a move between Admins, Developers and Viewers. A person's
   `Scope` is now a summary of their grants (admin with all of Admins on
   the org, deploy with all of Developers), for what still speaks in
   roles: the data policy, which keeps deciding data access until
   agents' phase, and the web UI. Approving follows `approve`, so the
   Approvers group approves without being admins. Tokens keep their
   scope and limits until phase 6. A grant names environment kinds;
   previews are marked on the environment's row (`environments.preview`)
   so a check needs no query. Settled while building: `dock access` was
   already a service's access protection (ADR 0030), so effective access
   is `dock can [email]` and one-off grants `dock grant`.
   Prod's shadow then found checks that named no permission (watching a
   site live, checking its tracking, its replays, its public dashboard):
   the scan only knew a fixed list of check functions, so a loader
   passing its scope and action on (`loadSite`) hid them; their tests ran
   as the system actor, which the shadow doesn't compare; and since
   grants decide, an unmapped check fell back to the person's summary
   role, which says nothing of where they hold it (it let someone with
   Analytics outside production read production's sites). Now the scan
   follows every function that passes a scope or action on to a check
   and resolves constants and locals, so every call site is covered or
   the test says which it can't read; an unmapped check refuses people,
   logs an error and fails the test suites' gates. Replays have their own
   permission (`analytics.replays`, in Analytics view) so a role may leave
   them out.
   So that prod reads a clean diff with the full mapping before groups
   decide, the platform has a switch, `dock platform access-model
   shadow|enforce` (Console › Users; `platform_settings.access_model`,
   migration 0108). Shadow, the default: people's roles decide as before
   groups, and the shadow answers beside each check from their real
   groups and grants (the tests' gates too); enforce: grants decide. An
   operator switches once the diff is clean; a later release makes
   enforce the default. The shadow compares final answers, so a grant
   that gives more than a role shows only where the role passes the
   scope pre-check; `TestPermissionsFitScopes` keeps the seeded groups
   from giving more.
   Hardened after the pre-open-source security review (enforce mode):
   an environment permission asked at the org holds wherever it is held
   at all, so revealing secret values is decided where they are
   (`revealsAt`: the environment, the whole project, or the whole org
   for the org's variables), per event for live streams;
   `TestEnvPermsCheckedAtTheirPlace` refuses an org-level check of an
   environment permission that changes or reveals, but in functions
   that say why. **Nobody gives more than they hold**: a grant, a group
   join, a role change, an invite's role, a token's scope and a custom
   role's parts need the giver to hold every permission at least as
   widely (`covers`), so Members and access edit is no way to Full
   access. Approving is decided in the environment's kind; build log
   and audit events need logs.read and activity.read where they happen;
   errors, alert resolving, incidents' components and org variables'
   uses follow the grants where each one is.
5. **Custom roles; MCP tools declare their permission** (built). An
   org's own roles (migration 0110) are built-in roles and single
   permissions under one name, stored as their names (`roles.parts`), so
   what a built-in role holds in a later Dock reaches the roles that
   include it; a grant names one by `role_id`, read with the person's
   grants in the same query. Settled while building: a role doesn't
   include another custom role or Full access (grant full instead); a
   role a grant names can't be removed; `dock role list --own` lists
   them, so `role list --json` keeps its shape. Every MCP tool names the
   permission its work needs (`internal/mcp/perms.go`), and tools/list
   leaves out what the caller holds nowhere: for tokens that is what
   their scope already hid, so their tool lists don't change until agents
   get grants (phase 6), when `make agent-eval` runs on them.
6. **Agents.** 6a, personal agents (built): a person's `dock login`
   tokens act for them with their grants as they are now, capped by
   their agent setting (migration 0112, `agent_settings`: per area role
   and environment kind a level, `all` or `off`, and ask) and by the
   agent's own narrowing (the same rows with its token), which only
   lowers. No setting: everything but approving, as `dock login` was.
   Environment permissions are capped per kind; org and project ones by
   what every kind allows. A change in a cell marked ask is a
   LockedError that files an access request with `ask_user_id`: only that
   person decides it, in the browser (a lock still goes to the
   approvers). Settled while building: an agent may tighten its setting
   (the CLI is usually the agent's own login) but loosening needs the
   person signed in, recently; settings apply while grants decide
   (enforce), and the shadow compares with them. 6b, org agents
   (built): an organization's tokens hold grants (`grants.token_id`,
   migration 0113 rebuilds grants for the third holder) and join groups
   (`group_agents`); a token's scope and limit become grants when it is
   made, older tokens' once at start (`tokens.granted`), so each does
   exactly what it did. Settled while building: a token keeps its scope
   and limit for what still speaks in them (the data policy's subject,
   MCP's implied project and environment); `dock token access|grant`,
   `dock group add --agent`, and Grants and groups in a token's menu.
   6c, agent rules (built): the org's ceilings per area role and kind
   (migration 0114, `agent_rules`: the most any agent may use, and ask),
   read with every agent's request; an agent uses the lowest of its
   person's setting, its narrowing and the rule. A rule's ask files a
   request the environment's approvers decide (a person's ask goes to
   them). `tokens.rules_bypass` exempts a trusted org agent. Settings ›
   Agent rules, `dock agent rules|rule`, `dock token bypass`.
7. **Protection** (built): a lock can hold people's changes too
   (`--people`, migration 0118, `environments.lock_people`): whoever may
   approve there still changes it in the browser; everyone else's change
   files an access request that runs as them once approved
   (`access_requests.user_id`). Loosening a lock (unlocking, opening it,
   letting people change it again) needs an approver there, not just an
   admin. Settled while building: protection stays the lock's name in
   the UI and CLI (`dock environment lock --people`) rather than a
   rename; approvers named per environment are Approver grants on it.

## Consequences

- Masking run output stops a command that prints secret values, not one
  that encodes them first: running, like deploying, reaches secrets, and
  the role editor says so.
- Every new check names a permission, and every new MCP tool declares one;
  tests fail otherwise.
- A refusal for a person names the org's admins, as the audit log already
  shows their names to members; a manual token's names nobody.
- `--role read|deploy|admin`, `--scope`, `--project`, `--environment`,
  `dock env lock|unlock` and `dock db policy` keep working on the new
  model.
