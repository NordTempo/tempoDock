# 0016: Database panel

Status: proposed

## Context

Database services ([0011](0011-stateful-services-and-databases.md)) are
private by default. To look at their data today you need `dock connect`
(not built yet) and a local client. We want a **Data** tab on database
services where people and agents browse, query and (deliberately) edit data,
with the same permissions, audit and safety as the rest of Tempo Dock.

NordTempo scoped a separate product for this, tempoBase (a Postgres viewer
with safe editing, per-connection roles and an MCP server). The owner decided
it becomes **part of Tempo Dock**, not a separate app integrated into it.
This ADR takes tempoBase's feature set as the target and builds it in Dock's
stack. tempoBase's own stack choices (TanStack Start, better-auth, its own
Postgres app database, its own users and tokens) are dropped.

Forces:

| Force | Means here |
|---|---|
| Idle means idle | No pools, goroutines or timers while nobody uses the panel. Daemon idle stays ≤ 30 MB. |
| Data access is stronger than deploy | 0011: direct data access needs more than `deploy`. |
| Agents ship all day safely | Agent data access is explicit, tiered, audited, and never writes production directly. |
| Private networking | The DB has no public port. The daemon must reach it over the environment's network only. |
| Remote servers | Environments can live on a node ([0014](0014-node-protocol.md)); the control plane must reach their DBs through the link. |

## Decision summary

| Topic | Decision |
|---|---|
| Shape | Native: Go in the daemon (pgx), React in the existing UI, `dock db` in the CLI. No extra process or container. |
| Engines | Postgres first. MySQL/MariaDB and Redis later behind the same API. |
| Detection | Template marker, else image name, else explicit override in service settings. |
| Network path | The node runtime dials the DB container's IP on the environment network from the host. Remote: the same dial, streamed over the node link. New `node.Runtime.Dial`. |
| DB credentials | Dock creates two least-privilege Postgres roles (`dock_reader`, `dock_writer`). The owner credentials from variables are only used to create them. Nothing reaches the browser. |
| Permissions | `read`: none. `deploy`: browse and read-only SQL on non-production. `admin`: everything, including production and all editing. Tokens additionally need an explicit data tier. |
| Writes | Read-only by default. Explicit edit mode, staged changes, review diff with the generated SQL, one transaction. Production asks for the environment name. Agents never write production directly. |
| Audit | Every query and every write in Dock's audit log, with the SQL in a detail row. Query history is read from it. |
| Performance | Connections opened on first use, closed after 60 s idle; results streamed in bounded batches with hard caps; heavy UI in a lazy route chunk. |

## 1. Detection and connection details

### Is this service a database?

First match wins.

| Signal | Result |
|---|---|
| Service setting `database = none` | Not a database (override). |
| Service setting `database = postgres` | Postgres (override, or set by a template). |
| Image repository is a known Postgres image: `postgres`, `postgis/postgis`, `pgvector/pgvector`, `timescale/timescaledb*`, `bitnami/postgresql`, `supabase/postgres` | Postgres. |
| Later: `mysql`, `mariadb` (3306); `redis`, `valkey/valkey` (6379) | That engine, once supported. |
| Anything else, including a bare port 5432 | Not detected. The Settings tab offers the override. |

`database` is a Dock-side setting: it applies immediately and never creates a
release. Templates (0011, M5) set it when they create the service.

### Connection details

Read from the **active release's resolved config** (what the container
actually runs with), never from staged values.

