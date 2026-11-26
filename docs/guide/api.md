# API

The CLI and UI are thin clients of one HTTP API under `/api/v1`, on the
daemon's `listen` address. Anything they do, you can do with `curl`.

| Path | What |
|---|---|
| `/api/v1/openapi.json`, `/api/v1/openapi.yaml` | OpenAPI 3.1 spec (`openapi-3.0.json` / `.yaml` for older tools) |
| `/api/v1/schemas/{Name}.json` | JSON Schema of one request or response body |
| `/api/v1/health`, `/api/v1/version` | Daemon health and version |

These need no auth; everything else does. `/api/v1/mcp` is the MCP
endpoint for agents (JSON-RPC, not in the spec; see
[agents](agents.md#mcp)). `GET .../services/{s}/tunnel` carries one
`dock connect` connection: an HTTP/1.1 upgrade to `dock-tunnel` with a
bearer token, then raw bytes ([ADR 0023](../decisions/0023-connect-tunnels.md)).

The spec is also printed offline by `dock openapi` (hidden command). The
daemon serves no docs page of its own: point an OpenAPI viewer at the
spec.

## Auth

| Method | Header | Used by |
|---|---|---|
| API token | `Authorization: Bearer dock_...` | CLI, agents, CI, scripts |
| Session cookie | `__Host-dock_session` over HTTPS, `dock_session` over plain HTTP (HttpOnly, SameSite=Strict), from `POST /api/v1/auth/login` | The web UI |

Every operation checks the caller's scope (`read` < `deploy` < `admin`) and
project/environment limits. `GET /api/v1/me` shows who you are:

```json
{"kind": "token", "name": "ci", "scope": "deploy", "project": "shop", "environment": "staging"}
```

Sessions last 30 days. Tokens last until revoked or their expiry.

## Resources

Addressed by name. Names are unique within their parent. Only projects can
be renamed ([below](#project-identity-and-renaming)); the full list of paths
is in the spec.

```
/api/v1/projects/{project}
  /environments/{environment}
    /changes, /changes/apply, /changes/discard
    /variables
    /log-drains, /log-drains/{id}[/test]
    /services/{service}
      /releases, /releases/source, /releases/{number}[/rollback|/build-log]
      /restart, /start, /stop, /archive, /logs, /volumes, /runs, /jobs
      /domains, /domains/generate, /domains/{hostname}[/check]
/api/v1/tokens, /users, /audit, /servers, /requests, /alerts, /events
```

Deploy, rollback and restart return `202` with the queued release right away.
Follow `/events` or poll `GET .../releases/{number}` until `status` is
`active` or `failed`.

## Errors

RFC 9457 (formerly 7807) problem documents, `Content-Type:
application/problem+json`, with a machine-readable `code`:

```json
{
  "title": "Forbidden",
  "status": 403,
  "detail": "token \"ci\" has scope deploy; archiving a service requires admin",
  "code": "forbidden"
}
```

Validation errors add `errors: [{"message", "location"}]`.

| `code` | HTTP | Means |
|---|---|---|
| `validation` | 400, 422 | Bad input. Nothing was done |
| `unauthorized` | 401 | No or invalid token or session |
| `forbidden` | 403 | Scope or project/environment limit too narrow; `detail` says which |
| `not_found` | 404 | No such project, environment, service, release, domain |
| `conflict` | 409 | Already exists |
| `deploy_in_progress` | 409 | Another deploy of the service is running |
| `hostname_taken` | 409 | The hostname belongs to another service |
| `too_large` | 413 | Source upload over `max_upload_mb` |
| `rate_limited` | 429 | Too many attempts (logins); wait and retry |
| `server_offline` | 503 | The server the operation needs isn't connected |
| `reauth_required`, `two_factor_required`, `sso_required` | 403 | Confirm who you are again (`POST /api/v1/me/reauth`), or sign in with a second factor or through the identity provider, then retry ([two-factor](two-factor.md)) |
| `environment_locked` | 423 | A token's change in a locked environment; `request` names the [access request](agents.md#locked-environments-and-access-requests) it became |
| `internal` | 500 | See the daemon log (`journalctl -u dock`) |

A failed release is not an HTTP error: the release has `status: "failed"`,
`error`, and `error_code` (`build_failed`, `health_check_failed`,
`deploy_failed`).

## Event stream

`GET /api/v1/events` is Server-Sent Events of everything the caller may see.
Narrow it with `?project=`, `&environment=` (needs project) and `&service=`
(needs environment).

```
event: release
data: {"type":"release","time":"2026-10-21T09:12:03Z","project":"shop","environment":"staging","service":"web","release":{...}}

: ping
```

- `event:` equals the payload's `type`. `data:` is one JSON object with the
  matching payload field set.
- `: ping` comment lines every 25 s keep proxies from closing the stream.
- A stream ends with `event: end`, or `event: error` whose data is a problem
  document. Errors before the stream starts (auth, not found) are normal
  problem responses.

| Type | Payload field | When |
|---|---|---|
| `release` | `release` | A release is created or changes status |
| `release_event` | `release_event` | A deploy progress line ("Health check passed") |
| `build_log` | `build_log` | One line of build output (`release`, `seq`, `text`) |
| `service_status` | `service_status` | Container state changes (`running`, `crashed`, ...) |
| `domain` | `domain` | Domain added, removed (`message: "removed"`) or status changed |
| `audit` | `audit` | A mutating action was recorded |
| `drift` | `message` | The database and Docker disagree |
| `project_renamed` | `project_renamed` | A project got a new name ([below](#project-identity-and-renaming)) |
| `usage`, `traffic` | `usage`, `traffic` | A minute's resource samples or traffic |
| `run`, `job`, `uptime` | `run`, `job`, `uptime` | A one-off command started or ended, a job was set or removed, an uptime check changed state |
| `environment_lock`, `access_request` | `lock`, `access_request` | An environment was locked or unlocked; an access request was filed, decided or ran |
| `environment_move` | `move` | A move went a step further or ended |
| `git` | `git` | A push or a deploy from GitHub was recorded on a service's connection (`last_event`, `pending`) |
| `server`, `alert`, `update`, `disk`, `analytics`, `server_drain`, `cloudflare_redirect` | `server`, `alert`, `update`, `disk`, (none), `drain`, `redirect` | Platform events, for operators and admins |
| `lagged` | (none) | The client was too slow and missed events. Refetch |

Two more SSE endpoints use `event: line`:

| Endpoint | Streams |
|---|---|
| `GET .../releases/{number}/build-log/stream` | Build log so far, then new lines until the build ends |
| `GET .../services/{service}/logs?follow=true&tail=100&since=10m&release=N` | Container output (`time`, `stream`, `text`) |

`GET .../releases/{number}/build-log` returns the whole build log as plain
text.

## Examples

```sh
api="$DOCK_URL/api/v1"
auth="Authorization: Bearer $DOCK_TOKEN"
svc="$api/projects/shop/environments/staging/services/web"
```

Deploy an image and wait for the result:

```sh
n=$(curl -fsS -H "$auth" -H 'Content-Type: application/json' \
  -d '{"image":"ghcr.io/acme/web:1.4.2"}' "$svc/releases" | jq .number)
until s=$(curl -fsS -H "$auth" "$svc/releases/$n" | jq -r .status); [ "$s" = active ] || [ "$s" = failed ]; do sleep 2; done
echo "$s"
```

Deploy source (the CLI respects `.dockerignore`; plain `tar` doesn't):

```sh
tar -czf - --exclude=.git . | curl -fsS -H "$auth" -H 'Content-Type: application/gzip' \
  --data-binary @- "$svc/releases/source"
```

Set a variable and apply right away:

```sh
curl -fsS -X PATCH -H "$auth" -H 'Content-Type: application/json' \
  -d '{"service":"web","set":{"LOG_LEVEL":"debug"},"apply":true}' \
  "$api/projects/shop/environments/staging/variables"
```

Discard one staged change (no body discards everything staged; selecting
something that isn't staged is a `404` and discards nothing):

```sh
curl -fsS -X POST -H "$auth" -H 'Content-Type: application/json' \
  -d '{"variables":[{"key":"LOG_LEVEL","service":"web"}],"settings":[{"service":"web","field":"port"}]}' \
  "$api/projects/shop/environments/staging/changes/discard"
```

One service's audit log:

```sh
curl -fsS -H "$auth" "$api/audit?project=shop&environment=staging&service=web"
```

Roll back to release 41:

```sh
curl -fsS -X POST -H "$auth" "$svc/releases/41/rollback"
```

Follow events for one service:

```sh
curl -N -H "$auth" "$api/events?project=shop&environment=staging&service=web"
```

Tail logs:

```sh
curl -N -H "$auth" "$svc/logs?follow=true&tail=50"
```

### Project identity and renaming

`PATCH /api/v1/projects/{project}` with `{"name":"new-name","id":123}` renames
an active project. The optional `id` guards against renaming a reused name.
Admin access to the whole project is required. The response is the project with
its unchanged ID. Old-name paths do not redirect.

`GET /api/v1/project-ids/{id}` resolves the current name within the caller's
organization and scope. Linked clients use it before name-based requests and
send `Dock-Project-ID: <id>` on project paths. A different identity is rejected
with `409 conflict`; clients must not silently fall back to a reused name.

The `project_renamed` SSE event carries `previous_name` and the updated `project`
inside `project_renamed`. Filtered subscriptions keep following the stable
project identity. A missed rename is recovered by comparing project-list IDs
on reconnection.
