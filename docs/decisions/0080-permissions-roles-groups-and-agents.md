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
2. Platform split.
3. The model, shadowed: tables, area roles, default groups, migration,
   both evaluators, the equivalence test.
4. Groups decide: members and groups, the group editor, effective access.
5. Custom roles; MCP tools declare their permission.
6. Agents: personal agents and their settings, org agents, agent rules.
7. Protection.

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
