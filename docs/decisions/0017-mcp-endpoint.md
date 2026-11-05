# 0017: MCP endpoint

Status: accepted (decided by the agent)

## Context

Tempo Dock is built so agents can deploy safely. Today an agent drives it
through `dock ... --json` and exit codes ([agents guide](../guide/agents.md)).
The Model Context Protocol lets clients like Claude Code call typed tools
directly, with descriptions that carry the safety model, so agents need no
shell, no CLI install and no output parsing.

Owner direction: **one** MCP server for the whole platform. Data tools are
part of it ([0016](0016-database-panel.md) phase 5); there is no separate
data MCP.

| Force | Means here |
|---|---|
| Agents can never exceed their token | Every tool goes through core's permission checks with the token's actor, exactly like the API. |
| Idle means idle | No sessions, goroutines or timers while no agent calls. |
| Lightweight | The binary has a 10% growth budget ([performance](../performance.md)). |
| One API | Tools call the same core operations as the API; no logic of their own beyond input defaults, caps and waiting. |

## Decision summary

| Topic | Decision |
|---|---|
| Transport | MCP Streamable HTTP at `POST /api/v1/mcp`, stateless: one JSON-RPC message per request, no session IDs |
| Implementation | A small hand-written server (`internal/mcp`), input schemas and validation from Huma. The official Go SDK is the test client |
| Auth | `Authorization: Bearer` with an API token or a `dock login` token. No cookies |
| Permissions | Core's checks, unchanged: the tool calls core as the token's actor |
| Tools | 60 tools: status, releases (and waiting for one), logs, deploys, stopping and starting, one-off commands, services and their settings, variables, volumes and snapshots, domains, project files, resource use, servers, registry credentials, alerts, audit, access requests, analytics, data. No archive, revoke, lock or token management |
| Resources | The user guide, `dock://docs/<page>`, embedded in the binary |
| Long operations | Deploy-like tools wait for the release on the event bus (default 300 s, at most 900 s), with progress notifications |
| Output | One text block: a one-line summary, a blank line, compact JSON |
| Audit | Core audits every mutating call, actor = the token's name, as for the API |
| Out of scope | Source deploys (`dock deploy`), admin actions, data setup and approval |

## Transport

| Request | Answer |
|---|---|
| `POST` with a request (`initialize`, `ping`, `tools/list`, `tools/call`, `resources/list`, `resources/templates/list`, `resources/read`) | `application/json` response; for a `tools/call` that sends progress, a `text/event-stream` of notifications ending with the response |
| `POST` with a notification or a client response | `202 Accepted` |
| `POST` with a batch | `400`, batches are refused (removed in MCP 2025-06-18) |
| `GET`, `DELETE` | `405`: there are no sessions and no server-to-client stream |

- Protocol versions 2025-11-25, 2025-06-18 and 2025-03-26. `initialize`
  echoes the client's version when supported, else offers 2025-11-25. An
  unsupported `MCP-Protocol-Version` header is a `400`.
- Stateless means every request stands alone: a `tools/call` works without
  an earlier `initialize` on that connection, and the daemon keeps nothing
  per client.
- A foreign `Origin` header is refused (the MCP spec's DNS rebinding
  rule). Bearer tokens already make cross-site calls impossible: browsers
  can't add the header without a CORS preflight, which the daemon never
  allows.
- Request bodies are capped at 4 MB, each stream write has a 15 s deadline.

### Implementation: hand-written, not the SDK

Measured on the full daemon binary (UI embedded, `-s -w`), against the
perf baseline of 40.9 MB (budget 45.0 MB):

| Option | Binary | One `whoami` call (handler) | Verdict |
|---|---|---|---|
| Before MCP | 42.6 MB | | |
| Official Go SDK v1.8.0 (Apache-2.0, older parts MIT) | 45.1 MB (+2.5 MB, over budget) | 170 µs, 387 KB, 394 allocs | Rejected |
| Hand-written subset | 43.0 MB (+0.4 MB) | 40 µs, 15 KB, 232 allocs | **Chosen** |

