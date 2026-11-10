# Agents

How to let an AI agent (Claude Code, a CI job, any bot) deploy with `dock`
without being able to break or destroy things.

The model: the agent gets a **scoped API token**, never SSH or Docker access.
`dock login` in the browser is for people at a terminal; it gives the CLI
your own, unlimited access. Agents get their own token, limited to what
they deploy.
Every rule is enforced by the daemon, not by the agent behaving. The worst
outcome of a bad deploy is a failed release that never got traffic.

## 1. Create a token

As an admin of the organization, with an unlimited admin token (the one
from install). The token belongs to that organization and never acts in
another:

```sh
dock token create claude-shop-staging \
  --scope deploy --project shop --environment staging --expires 30d --json
```

With `--json` it prints `{"token": {...}, "secret": "dock_..."}`. The secret
is shown once. Pick the narrowest fit:

| Scope | Use for |
|---|---|
| `read` | Monitoring and triage bots: status, logs, releases, resource use, variable names |
| `deploy` | Agents that ship: deploy, roll back, restart, variables, domains |
| `admin` | Humans only. Never give an agent `admin` |

| Limit | Token can touch |
|---|---|
| none | Every project |
| `--project shop` | All environments of `shop`, and create new ones |
| `--project shop --environment staging` | Only `shop/staging` (recommended for agents) |

Tokens are stored hashed, show `last_used_at`, and can be revoked any time:
`dock token list`, `dock token revoke <id>`.

## 2. Give it to the agent

```sh
export DOCK_URL=http://100.64.0.10:7070
export DOCK_TOKEN=dock_...
```

With `DOCK_URL` set, contexts and the config file are ignored entirely, so a
fresh sandbox needs nothing else. Keep the token in the sandbox's or CI's
secret store, not in the repo. The agent's machine must reach the API (e.g.
on the tailnet).

Check what the token can do:

```sh
curl -fsS -H "Authorization: Bearer $DOCK_TOKEN" "$DOCK_URL/api/v1/me"
# {"kind":"token","name":"claude-shop-staging","scope":"deploy","project":"shop","environment":"staging"}
```

Tell commands where to deploy with flags, or `dock link` once in a
persistent workspace:

```sh
dock deploy --project shop --environment staging --service web --json
```

Every command that acts on a service takes `--project/-p`,
`--environment/-e` and `--service/-s`. A directory's link still supplies
them when `DOCK_URL` is set; only its context (server) is ignored.

## 3. Output and exit codes

- **Always pass `--json`.** stdout is then exactly one JSON value; fields are
  only ever added, never renamed or removed. Tokens are never printed.
- **Errors** are one JSON object on stderr:

  ```json
  {"error": {"code": "health_check_failed", "exitCode": 6, "message": "...", "hint": "..."}}
  ```

  `code` is the server's reason when it gave one (`deploy_in_progress`,
  `hostname_taken`, `build_failed`, ...), otherwise the exit code's name.
- **Never prompts** when stdin isn't a terminal. Missing input is exit code 2
  with the flag to pass. `--yes` skips confirmations, never permission checks.

