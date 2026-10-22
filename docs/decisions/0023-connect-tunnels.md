# 0023: `dock connect` tunnels

Status: accepted (decided by the agent)

## Context

Database services are private ([0011](0011-stateful-services-and-databases.md)):
no public port. The Data tab and `dock db` cover browsing and safe edits
([0016](0016-database-panel.md)), but people also want their own tools
(psql, TablePlus, pg_dump, redis-cli) against a private database, and
sometimes against another private service. 0011 promised `dock connect`,
"a stronger permission than deploy"; 0016 made it admin-only and gave it
the primitive it needs: `node.Runtime.Dial`, which reaches a service
container's port on its environment network, on this server or over the
node link ([0014](0014-node-protocol.md)).

Forces:

| Force | Means here |
|---|---|
| Private by default | No published port, no new container, no new listener on the server. |
| Raw access | A tunnel bypasses masking, read-only roles and changesets. Whoever gets one can do what the credentials allow. |
| Policy, not hard-coded rules | Data access is the configurable policy of 0016; admins grant it per subject and environment kind. |
| Agents ship safely | Tokens never write production data directly (0016). |
| Idle means idle | An open but quiet tunnel costs nothing but its connections. |
| Remote servers | Must work for environments on nodes, through the control plane. |

## Decision

### Shape

`dock connect [service] [--port N] [--confirm ENV] [--show-url]` listens on
`127.0.0.1` (the service's port when free, else any free one; `--port`
chooses) and runs until Ctrl-C. Every accepted connection becomes one
tunnel request to the API; core dials the service with `Runtime.Dial`
and copies bytes both ways.

| Topic | Decision |
|---|---|
| Transport | One HTTP/1.1 upgrade per TCP connection: `GET .../services/{s}/tunnel` with `Connection: Upgrade`, `Upgrade: dock-tunnel` and a bearer token, answered `101 Switching Protocols`, then raw bytes. A plain handler next to Huma (like MCP), bearer only: browsers can't send the upgrade and the session cookie never opens a tunnel. The CLI uses an HTTP/1.1-only transport for it (HTTP/2 has no upgrade). |
| Why not HTTP/2 full duplex | The API listens on plain HTTP (Tailscale or loopback), so HTTP/2 would mean h2c, which proxies handle worse than upgrades. Upgrades pass through Caddy, nginx and most reverse proxies unchanged. |
| Preflight | `GET .../services/{s}/connect` (in the OpenAPI spec): checks access, says the port and, for Postgres, the owner's user and database. The CLI calls it first so it fails before listening. |
| Target port | Postgres: the panel's connection port (URL, else the service's port, else 5432). Others: the active release's port, else its health port (template databases). A service without one can't be connected to. |
| Closing | Either side ending closes both, at once: a client that disconnects closes the service's connection, and the service closing ends the client's. No half-close: proxies don't carry it reliably, and psql, TablePlus and redis-cli don't need it. |
| Shutdown | Core tracks open tunnels; stopping the daemon closes them and audits each close. |
| Limits | 16 open tunnel connections per user or token (429 past it); remote servers also keep their 64 open dials per server. No idle timeout: database clients keep connections open on purpose. Tunnels hold no timers. |

### Access

A tunnel needs the data access policy to give the caller **level `write`
with sensitive `visible`** in that environment: everything the policy can
give. Anything less would let a tunnel route around masking or
changesets, which is what those settings promise.

| Caller | Non-production | Production |
|---|---|---|
| Admins (users, `dock login` tokens) | yes | yes, with `--confirm <env>` |
| Users with role deploy or read | only if the policy gives their role write, visible (default: no) | same, with `--confirm` |
| API tokens (agents), any scope | only with write, visible from the tokens rule or the token's own override, capped by the token's scope as usual (default: no) | never: on production a token's write acts as propose |

- Production asks for the environment's name (`--confirm production`), as
  committing a changeset does.
- Access is checked on every tunnel connection, so a change of policy or a
  revoked token applies to the next connection. Connections already open
  stay open until either side closes them.
- `--show-url` prints the connection URL with its password, pointed at
  the local port: for Postgres the owner's (as the panel derives it), else
  the variable `database_url_variable` names, else `DATABASE_URL`, else
  `REDIS_URL`. It needs the same access, since a tunnel without
  credentials is useless and someone who may tunnel already has full
  data access; it is audited as `data.reveal` and never printed without
  the flag.

This replaces 0016's "raw access is admin-only": with the default policy
it still is (only admins get write, visible), and admins can now grant it.

### Audit

| Action | When | Summary |
|---|---|---|
| `data.connect` | A tunnel connection opened | Port and the caller's address ("from 100.64.0.2"; behind a proxy, the proxy's) |
| `data.disconnect` | It closed | Duration and bytes each way |
| `data.reveal` | `--show-url` | "revealed the connection URL" |

Traffic is never logged or inspected.

## Consequences

- People use their own tools against private databases without a public
  port, on local and remote servers alike, and every connection is in the
  audit log.
- No periodic work: an idle tunnel is two blocked goroutines and its
  sockets (local services splice in the kernel). Nothing runs without one.
- Two audit events per TCP connection: clients with connection pools add a
  few rows per session. Retention ([0020](0020-retention-and-alerts.md))
  ages them out like other audit events.
- Agents can't open tunnels to production, so an admin-scope API token
  (the bootstrap token included) connects to non-production only; people
  use `dock login`.
- A tunnel is raw: SQL run through it isn't in the audit log, unlike the
  Data tab's. That's why it needs the policy's top level.
- `dock db shell` (psql through a tunnel) is left for later; `dock connect`
  plus a local client covers it.