| Field | Source, in order |
|---|---|
| Owner user, password, database | 1. The variable named by the `database_url_variable` setting (e.g. `DATABASE_URL`, or `api.DATABASE_URL` for another service's). 2. The service's own `DATABASE_URL` (templates set it). 3. `POSTGRES_USER` (default `postgres`), `POSTGRES_PASSWORD`, `POSTGRES_DB` (default: the user). |
| Host | Ignored. The panel always dials the service's own container (section 2). A URL whose host isn't this service is an external database: out of scope for v1. |
| Port | Port from the URL, else the service's `port` setting, else 5432. |
| Used by | Services whose variables reference `${{<db>.DATABASE_URL}}` or point at this hostname. Shown in the panel, never used for credentials. |

`POSTGRES_PASSWORD` only takes effect when the volume is first initialised.
If it was changed later, the owner login fails and the panel says so, pointing
at `database_url_variable`.

## 2. Network path

| Option | Security | Performance | Verdict |
|---|---|---|---|
| **Dial the container's IP on the environment bridge from the host** | Host-to-bridge traffic is outside the app firewall's scope (it guards traffic *from* bridges; replies are `ESTABLISHED`). No new port, no new container. Dial only targets Dock-labelled service containers. | One TCP connection, zero overhead, nothing running when idle. | **Chosen** |
| Publish the DB port on loopback | Every DB gets a host port; anything on the host can reach it. Adding it is a release, and stateful releases recreate (downtime). | Same as above. | Rejected |
| Short-lived proxy container (socat) on the environment network | Another container with network access to the DB. | 0.5 to 1 s container start per session, image to keep. | Rejected |
| `docker exec psql` in the DB container | Runs a process inside the DB container; text output must be parsed. | A process per query, no types, no streaming, no cancel. | Rejected |

**`node.Runtime.Dial(ctx, DialRequest{ContainerID, Port}) (net.Conn, error)`**

- **Local:** inspect the container, check it carries `dock.managed` and a
  service label, take its IP on its environment network (never another
  network), `net.Dial` from the host.
- **Remote:** the same checks and dial run on the node. Bytes stream over the
  node link as a full-duplex call, the shape `Build` already uses (body up,
  body down, cancel by RST_STREAM). Closing either side ends the call.
- Not an open proxy: no caller can pass an address, only a container the
  control plane placed on that server.
- The same primitive later serves `dock connect` (0011).

pgx gets it as its `DialFunc`. Postgres cancel requests (sent on context
cancel) take the same path.

## 3. Engine in the daemon

New package `internal/data` (engine-neutral types) and `internal/data/pg`
(pgx v5, MIT).

| Concern | Rule |
|---|---|
| Connections | No `pgxpool`: it runs a health-check goroutine for as long as the pool lives. A small map of `*pgx.Conn` keyed by (service, role), at most 2 per key and 8 per daemon, each closed by `time.AfterFunc` 60 s after its last use. Idle panel: zero connections, zero goroutines. |
| Reads | `BEGIN READ ONLY` as `dock_reader`; `statement_timeout` 10 s (CLI `--timeout` up to 60 s); `lock_timeout` 1 s; one statement per request (extended protocol, multi-statement strings rejected); `application_name = dock:<actor>`. |
| Writes | As `dock_writer`, one transaction per changeset, `lock_timeout` 2 s, `statement_timeout` 30 s, `idle_in_transaction_session_timeout` 30 s. |
| Streaming | SQL results go out as NDJSON: a header frame (columns, types), row frames of up to 500 rows, an end frame (row count, truncated, duration). Rows are encoded as they are read; memory is bounded by one batch. |
| Caps | UI 10 000 rows per query, CLI default 1 000 and `--limit` up to 100 000, 64 MB per response, cells over 8 KB truncated (full value on request). Hitting a cap ends the stream with `truncated: true`. |
| Cancel | Client disconnect cancels the context; pgx cancels the query on the server. |
| Table pages | Keyset pagination: `ORDER BY` the primary key (or a sort column plus the key), cursor = last key values, `LIMIT 100`. No `OFFSET`. Tables without a primary key page by `ctid` and are read-only. Row counts are `pg_class.reltuples` estimates; exact count on request, under the statement timeout. |
| Filters | Structured (`column op value`) and compiled to parameterised SQL. Free-form filtering is the SQL editor. |
| Introspection | One catalog query per panel open (schemas, tables, columns, keys, foreign keys, indexes, sizes), cached in memory 60 s. Feeds the table list, FK navigation, editor autocomplete and the schema view. |

Targets: daemon idle unchanged; panel in use adds ≤ 10 MB; first table page
≤ 100 ms on the local server for a warm table. Binary size impact measured
when pgx lands.

## 4. Permissions

Data access is checked in core, on the same `Actor` path as everything else
(0008). Humans get it from their role; tokens also need an explicit tier.

### By scope (owner's decision)

| Capability | `read` | `deploy`, non-production | `deploy`, production | `admin` |
|---|---|---|---|---|
| See the Data tab, schema | no | yes | no | yes |
| Browse rows, read-only SQL | no | yes | no | yes |
| Edit mode, commit changes | no | no | no | yes |
| Approve or reject agent proposals | no | no | no | yes |
| Set up roles, mark sensitive columns, overrides | no | no | no | yes |
| Reveal masked values | no | no | no | yes |
| `dock connect`, `dock db shell` (raw access) | no | no | no | yes |

Token project/environment limits apply as usual.

### Token data tiers

Tokens get a new field `data_tier`, default `none`. Effective access is the
lower of the tier and what the scope allows above.

| Tier | Allows | Needs scope |
|---|---|---|
| `none` | Nothing | |
| `schema` | Tables, columns, relations. No rows. | `deploy` (non-production) or `admin` |
| `read` | Browse and read-only SQL, sensitive columns masked | `deploy` (non-production) or `admin` |
| `propose` | `read`, plus drafting changesets that a human admin approves in the UI | `admin` |
| `write` | `propose`, plus committing its own changesets directly on non-production | `admin` |

On production, `write` acts as `propose`: agents never write production data
without a human approving the exact SQL.

`dock token create --data-tier read` sets it; the token list shows it.

## 5. Safety

### Least-privilege roles in the database

A read-only transaction is not enough as the DB superuser: `COPY ... TO
PROGRAM`, `pg_terminate_backend` and friends still work. So the panel never
runs reads or edits as the owner.

| Role | Grants | Used for |
|---|---|---|
| `dock_reader` | `pg_read_all_data`, `default_transaction_read_only = on`, not superuser | All browsing and SQL |
| `dock_writer` | `pg_read_all_data`, `pg_write_all_data` (DML only, no DDL) | Committing changesets |
| `dock_reader_masked` (phase 4) | `SELECT` per column, excluding sensitive columns; re-synced when marks or the schema change | Masked actors |

- **Setup** creates them with the owner credentials: automatically for
  template databases, otherwise one admin action ("Set up data access",
  `dock db setup`). Passwords are generated by Dock and stored encrypted
  (`internal/secrets`), never as service variables, so apps don't see them.
  Re-running setup rotates them.
- Until setup, the panel shows only "Set up data access" (admins) or "Not
  set up" (others).
- Requires Postgres 14+ (predefined roles). Older versions show
  "unsupported".
- Row-level security still applies to these roles; that's correct.
- DDL stays with `dock connect` and a real client.

### Credentials

Owner credentials are decrypted in memory only for setup. Role passwords live
only in the daemon. Nothing about credentials goes to the browser or into
API responses, logs or events. The CLI's raw paths (`dock connect`,
`dock db shell`) are admin-only, consistent with admins reading secret values.