| Exit | Name | What the agent should do |
|---|---|---|
| 0 | `ok` | Continue (verify the deploy, below) |
| 1 | `error` | Stop and report the message |
| 2 | `usage` | Fix the command using `hint`. Nothing was done |
| 3 | `auth` | Stop. Missing token or the token lacks the scope. Don't look for another credential |
| 4 | `not_found` | Check names with `dock project list`, `dock service list`. Don't create things to make it pass unless asked |
| 5 | `conflict` | `deploy_in_progress`: run `dock release wait`, which follows the running release until it is done, then retry. `environment_locked`: tell the user, then `dock request wait <id>` ([below](#locked-environments-and-access-requests)). `hostname_taken`: stop and report |
| 6 | `deploy_failed` | Build or health check failed. **Traffic was not switched; don't roll back.** Read logs, fix, deploy again |
| 7 | `unreachable` | Retry with backoff (10 s, 30 s, 60 s), then stop and report |
| 8 | `incompatible` | Stop. CLI and server versions differ |

## MCP

The daemon is also an MCP server, so agents like Claude Code can operate
Dock through tools instead of the CLI: no install, no output parsing. It is
one endpoint for the whole platform, data included, at `/api/v1/mcp`
(Streamable HTTP). It takes the same scoped token, and every tool goes
through the same permission checks and audit as the API: a tool can do
exactly what the token can. Design: [ADR 0017](../decisions/0017-mcp-endpoint.md).

`dock mcp` prints the config for your current context. In the repository,
with the agent's token in `DOCK_TOKEN`:

```sh
claude mcp add --transport http --scope project dock https://dock.example.com/api/v1/mcp \
  --header 'Authorization: Bearer ${DOCK_TOKEN}'
```

That writes `.mcp.json`, which other clients read too:

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

Claude Code fills in `${DOCK_TOKEN}` from the environment, so the file can
be committed. `dock mcp --show-token` prints the context's own token
instead; that is your login, so don't hand it to an agent.

| Tools | |
|---|---|
| Look | `whoami`, `list_projects`, `project_overview`, `export_project`, `service_status`, `service_stats`, `list_releases`, `release_details`, `diagnose`, `logs`, `list_servers`, `list_alerts` (the org's; `platform` for operators), `status_pages`, `list_errors`, `list_vulnerabilities` (what is vulnerable in live images and the fix), `list_registry_credentials` (admin; hosts and usernames only), `audit_log` |
| Ship | `deploy_image`, `redeploy`, `stop_service`, `start_service`, `rollback`, `promote`, `clone_environment`, `wait_for_release`, `run_command`, `list_runs` |
| Jobs | `list_jobs`, `set_job`, `run_job`, `remove_job`, `job_history` |
| Previews | `get_previews`, `get_preview` |
| Configure | `create_service`, `update_service`, `set_pre_deploy_command`, `list_variables`, `set_variables`, `show_changes`, `apply_changes`, `discard_changes`, `list_domains`, `add_domain`, `generate_domain`, `check_domain`, `remove_domain` |
| Volumes | `list_volumes`, `add_volume`, `list_snapshots`, `create_snapshot`, `restore_snapshot` (into a new service only) |
| Project files | `plan_project_file`, `apply_project_file`, `import_compose` |
| Analytics | `analytics_sites`, `analytics_stats`, `analytics_breakdown`, `analytics_errors`, `analytics_vitals`, `analytics_replays` (listed only; watched in the UI), `analytics_goals`, `analytics_funnel` |
| Data | `data_status`, `data_schema`, `data_rows`, `data_keys` and `data_key` (Redis), `data_query`, `data_insights`, `data_cancel`, `propose_data_changes` |
| Ask | `request_access`, `wait_for_request`, `list_requests` |

- **Project and environment default to the token's limits**, so a token
  for `shop/staging` only names the service. `whoami` shows them.
- **Deploy tools wait** until the release is active or failed (default
  300 seconds, `wait_seconds` up to 900) and stream each step as a
  progress notification. A failed release comes back with its reason and
  the last lines of its container's output and build, so the agent can fix
  it without rolling back. When a wait runs out, or someone else's release
  is deploying, `wait_for_release` follows it the same way; there is no
  need to poll `release_details`.
- **Fixing a failed health check**: `update_service` stages a new `port`
  or `health_check_path` (and the other settings), `show_changes` reviews
  it and `apply_changes` deploys it, health checked like any release.
- **Logs** can be filtered: the `logs` tool takes `grep` (a plain,
  case-sensitive substring) and `stderr` (stderr lines only), applied
  after `tail`, like `dock logs --grep` and `--stderr`.
- **Errors are tool results** with the API's codes and a hint:
  `forbidden` means stop and report, `deploy_in_progress` means call
  `wait_for_release`, then retry. Its `release` field (in the API's error
  too) names the release that is deploying.
- **Data** follows the [data access policy](data.md): tokens get nothing
  until an admin grants it. `propose_data_changes` stores a changeset for
  an admin to approve, and commits it only on non-production with `write`
  access; never on production.
- **Docs**: every guide page is a resource, `dock://docs/<page>`
  (`dock://docs/agents` is this page), so an agent can read how Dock works
  and explain it to you.
- **Locked environments** refuse every change with `environment_locked`
  and file an access request (below); `wait_for_request` waits for the
  decision. `request_access` asks first, with a reason, anywhere.
- **Not over MCP**, each for a reason:
  - Source deploys: use `dock deploy`; uploading a build context through
    a tool call would buffer it.
  - Archiving services and projects, removing volumes, deleting
    snapshots, in-place restores: they throw data away, so a person does
    them.
  - Locking and unlocking environments, approving access requests: a
    token must not loosen what a person set.
  - Log drains: they send logs off the server.
  - A service's access password, registry credentials, tokens, users:
    credentials a person chooses.
  - Creating projects and environments (`clone_environment` and
    `apply_project_file` aside), git connections, backups, alert channels
    and resolving alerts, servers, data setup: platform setup for admins.

## Locked environments and access requests

An admin can lock an environment, typically production once people use it
(`dock environment lock production`, or Lock on the project page). Tokens
can still read everything there, but every change they try (deploys,
rollbacks, restarts, variables, settings, domains, volumes) fails with
`environment_locked` (HTTP 423, exit code 5) and becomes an **access
request**:

```text
$ dock env set PRICE=42 --reason "launch prices"
error: environment shop/production is locked: setting variables waits for an admin's approval as access request #12. ...
$ dock request wait 12
```

- The request holds the exact call and its arguments. An admin sees it in
  the web UI under Requests (and gets an alert), and approves or rejects
  it with a comment. Approved, it runs as your token, through every check
  it would pass then, and the audit log names both of you.
- `--reason` on any command (MCP: `request_access`'s `reason`) tells the
  admin why. Retrying the same call joins the same request; don't retry.
- `dock request list|show|wait`, MCP `list_requests` and
  `wait_for_request` follow it: `wait` exits 0 when it ran, 1 when it
  failed, 3 when rejected, 4 when it expired (after 24 hours by default).
- `dock deploy` into a locked environment uploads anyway: the archive is
  checked and kept, and deployed once approved. A newer upload of the
  service replaces the one waiting. Pushes to a connected GitHub branch
  wait the same way.
- Tokens never approve, not even admin tokens and not their own requests.
  An admin can also open the environment for a while instead.
- Data panel changes in a locked environment are changesets for an admin
  to approve, as on production.

## What a `deploy` token can and can't do

| Can | Can't |
|---|---|
| Deploy source or images, roll back, restart | Archive services (`admin`) |
| Run one-off commands in a service's image (`dock run`, MCP `run_command`) with its variables, audited with the exit code | Run anything on the host itself |
| Create services in its environment | Create projects (needs an unlimited token); create environments when limited to one |
| Set, unset and apply variables | Read secret values, even in the staged-changes diff |
| Add, generate and remove domains | Manage tokens or users |
| Change service settings and add volumes (both staged) | Delete volumes (nothing deletes volumes as a side effect) |
| Clone an environment (when not limited to one): references and new template credentials come along | Copy secret values into the clone; they are listed for someone to set |
| Read status, releases, logs, audit events of its environment | See or touch any other project or environment |
| | Reach Docker, BuildKit, Caddy or the host. Deploy over the platform itself |

Within its environment a `deploy` token can still do harm that passes a
health check: remove a domain, or set a wrong variable. So:

- Limit agents to non-production environments; promote or deploy production
  yourself.
- Give services a health check path that checks their dependencies.
- Review `dock audit --json`: every change is recorded with the token's name.

## Deploy, verify, roll back

`dock deploy` waits until the release is `active` or `failed` and streams
progress to stderr. With `--json` it then prints the final release
(`number`, `status`, `error_code`, ...), also when it failed. To follow a
release that is already running (another deploy, or one whose wait was
cut off), `dock release wait [N]` waits until it is done and exits the
same way.

A failed deploy needs no rollback. To learn why it failed (or why the live
release crash loops or turned unhealthy), `dock diagnose --json` (MCP
`diagnose`) returns one bundle: the likely cause with its fix
(`cause.rule`, `cause.summary`, `cause.fix.cli`; an empty rule means no
rule matched), what was ruled out (`checked`), and the evidence. Rollback
is for a release that went live but misbehaves:

```sh
#!/usr/bin/env bash
# Deploy the linked service, smoke-test it, roll back if the smoke test fails.
set -uo pipefail
check_url="https://web-shop-staging.apps.example.com/healthz"

# The release serving now, to roll back to.
prev=$(dock releases --json | jq -r 'map(select(.status == "active"))[0].number // empty')

for attempt in 1 2 3; do
  dock deploy --json >release.json 2>error.json
  code=$?
  case $code in
    0) break ;;
    5) dock release wait >/dev/null 2>&1 ;;  # another deploy in progress: follow it to its end
    7) sleep $((attempt * 20)) ;;           # server unreachable
    6) jq . error.json; exit 6 ;;     # build/health check failed; previous release still serving
    *) jq . error.json; exit "$code" ;;
  esac
done
[ "$code" -eq 0 ] || exit "$code"

number=$(jq -r .number release.json)
for _ in $(seq 10); do
  if curl -fsS --max-time 5 "$check_url" >/dev/null; then
    echo "release $number is live"; exit 0
  fi
  sleep 3
done

echo "release $number failed the smoke test; rolling back to $prev" >&2
[ -n "$prev" ] && dock rollback "$prev" --json
exit 1
```

After an exit 6, the reason is in the error's `message` and the release's
`error`. Build output: `dock logs --build`; app output of the failed release:
`dock logs --release <number>`.

## Instructions for the agent

Paste into the repo's `CLAUDE.md` (or `AGENTS.md`), adjusted to your names:

```md
## Deploying (Tempo Dock)

This repo deploys with the `dock` CLI. `DOCK_URL` and `DOCK_TOKEN` are set.
The token can deploy `shop/staging` only.

- Always pass `--json`. Parse stdout. Errors are one JSON object on stderr:
  `{"error": {"code", "exitCode", "message", "hint"}}`.
- Deploy: `dock deploy --project shop --environment staging --service web --json`.
  It waits until the release is live or failed.
- Branch on the exit code:
  - 0: deployed. Verify it (below).
  - 5 with code `deploy_in_progress`: run `dock release wait --json` (it
    follows the running release until it is done), then deploy again.
  - 6: build or health check failed. Nothing changed; the previous release
    still serves. Do not roll back. Read `dock logs --build` and
    `dock logs --release <number>`, fix, deploy again.
  - 7: server unreachable. Retry with backoff, then stop and report.
  - 2, 3, 4, 8 or 1: stop and report the error and its `hint`. Don't work
    around it.
- Verify after 0: `curl -fsS https://web-shop-staging.apps.example.com/healthz`
  and look for errors in `dock logs --since 2m --json`.
- If verification fails: `dock rollback <number of the previously active release> --json`,
  then report what happened.
- Variables: `dock env set KEY=value --json` stages a change; the next deploy
  takes it live. Secret values are write-only for you; never try to print them.
- Never ask for an admin token, use SSH or Docker on the server, or deploy
  to production.
```

## CI

Deploy an image built in CI; the server only pulls it. For a private
registry, an admin adds the organization's credentials for its host once
(`dock registry login ghcr.io --username NAME --password-stdin`, see
[private registries](deploying.md#private-registries)); deploys pull with
them.

The runner needs the CLI: download `dock-cli-linux-amd64` from the release
the server runs (`dock version` shows it) and check it against
`SHA256SUMS`. The repository is private, so `gh` needs a token that can read
its releases (a fine-grained token with read access to its contents), not
the workflow's own `GITHUB_TOKEN`. The deploy itself only uses `DOCK_URL`
and `DOCK_TOKEN`.

```yaml
# .github/workflows/deploy.yml (excerpt)
- name: Install dock
  env:
    GH_TOKEN: ${{ secrets.DOCK_RELEASES_TOKEN }}
  run: |
    gh release download v0.1.0 -R NordTempo/tempoDock -p dock-cli-linux-amd64 -p SHA256SUMS
    grep ' dock-cli-linux-amd64$' SHA256SUMS | sha256sum --check
    sudo install -m 0755 dock-cli-linux-amd64 /usr/local/bin/dock
- name: Deploy to staging
  env:
    DOCK_URL: ${{ secrets.DOCK_URL }}
    DOCK_TOKEN: ${{ secrets.DOCK_TOKEN }}
  run: |
    dock deploy --image ghcr.io/acme/web:${{ github.sha }} \
      --project shop --environment staging --service web --json
```

The runner must reach the API: use Dock's [public address](install.md#public-address)
as `DOCK_URL`, or join the runner to the tailnet (e.g.
`tailscale/github-action`). Never expose the API port itself.