The SDK's license is fine; its weight isn't. It brings its own JSON
library, JSON Schema engine, OAuth and URI template packages for features
Dock doesn't use (sessions, sampling, elicitation, subscriptions). The
subset Dock needs is about 300 lines. Input schemas come from the tool's
Go input type through Huma (already in the binary, like the API's request
schemas: `doc`, `enum`, `minimum`, required unless `omitempty`, unknown
properties refused), with nested types under `$defs`; arguments are
validated against them before the tool runs.

Tests drive the endpoint with the official SDK's client, so interop with the
reference implementation is checked on every run. Revisit the SDK when Dock
needs what it adds (sessions, server-to-client requests) or when keeping
up with protocol versions costs more than its weight.

## Auth and permissions

| Credential | Accepted | Acts as |
|---|---|---|
| API token (`dock token create`) | yes | The token: its scope, project and environment limits, data policy |
| `dock login` token | yes | The user who approved it, as over the API |
| UI session cookie | no | |

- The token is resolved per request by `core.AuthenticateToken`, the same
  call the API's middleware makes, so revoked or expired tokens stop
  working at once.
- Each tool calls exported core operations with that actor. Core checks
  scope, limits and the data access policy itself (as AGENTS.md
  requires), so the endpoint has no permission logic to get wrong.
- Defaults are convenience, not authority: a tool's project and
  environment default to the token's limits; naming another one is
  checked (and refused) by core.

## Tools

Names are snake_case. Descriptions are written for the model and state the
safety model where it matters (a failed deploy needs no rollback, secret
values are write-only, data changes are proposals). Annotations mark
read-only tools (clients may auto-approve them) and the three destructive
ones.

