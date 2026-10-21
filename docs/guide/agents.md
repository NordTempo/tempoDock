# Agents

How to let an AI agent (Claude Code, a CI job, any bot) deploy with `dock`
without being able to break or destroy things.

The model: the agent gets a **scoped API token**, never SSH or Docker access.
Every rule is enforced by the daemon, not by the agent behaving. The worst
outcome of a bad deploy is a failed release that never got traffic.

## 1. Create a token

As an admin, with an unlimited admin token (the one from install):

```sh
dock token create claude-shop-staging \
  --scope deploy --project shop --environment staging --expires 30d --json
```

With `--json` it prints `{"token": {...}, "secret": "dock_..."}`. The secret
is shown once. Pick the narrowest fit:

| Scope | Use for |
|---|---|
| `read` | Monitoring and triage bots: status, logs, releases, variable names |
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
| 5 | `conflict` | `deploy_in_progress`: wait 30 s and retry, up to 3 times. `hostname_taken`: stop and report |
| 6 | `deploy_failed` | Build or health check failed. **Traffic was not switched; don't roll back.** Read logs, fix, deploy again |
| 7 | `unreachable` | Retry with backoff (10 s, 30 s, 60 s), then stop and report |
| 8 | `incompatible` | Stop. CLI and server versions differ |

## What a `deploy` token can and can't do

| Can | Can't |
|---|---|
| Deploy source or images, roll back, restart | Archive services (`admin`) |
| Create services in its environment | Create projects (needs an unlimited token); create environments when limited to one |
| Set, unset and apply variables | Read secret values, even in the staged-changes diff |
| Add, generate and remove domains | Manage tokens or users |
| Add volumes | Delete volumes (nothing deletes volumes as a side effect) |
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
(`number`, `status`, `error_code`, ...), also when it failed.

A failed deploy needs no rollback. Rollback is for a release that went live
but misbehaves:

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
    5|7) sleep $((attempt * 20)) ;;   # deploy in progress, or server unreachable
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
  - 5 with code `deploy_in_progress`: wait 30 s, retry up to 3 times.
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

Deploy an image built in CI; the server only pulls it. The server needs pull
access to the registry.

```yaml
# .github/workflows/deploy.yml (excerpt)
- name: Deploy to staging
  env:
    DOCK_URL: ${{ secrets.DOCK_URL }}
    DOCK_TOKEN: ${{ secrets.DOCK_TOKEN }}
  run: |
    dock deploy --image ghcr.io/acme/web:${{ github.sha }} \
      --project shop --environment staging --service web --json
```

The runner must reach the API: join it to the tailnet (e.g.
`tailscale/github-action`) rather than exposing the API port.
<!-- verify: how CI gets the dock binary (no published releases yet), and how the server authenticates to private registries (not implemented as of 56c9cfa). -->