### Editing

| Step | Behaviour |
|---|---|
| Edit mode | Off by default, per tab, never remembered. Turning it on is explicit (`e`, or the button). |
| Staging | Cell updates, inserted rows and deleted rows are buffered in the browser and highlighted in the grid. Nothing hits the server. |
| Review | The server renders the changeset: per-row diff plus the exact SQL it will run. |
| Commit | One transaction. Each `UPDATE`/`DELETE` matches the primary key **and** the original values of the edited columns, so a row changed by someone else fails the whole commit with a conflict listing the rows. At most 500 rows per changeset. |
| Production | Admin only, and the commit asks for the environment name (`--confirm production` in the CLI). |
| Not editable | Tables without a primary key, views, generated columns. |

### Agent proposals

`propose`-tier tokens submit changesets through the same pipeline. They show
up under the Data tab as pending, with diff and SQL. An admin approves (the
changeset is revalidated against current data and committed) or rejects.
Pending changesets expire after 7 days.

### Sensitive columns

Admins mark columns sensitive; names like `password`, `*_hash`, `token`,
`secret` are suggested on setup. Masked for all tokens and for `deploy`
users; admins see them masked too until they reveal (audited). Enforced by
`dock_reader_masked` in Postgres, so SQL can't route around it (phase 4;
until then, masked actors get structured browsing only, no free SQL).

## 6. Audit

Every query and write goes into Dock's existing audit log. The full SQL goes
into a detail row, so the main log stays scannable.

| Action | Written when | Detail |
|---|---|---|
| `data.query` | Each SQL statement, from UI, CLI or agent | SQL (up to 16 KB), rows, duration, outcome |
| `data.browse` | First table page per actor and table, then at most every 10 min | Table, filter, sort |
| `data.commit` | Each committed changeset | Generated SQL, rows affected per statement, approver for proposals |
| `data.propose`, `data.approve`, `data.reject` | Changeset lifecycle | Changeset ID |
| `data.reveal` | Masked values revealed | Table, columns |
| `data.setup` | Roles created or rotated | |
| `data.connect` | Raw tunnel opened and closed | Duration, bytes |

`AuditEvent` gains an optional `detail_id`. **Query history** is the
caller's own `data.query` events; there's no separate history store.

## 7. Feature set and phasing

tempoBase's v1 list, mapped:

| Feature | Phase |
|---|---|
| Table browser: keyset pagination, sort, structured filters, row detail | 3 |
| FK navigation: jump to the referenced row, list referencing rows | 3 |
| SQL editor: autocomplete from the schema, history, saved queries | 3 |
| Schema view: columns, indexes, constraints | 3 |
| Read-only by default, edit mode, staged edits, review, commit | 4 |
| Agent tiers, proposals and approval | 4 (schema/read tiers in 3) |
| Sensitive-column masking | 4 |
| ER diagram | 5 |
| MCP endpoint (`/api/v1/mcp`: tools mirror the `dock db` commands, plus built-in docs) | 5 |
| MySQL/MariaDB, Redis | later |

Until the MCP endpoint lands, agents use `dock db ... --json`.

## 8. Surface

### UI

A **Data** tab (`g d`) on the service page, shown only for detected
databases. Inside it: Tables, SQL, Schema, Changes (proposals). Everything
has a URL (`/shop/staging/postgres/data/public.orders?after=...`).

```
postgres  data  read-only  dock_reader  staging              edit (e)
public          public.orders  ~12 431 rows  filter: status = paid  ×
  customers     id    customer_id →  total    status  created_at
  orders        1041  88             120.00   paid    2026-10-20 14:02
  products      1042  91             15.50    paid    2026-10-20 14:05
  ...           ...
                next →
```

- Same v2 language: true black, white text, monospace data, no cards or
  pills, static state text, no spinners. The edit-mode indicator is text
  in the header line, and production adds the environment name in the
  warning colour.
- Grid: Glide Data Grid (MIT, canvas). Editor: CodeMirror 6 with
  `@codemirror/lang-sql` (MIT). Both live in the lazy Data route chunk
  (budget ≤ 250 KB gzipped), never in the initial bundle.
- Staged edits live in the route's own state (a reducer), no new state
  library.
- Errors say what to do: "Not set up. An admin can run `dock db setup`."

### CLI

| Command | What it does | Needs |
|---|---|---|
| `dock db status` | Engine, version, setup state, your access | `deploy` (non-prod) |
| `dock db setup` | Create or rotate the Dock roles | `admin` |
| `dock db tables`, `dock db describe TABLE` | Schema | `deploy` (non-prod), tier `schema` |
| `dock db rows TABLE [--where col=val] [--order col] [--after CURSOR] [--limit N]` | One keyset page; prints the next cursor | tier `read` |
| `dock db query SQL\|-f FILE\|- [--limit] [--timeout] [--csv]` | Read-only SQL, streamed | tier `read` |
| `dock db changes list\|show\|approve\|reject ID` | Changesets | `admin` to approve |
| `dock db propose -f changes.json` | Submit a changeset (`--commit` with tier `write` on non-production) | tier `propose` |
| `dock connect [--port N]`, `dock db shell` | Raw tunnel (0011); `shell` runs a local `psql` through it | `admin` |

All take `--json` and the linked service. Permission errors name the missing
scope or tier.

### API

Under `/api/v1/projects/{p}/environments/{e}/services/{s}/data`:

| Endpoint | |
|---|---|
| `GET /data` | Engine, version, status (`ready`, `setup_needed`, `unsupported`, `unreachable`, `not_running`), the caller's access |
| `POST /data/setup` | Admin |
| `GET /data/schema` | Introspection result |
| `GET /data/tables/{table}/rows` | Keyset page: `after`, `order`, `filter`, `limit` |
| `GET /data/tables/{table}/references` | Referencing rows for FK navigation |
| `POST /data/query` | NDJSON stream |
| `POST /data/changesets` | `{changes, dry_run}`: preview, or stage a proposal |
| `POST /data/changesets/{id}/commit`, `/reject` | `{confirm}` on production |
| `GET`, `PUT`, `DELETE /data/saved-queries[/{id}]` | Per user, per service |
| `PUT /data/sensitive-columns` | Admin |
| `PUT /services/{s}` | `database`, `database_url_variable` settings |

## 9. Data model

```sql
tokens:          + data_tier TEXT NOT NULL DEFAULT 'none'
                     CHECK (data_tier IN ('none','schema','read','propose','write'))
services:        + database TEXT NOT NULL DEFAULT ''        -- '', 'postgres', 'none'
                 + database_url_variable TEXT NOT NULL DEFAULT ''
data_access:     service_id PK, engine, server_version, reader_secret, writer_secret,
                 masked_synced_hash, set_up_at, set_up_by
sensitive_columns: service_id, schema_name, table_name, column_name, PK of all four
saved_queries:   id, service_id, user_id, name, sql, created_at, updated_at
changesets:      id, service_id, author (actor), status (pending, committed, rejected,
                 expired, conflict), changes JSON, sql TEXT, created_at, decided_at,
                 decided_by, expires_at
audit_details:   id, sql TEXT, rows INTEGER, duration_ms INTEGER, outcome TEXT
audit_events:    + detail_id INTEGER REFERENCES audit_details (id)
```