| Tool | Does | Core operation | Needs |
|---|---|---|---|
| `whoami` | Token name, scope, limits | `Actor.Me` | any |
| `list_projects` | Project names | `ListProjects` | read |
| `project_overview` | Environments, services, state, releases, domains | `Overview` | read |
| `export_project` | The project's live configuration, variable names only | `ExportProject` | read |
| `service_status` | One service's state, active and latest release | `GetService`, `ListReleases` | read |
| `service_stats` | CPU, memory and I/O: latest sample, averages, peaks, at most 24 points | `ServiceStats` | read |
| `list_releases` | Releases, newest first | `ListReleases` | read |
| `release_details` | One release with steps; failed: reason, container and build log tails | `GetRelease`, `ServiceLogs`, `BuildLog` | read |
| `wait_for_release` | Follow a running release (default: the in-flight one) to its end | `ListReleases`, `GetRelease`, `Events` | read |
| `logs` | Last lines of a release's container | `ServiceLogs` | read |
| `deploy_image` | Deploy a prebuilt image, wait | `DeployImage` | deploy |
| `redeploy` | Restart (new identical release), wait | `Restart` | deploy |
| `stop_service` | Stop a service, keeping its release and config | `StopService` | deploy |
| `start_service` | Start a stopped service (new identical release), wait | `StartService` | deploy |
| `run_command` | One-off command in the service's image, wait, last 16 KB of output | `RunCommand` | deploy |
| `list_runs` | A service's latest one-off commands, without output | `ListRuns` | read |
| `rollback` | New release from an earlier live one, wait | `Rollback` | deploy |
| `promote` | Images from one environment to another, wait | `Promote` | deploy on target |
| `clone_environment` | New environment copied from another, optionally deployed, wait | `CloneEnvironment` | deploy on the project |
| `create_service` | New service (or a database template, which deploys) | `CreateService` | deploy |
| `update_service` | Stage settings: port, health check, image, limits, ... | `UpdateService` | deploy |
| `set_pre_deploy_command` | Stage the command run before a release gets traffic | `UpdateService` | deploy |
| `list_volumes` | Volumes, staged ones marked | `ListVolumes` | read |
| `add_volume` | Stage a volume | `AddVolume` | deploy |
| `list_snapshots` | Volume snapshots, restores, retention | `ServiceSnapshots` | read |
| `create_snapshot` | Snapshot a service's volumes now, wait | `CreateSnapshot` | deploy |
| `restore_snapshot` | Restore into a new service (never in place), wait | `RestoreSnapshot` | deploy |
| `plan_project_file` | Compare a project file with the live state | `PlanProject` | read |
| `apply_project_file` | Stage a project file's plan, optionally apply and wait | `ApplyProject` | deploy |
| `import_compose` | Convert a Compose file to a project file and plan it; changes nothing | `ImportCompose`, `PlanProject` | read |
| `list_variables` | Names and pending changes; values only with `reveal` | `ListVariables` | read (admin to reveal) |
| `set_variables` | Stage sets and unsets | `SetVariables` | deploy |
| `show_changes` | Staged changes as a diff, values masked | `GetChanges` | read |
| `apply_changes` | Apply staged changes, wait for the config releases | `ApplyChanges` | deploy |
| `discard_changes` | Drop staged changes (destructive hint) | `DiscardChanges` | deploy |
| `list_domains` | Domains with status and DNS records | `ListDomains` | read |
| `add_domain` | Custom hostname; returns the DNS records to create | `AddDomain` | deploy |
| `generate_domain` | Hostname under the server's base domain | `GenerateDomain` | deploy |
| `check_domain` | Check a domain's verification, DNS and certificate now | `CheckDomainNow` | deploy |
| `remove_domain` | Remove a hostname and Dock's DNS record for it (destructive hint) | `RemoveDomain` | deploy |
| `list_servers` | Servers, status, accepted kinds | `ListServers` | read |
| `list_alerts` | Open alerts, or all with `all` | `ListAlerts` | operator |
| `list_registry_credentials` | Registry hosts and usernames the org pulls with, never passwords | `ListRegistryCredentials` | admin |
| `audit_log` | Audit events | `ListAudit` | read |
| `request_access` | File an access request for a change, with a reason ([0044](0044-environment-lock.md)) | `FileRequest` | read |
| `wait_for_request` | Wait for a request's decision and run | `WaitRequest` | read |
| `list_requests` | The caller's access requests | `ListRequests` | read |
| `analytics_sites` | A project's analytics sites | `ListSites` | read |
| `analytics_stats` | A site's dashboard: totals against the period before, series, top values | `SiteStats` | read |
| `analytics_breakdown` | Top values of one dimension | `SiteBreakdown` | read |
| `analytics_errors` | Uncaught errors grouped, or one group in full | `SiteErrors`, `SiteError` | read |
| `analytics_vitals` | Web Vitals at p75, by dimension and over time | `SiteVitals` | read |
| `analytics_replays` | Session replays, listed only (watched in the UI) | `SiteReplays` | read |
| `analytics_goals` | Goals and who reached them | `SiteGoals` | read |
| `analytics_funnel` | A funnel's steps, or the funnels | `SiteFunnels`, `SiteFunnel` | read |
| `data_status` | Database state and the caller's data access, with why | `DataInfo` | sees the service |
| `data_schema` | Tables, columns, keys; one table in full | `DataSchema` | data `schema` |
| `data_rows` | One keyset page with filters, sensitive columns masked | `DataRows` | data `read` |
| `data_query` | One read-only SQL statement | `DataQuery` | data `read` |
| `propose_data_changes` | Preview, propose, or commit a changeset (destructive hint) | `CreateDataChangeset`, `CommitDataChangeset` | data `propose`; `write` to commit |

`propose_data_changes` commits only when asked, only if core allows the
caller's level (`write`), and never on production from MCP, whatever the
token: there the changeset stays pending for an admin. That is stricter
than the API, where a `dock login` user may commit production with a
confirmation; an agent must not type that confirmation for a person.

