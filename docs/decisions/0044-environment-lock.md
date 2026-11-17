# 0044: Locked environments, and agents asking for access

Status: accepted (asked by Saile, 2026-11-03; designed by the agent); superseded
in part by [0080](0080-permissions-roles-groups-and-agents.md) (approvers, not only admins, decide requests and loosen a
lock; `--people` holds people's changes too). Amended 2026-11-17: the web
UI's lock bar defaults to a full freeze, people's changes held too (blind
reviewers read "lock" as a code freeze); `dock environment lock` keeps its default.

## Context

Saile: "it should be possible to lock / unlock an environment. It does not
freeze it fully, but it stops the agent from doing destructive things in
it. For a prod environment which is released and users use it, I don't
want an agent to be able to do changes to it. But the agent can request
access (so the user has to go in the UI, see what the agent wants to do,
then approve or reject the action). Or it can be unlocked and the agent
can do whatever." The roadmap's "human approval for selected actions" and
"production deploy approval" are the same thing.

| Force | Means here |
|---|---|
| One check | Every new action (from any thread, API, CLI or MCP) must be covered without remembering to. Core already has one place every environment operation passes: `Actor.checkEnv` |
| Agents can't tell us they're agents | Claude Code running `dock` in Saile's shell uses Saile's `dock login` token. The daemon sees a token, not a person |
| Safe by default | When in doubt, the lock holds and a human decides |
| Exactly what will happen | The approver sees the call itself and its arguments, not a paraphrase |
| Idle means idle | No timers for the lock or for expiry; nothing polls |

## Decision

| Topic | Decision |
|---|---|
| State | An environment is unlocked or locked (`environments.locked_at`, `locked_by`, `lock_reason`). A locked environment can be opened for a while (`open_until`): it counts as unlocked until then and locks again by itself, computed at check time |
| Who it applies to | Every token-authenticated caller: API tokens, `dock login` tokens (also a human's own CLI) and MCP. Humans in a UI session and the daemon itself are unaffected. A `dock login` token isn't exempt because agents run with it; its owner approves their own CLI's requests in the UI in one click, or opens the environment for a while |
| What it blocks | Every operation that needs `deploy` or `admin` in that environment: deploys, rollbacks, restarts, promote into it, variables, settings, apply and discard, domains, volumes, backups and restores, archive, unlock. Reads (`read` scope) stay allowed. Archiving a project, which changes every environment, is blocked while any of its environments is locked |
| Where | `checkEnv`, after scope and limits: the same call every environment operation already makes. New operations are covered by using `envFor`, `serviceFor` or `checkEnv`, which `internal/core/AGENTS.md` requires |
| Data panel | Within the existing policy, a token's level in a locked environment is capped at `propose`: an agent drafts a changeset and an admin approves it in the Data panel. That flow already is the access request for data |
| Locking | Admins (a UI session or an admin token: locking only takes power away). `dock env lock [--reason]`, the environment header in the UI. Audited `environment.lock` |
| Unlocking | Admins in a UI session. A token asking to unlock gets an access request like any other blocked action. `dock env unlock [--for 30m]`; the UI offers unlock and "open for 15 min / 1 h / 4 h". Audited `environment.unlock` |
| Default | An org setting, "lock new production environments", off by default (on would break existing agent workflows the day they upgrade). The setting is in Settings › General and `dock org locks --lock-production on\|off`, with the requests' expiry (`--request-ttl`) |
| Shown | The environment header, the switcher, project overview, `dock status`, `dock env list`, MCP `project_overview` |

### Access requests

| Topic | Decision |
|---|---|
| When | A token's action is blocked by a lock: core records a pending request in the same call and the error names it. Identical pending requests (same token, same call, same arguments) are one request |
| What it holds | The exact call, replayable: for the API the operation ID and its decoded input, for MCP the tool name and its arguments, sealed with the secrets box (they can hold variable values). Plus a one-line summary ("token claude wants to set variables in shop/production"), the requester, an optional reason, the environment's audit position at that moment, and when it expires |
| Source deploys | `dock deploy` into a locked environment uploads anyway: the archive is checked and kept (`<data>/requests/<id>.tar`, one pending source deploy per service, a newer one replaces it) and approval deploys it. Nothing is built before approval |
| Push deploys | A GitHub push into a locked environment waits for approval: the push becomes a request naming the commit (a newer push replaces it). This is the production deploy approval. A human's Deploy in the UI is not held |
| Reason | `--reason "..."` on any CLI command (the `Dock-Reason` header), `reason` on MCP's `request_access` |
| Error | Code `environment_locked`, HTTP 423 Locked, `request` with the request ID; the message says it waits for approval and how to follow it. MCP's tool error carries the same code and `request` |
| Asking first | MCP `request_access {tool, arguments, reason}` files a request for a mutating tool call without running it, locked or not; the tool's checks still run up to the point where it would change something |
| Waiting | `dock request wait <id>` and MCP `wait_for_request` block until it is decided (at most 15 minutes per call, on the event bus); `dock request list/show`, MCP `list_requests` |
| Deciding | Admins of the org in a UI session, at Requests (`/<org>/requests`, a badge in the nav, live over SSE). Approve or Reject, with an optional comment. Tokens can never decide, so the CLI has no approve |
| Approve runs it | The stored call runs again as the requesting token, through every check it would pass today (scope, limits, validation), with the lock lifted for that one call. The token is loaded afresh: revoked, expired or demoted, the run fails. The action's audit event names the token and adds "approved by <user>, request #N"; the decision is audited `request.approve` / `request.reject` |
| Changed since | The detail shows how many changes the environment had since the request; a call that no longer applies fails its own validation (a release that is gone, a deploy in progress) and the request ends `failed` with the error. Nothing is half-done: the run is the same operation, with its own transaction |
| States | `pending` → `approved` (running) → `done` or `failed`; `pending` → `rejected` or `expired`. A run interrupted by a restart ends `failed` |
| Unlocking ends them | Unlocking an environment, or opening it for a while, expires its pending requests with the comment "the environment was unlocked, so the call needs no approval now: call again", and resolves their alerts: the tokens make their calls themselves (a held push is pushed again, or deployed from the UI). Approving one after the unlock would run a call nobody needs to review |
| Expiry | Org setting, default 24 hours, 1 hour to 30 days. A pending request past it counts as expired when read; the daily retention pass marks them and removes kept archives |
| Alerts | A new request raises an `access_request` alert (warning) through the alert channels, so it reaches a phone; deciding or expiring it resolves it |
| Who sees requests | The org's admins, and the token that asked (its own). Arguments only for admins |

### UI

Mocks (2026-11-04): three for the environment's lock state, three for
reviewing a request. Saile wasn't watching live, so the agent picked:

| Screen | Pick | Why |
|---|---|---|
| Environment header | B: a strip under the environment's header while it is locked ("Locked by saile, 3 days ago · reason", the requests waiting there, Open for… and Unlock) | Nobody misses it on the one environment that matters, it says who and why, and it only exists while locked, so other environments look as before. A: one more phrase in an already busy header line. C: a glyph alone hides the reason |
| Request review | A: an inbox, the list beside the detail of the selected request | The list stays in view while deciding; the detail shows the exact call, its arguments, what changed since and both decisions beside a comment, so several requests go in a row without page loads. B: rows grow unreadable with real arguments. C: a page per request costs a navigation per decision |

## Why this shape

- **Replaying the call, not modelling each action.** A typed request per
  operation would have to be written for every action, and every new one
  would be a gap. Storing the operation's decoded input and running the
  same handler again covers every current and future API operation and
  MCP tool, and the approver sees exactly the arguments that will run.
- **The lock lives in `checkEnv`.** It already runs for every environment
  operation with the environment row loaded, so the lock costs no query
  and no handler can forget it.
- **All tokens, not "agents".** The data panel treats a `dock login`
  token as its user, which is right for data policy. For a lock it would
  leave the most common agent setup (an agent in the owner's shell)
  unguarded. Approving one's own request is one click.
- **Tokens can't escape it through accounts.** Tokens never create,
  reset, enable, disable or delete user accounts, locked or not
  (`requireAccountManager`): a person does that from a signed-in session.
  While any environment of an organization is locked, its tokens also
  can't invite members, change roles or create admin or operator tokens
  (`checkAccountLock`); a person in the web UI does those. Revoking,
  removing members and deploy or read tokens
  stay open. The lock still guards against an agent's mistakes and
  overreach, not every hostile admin token path: give agents `deploy`
  tokens limited to the environments they work in.

## Consequences

- Every new environment operation must go through `envFor`, `serviceFor`
  or `checkEnv`; one that skips them skips the lock, and the permission
  matrix test lists every mutating operation.
- A call's input type must survive JSON (Huma inputs and MCP arguments
  do). A source upload is the one exception, kept as a file.
- Requests are kept with the audit log's retention.