## 10. Plan

Within a phase, tracks own disjoint paths so agents can run in parallel.
Phase 1 is one agent and short.

| Phase | Track | Owns | Delivers |
|---|---|---|---|
| 1 | Contracts | `internal/apitypes/**`, `internal/node/runtime.go`, `internal/node/fake/**`, `internal/data/data.go` | Data API types (status, schema, page, query frames, changeset, tiers); `Runtime.Dial` signature, fake dialing a test address; engine interface; core signatures stubbed |
| 2 | Dial | `internal/node/docker/**`, `internal/node/local.go`, `internal/node/remote/**` | Local dial on the environment network with label checks; full-duplex dial over the node link |
| 2 | Engine | `internal/data/pg/**` (new) | Conn cache with idle close, introspection, keyset pages, filters, streamed queries, value encoding, setup SQL, changeset SQL and apply with conflict checks; tests against a real Postgres in CI |
| 2 | Store | `internal/store/**` | Migration and sqlc queries for section 9 |
| 2 | Web groundwork | `web/src/features/data/**` (new) | Lazy chunk with Glide grid, CodeMirror editor, tables list, against fixtures typed from the OpenAPI schema |
| 3 | Core | `internal/core/data*.go` (new files), `internal/core/actor.go` | Detection, connection derivation, permission matrix and tiers, audit and details, setup, read paths |
| 3 | Surface | `internal/api/data.go` (new), `internal/cli/db.go` (new), `docs/cli.md`, `docs/guide/**` | Endpoints, NDJSON streaming, `dock db *`, `--data-tier` on tokens, docs |
| 3 | Web | `web/src/routes/*data*`, `web/src/features/data/**`, `web/src/features/service/ServiceHeader.tsx` | Data tab wired to the API: tables, FK navigation, SQL with history and saved queries, schema view |
| 4 | Editing | `internal/core/data*.go`, `internal/data/pg/**`, `web/src/features/data/**` | Edit mode, staged edits, review, commit, production confirm, proposals and approval, masking role and reveal |
| 5 | Later | per feature | ER diagram, MCP endpoint, `dock connect`/`db shell`, more engines |

Surface and Web start from the phase 1 types and the OpenAPI spec, so they
can run against stubs early. Roadmap: phases 1 to 3 fit M5 (Data) next to the
templates; 4 and 5 follow.

## Consequences

- No new process, container or resident cost; the panel costs something only
  while used.
- One new dependency (pgx) and one new `node.Runtime` method, which `dock
  connect` reuses.
- The DB enforces the important limits (read-only role, DML-only writer,
  column grants), so a bug in Dock's checks doesn't become a superuser query.
- Dock creates roles inside user databases. That's visible (`\du`) and
  documented; setup is one admin action for non-template databases.
- `deploy` users and agents can read non-production data. As
  [safety-model](../safety-model.md) notes, `deploy` already implies the
  environment's secrets, so this adds convenience and audit, not new trust.
- External databases (a `DATABASE_URL` pointing at Neon and the like) aren't
  covered.
- The UI bundle grows only in the lazy chunk.

## Alternatives considered

| Alternative | Why not |
|---|---|
| tempoBase as a separate app, run on demand and embedded | Owner decision: one product. It also meant a second auth system, SSO, a Node app plus its own database, and an iframe origin to secure. |
| Adminer or pgweb containers on demand | Their own auth and no tie-in to Dock's permissions, audit or agents. |
| One-shot `docker exec psql` for everything | See section 2. |

## Note on tempoBase

The brief in the tempoBase repository (`docs/brief.md`, 2026-10-21) is
superseded by this ADR. Its feature list and design constraints carry over;
its standalone product shape, stack and permission model do not.

## Open questions

| Question | Proposed default |
|---|---|
| May `deploy` tokens hold tier `propose` on non-production (an admin still approves)? | No: `propose` needs `admin`, as decided. Revisit if agents need it. |
| Should `dock_reader` setup run automatically for detected (non-template) databases on first open by an admin, instead of a separate action? | Explicit action: it writes to a user database. |
| Mask sensitive columns for `deploy` users too, or only for tokens (tempoBase's default)? | Both, as above. |