Not exposed: archiving projects and services, token, user and server
management, data setup, policy, sensitive columns, approving or
rejecting changesets, revealing sensitive columns, removing volumes,
deleting snapshots and restoring them in place, creating projects and
environments (cloning aside), locking and unlocking environments,
approving access requests, log drains, a service's access password,
adding registry credentials, git connections, backups, alert channels and resolving alerts. Admins do
those in the UI or CLI.

## Resources

Every page of [docs/guide](../guide/README.md) is a resource,
`dock://docs/<page>` (`dock://docs/agents`, `dock://docs/deploying`, ...;
the index is `dock://docs/index`), `text/markdown`, titled by its first
heading and described by its row in the guide index. The pages are
embedded by a Go file in `docs/guide` (`go:embed *.md`), so there is no
copy to keep in sync and an agent always reads the guide of the version it
talks to. Agents use them to learn the platform and to explain it to the
user. The server's `initialize` instructions summarize the safety model
and point at `dock://docs/agents`.

## Long operations

`deploy_image`, `redeploy`, `rollback`, `promote`, `clone_environment`
with deploy, `apply_changes`, and `add_domain`/`generate_domain` when they
redeploy to publish a port; `wait_for_release` from step 2, for a release
already running:

1. Start the release(s) through core, as the API does.
2. Subscribe to the event bus for the environment (`core.Events`, the
   stream behind `/events`), then read each release once, so one that
   finished before the subscription isn't waited for.
3. Each release step (`release_event`) becomes a `notifications/progress`
   message when the call carries a `progressToken`; the response turns
   into an event stream at the first one.
4. Return when every release is `active` or `failed`, or after
   `wait_seconds` (default 300, at most 900, 0 for no wait). Unfinished
   releases say so, and the summary points at `wait_for_release`.
5. Failed releases come with the reason and the last 40 lines of their
   container's output (saved when the release failed) and of their build
   or pull.

No polling: the wait is one bus subscription, dropped when the call ends.
A lagged subscription re-reads the pending releases once. A client that
disconnects cancels the request's context, which ends the wait.

## Output

| Part | Shape |
|---|---|
| Success | One `text` content block: a one-line summary for the person watching and the model, a blank line, compact JSON |
| Data | Compact views: releases without config and image IDs, services as state plus release summaries, domains as `URL (status)` |
| Tool error | `isError: true`, `"<code>: <message>"`, a blank line and `{"error": {"code", "message", "hint"}}` with the API's codes (`forbidden`, `not_found`, `deploy_in_progress`, ...), as the CLI prints them; `deploy_in_progress` adds `release`, the number of the release deploying, as the API's error does |
| Protocol error | JSON-RPC error: unknown method, unknown tool, unknown resource |

Errors core reports go back as tool errors, so the model reads them and the
hints say what to do (stop on `forbidden`, `wait_for_release` on
`deploy_in_progress`, look before retrying a `conflict`, back off on
`rate_limited`, ...).
Invalid arguments are tool errors too, naming the field but never echoing
its value (it may be a secret).

## Limits

| What | Default | Max |
|---|---|---|
| `logs` lines | 100 | 1000 |
| `release_details` log lines (per log) | 40 | 500 |
| `list_releases` | 10 | 100 |
| `audit_log` events | 50 | 500 |
| `list_alerts` with `all` | 50 | 500 |
| `service_stats` points | | 24 (averaged) |
| `data_rows` page | 50 rows | 200 rows |
| `data_query` rows | 100 | 1000 |
| `data_rows`, `data_query` result | | 512 KB of rows, then `truncated` |
| Cells | | 8 KB (core's cap) |
| SQL statement timeout | 10 s | 60 s |
| Wait for releases | 300 s | 900 s |
| Request body | | 4 MB |

## Audit

Nothing new: core writes an audit event for every mutating operation with
the actor's name, so a deploy through MCP is `release.deploy` by
`claude-shop-staging`, like one through the CLI. Data reads are audited as
0016 says (`data.query` per statement, `data.browse` throttled).

## Performance

| Concern | Rule |
|---|---|
| Idle | No goroutines, timers or subscriptions. The tool table is built on the first request. |
| Per call | One auth lookup, one decode, the core calls; no session state. `BenchmarkToolCall` (handler only): `whoami` 40 µs and 15 KB, `service_status` 130 µs and 43 KB. Over loopback HTTP with the SDK client, p50 0.17 ms and 0.30 ms. |
| Waiting | One event bus subscription per waiting call, gone when it returns. A test checks no goroutine of the endpoint remains after calls. |
| Binary | +0.4 MB. |

## Client config

`dock mcp` prints the snippet for the current context; the token is
`${DOCK_TOKEN}` unless `--show-token`:

```sh
claude mcp add --transport http --scope project dock https://dock.example.com/api/v1/mcp \
  --header 'Authorization: Bearer ${DOCK_TOKEN}'
```

```json
{
  "mcpServers": {
    "dock": {
      "type": "http",
      "url": "https://dock.example.com/api/v1/mcp",
      "headers": { "Authorization": "Bearer ${DOCK_TOKEN}" }
    }
  }
}
```

## Consequences

- Agents get the platform as tools, with the same limits as the API and no
  new trust: an MCP call can do exactly what the token can over HTTP.
- No idle cost, 0.4 MB of binary.
- The endpoint follows MCP versions by hand. The subset is small and
  versions are negotiated, so older ones keep working while newer ones are
  added.
- Source deploys stay in the CLI: uploading a build context through
  JSON-RPC would buffer it, and agents that have the source have a shell.
- The OpenAPI spec doesn't list the endpoint; it isn't REST.

## Alternatives considered

| Alternative | Why not |
|---|---|
| Official Go SDK | Over the binary budget for unused features (see above). |
| A separate data MCP server | Owner decision ([0016](0016-database-panel.md)): one Dock MCP, data included. |
| MCP as a local stdio process wrapping the CLI | Every client machine needs the CLI; a second code path to keep in sync; nothing gained over HTTP with a token. |
| Stateful sessions with server-to-client streams | State per client in the daemon for features no tool needs. |
| Return at once and let the agent poll | Agents poll badly and burn context; waiting on the bus is cheaper for both sides. |

## Amendment (2026-11-04): waiting for a running release, fixing services

Decided by the agent.

Two gaps showed in use:

- **Nothing waited on a release that was already running.** When a tool's
  wait ran out, or a deploy was refused with `deploy_in_progress`, the
  summary and hint pointed at `release_details`, which answers at once.
  The agent was left to poll it, which the Alternatives table rejects.
  `wait_for_release` follows a running release (default: the service's
  in-flight one) exactly like the deploy tools: one bus subscription,
  progress notifications, the final state with the failure logs. With
  nothing in flight it returns the latest release's state at once. It is
  read only: it changes nothing, so a `read` token may wait too.
  `deploy_in_progress` errors now carry the deploying release's number as
  `release`, in the tool error and the API's problem response alike.
- **The most common failure couldn't be fixed over MCP.** A health check
  that fails on a wrong port or path needs a settings change; the agent
  could read the reason but not act on it. `update_service` and
  `add_volume` stage like `set_variables` (review with `show_changes`,
  then `apply_changes`); `create_service`, `remove_domain` (destructive
  hint), `list_volumes`, `service_stats` and `list_alerts` (operators)
  round it out. All call core with the token's actor, so their limits
  are the API's, and a locked environment refuses them and files an
  access request like any other change.

Hints now cover every code a tool can return: `validation`, `conflict`,
`hostname_taken`, `rate_limited`, `too_large` and `internal` too, and
`environment_locked` says to wait for the request it filed (or ask a
human to unlock) and never to look for another token.

Left out on purpose: locking and unlocking (a token must not loosen what
a person set), log drains (they send logs off the server), deleting
snapshots and in-place restores (they throw data away), a service's
access password (a credential a person chooses).
