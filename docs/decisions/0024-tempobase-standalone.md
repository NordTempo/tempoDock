# 0024: tempoBase, a standalone build of the Data panel

Status: accepted (decided by the agent)

## Context

[0016](0016-database-panel.md) folded tempoBase (NordTempo's Postgres
viewer) into Tempo Dock as the Data panel and dropped its standalone
shape. The owner now wants both: "I just want to be able to have this
integrated inside TempoDock in a neat and performant way, but I also want
to be able to self-host it as a standalone." Already decided: **one
codebase**. tempoBase becomes a second binary built from this repository,
sharing the Go data engine and the React panel. The TypeScript tempoBase
app (`NordTempo/tempoBase`: TanStack Start, better-auth, Drizzle, its own
Postgres) is retired once its good ideas are ported.

Today the panel's logic is split between host-neutral code
(`internal/data`, `internal/data/pg`, `web/src/features/data`) and Dock
glue in `internal/core/data*.go` that mixes the panel's rules (policy,
setup, grants, cursors, reveal, changesets, audit summaries) with Dock's
concepts (services, environments, releases, tokens, the audit table).

| Force | Means here |
|---|---|
| One codebase | Every rule of the panel exists once. A fix to masking or changesets lands in Dock and tempoBase together. |
| Dock unchanged | Dock's API, behaviour and tests stay as they are. The only contract change is additive and Dock never emits it. |
| Lightweight | tempoBase must not link Dock's daemon (Docker, BuildKit, node runtime). Smaller binary and idle footprint than Dock's. |
| Idle means idle | No timers, pools or polling in tempoBase either. Database connections close 60 s after last use, as in Dock. |
| Same safety model | Least-privilege roles, read-only reads, changesets, masking, reveal and audit work exactly as in 0016. |
| Easy self-hosting | One static binary or one small image, one data directory, first admin in a minute. |

## Decision summary

| Topic | Decision |
|---|---|
| Shape | `cmd/tempobase` builds binary `tempobase`: HTTP API, embedded UI and MCP endpoint over its own SQLite. Same repository, same version, same release as `dock`. |
| Boundary | New package `internal/datapanel`: the panel's logic behind a generic `Host[D]` interface. Dock's core becomes an adapter (`Host[*svcData]`); tempoBase implements it over its own store. `datapanel.Ops[A, R]` is the operation set both hosts expose. |
| Sharing | Shared as-is: `apitypes` (data types), `data`, `data/pg`, `secrets`, `version`. Extracted: `datapanel`, `datapanel/datahttp`, `httpkit`, `mcpkit`. tempoBase never imports `core`, `api`, `store`, `node`, `cli` or `mcp`. |
| Databases | A saved **connection**: a Postgres URL, sealed at rest with an AES key file (`internal/secrets`), TLS modes from `sslmode`, a per-connection **production** flag. Roles `tempobase_reader`, `tempobase_writer`, `tempobase_reader_masked`. |
| Auth | Local users (email, bcrypt password, role `viewer`, `editor` or `admin`), cookie sessions, per-user API tokens. First admin through a one-time setup code printed at start. Invite and reset links instead of email. OIDC later. |
| Policy | 0016's policy unchanged, scoped default then connection instead of default, project, environment. Viewers are subject `role:read`, editors `role:deploy`. Connections where a caller's level is `none` are invisible to them. |
| API | Dock's data endpoints with the same request and response types under `/api/v1/connections/{connection}/data`, plus tempoBase's own auth, users, tokens, connections, policy and audit endpoints. |
| MCP | `POST /api/v1/mcp` with the same data tool names as Dock's (`data_status`, `data_schema`, `data_rows`, `data_query`, `propose_data_changes`), `connection` instead of project, environment and service. |
| Web | One Vite project, two entries: `web/` (Dock, unchanged) and `web/tempobase/` (second config). `features/data` is shared through a source bound to one database. tempoBase initial JS ≤ 120 KB gzip. |
| Packaging | Static binary (≤ 20 MB, UI embedded), distroless image `ghcr.io/nordtempo/tempobase`, idle anonymous RSS ≤ 20 MB (target 10), zero periodic work. `make perf` covers it. |
| Releases | Built and published by this repository's release workflow on every `v*` tag. `NordTempo/tempoBase` keeps only a README pointing here; no mirrored releases. |

## 1. Boundaries

### Packages

| Package | Role | Linked by |
|---|---|---|
| `internal/apitypes` | JSON contract. The data types (`data.go`) are shared verbatim. | both |
| `internal/data` | Engine-neutral types and the `Engine` interface. | both |
| `internal/data/pg` | The Postgres engine. | both |
| `internal/secrets` | AES-GCM box and key file. | both |
| `internal/version` | Version string. | both |
| `internal/datapanel` (new) | The panel's logic: policy decisions, setup, grants syncs, cursors, reveal, browse throttle, changesets, masking, audit summaries. | both |
| `internal/datapanel/datahttp` (new) | Huma helpers for the data endpoints: NDJSON streaming, filter and key parsing, body caps, error statuses, operation descriptions. | both |
| `internal/httpkit` (new) | Embedded SPA handler (from `api/ui.go`), security headers and CSRF guard (from `api/guard.go`), token-bucket limiter (from `core/ratelimit.go`). | both |
| `internal/mcpkit` (new) | Stateless MCP transport, tool registry with Huma schemas, docs resources, and the data tools, generic over the actor and database types. | both |
| `internal/core`, `internal/api`, `internal/store`, `internal/node/**`, `internal/mcp`, `internal/cli` | Dock. `internal/mcp` keeps Dock's platform tools and builds on `mcpkit`. | Dock only |
| `internal/tempobase/**` (new), `cmd/tempobase`, `web/tempobase` (`tbweb`), `docs/tempobase` (`tbdocs`) | tempoBase. | tempoBase only |

Import rule, checked in CI (`make tempobase-deps`): `go list -deps
./cmd/tempobase` contains none of `internal/core`, `internal/api`,
`internal/store`, `internal/node`, `internal/mcp`, `internal/cli`,
`github.com/moby/`, `github.com/moby/buildkit`, `github.com/railwayapp/`,
`google.golang.org/grpc`.

### `internal/datapanel`

The types and interfaces are committed with this ADR
([datapanel.go](../../internal/datapanel/datapanel.go),
[errors.go](../../internal/datapanel/errors.go),
[policy.go](../../internal/datapanel/policy.go)), compiled and checked
against Dock's core (`var _ datapanel.Ops[Actor, ServiceRef] =
(*Core)(nil)` in `internal/core/data_ops.go`). In short:

```go
type Actor struct{ Kind, Name string; UserID, TokenID int64 }

// A database resolved for one call; D is the host's own handle.
type Database[D any] struct {
	ID          int64 // keys engine caches, cursors, throttles and every store call
	Name        string
	Engine      apitypes.DataEngine
	DetectedBy  apitypes.DataDetection
	ConfirmName string // what a production commit confirms with
	Perm        apitypes.DataPermission // from Decide
	Host        D
}

type Conn struct {
	Dial        func(ctx context.Context) (net.Conn, error)
	TLS         *tls.Config // nil: plaintext
	TLSOptional bool        // sslmode=prefer
	Database    string
	Owner       data.Credentials
}

type Host[D any] interface {
	Connect(ctx context.Context, db D) (Conn, error) // ErrNotRunning, or a validation Error
	Access(ctx context.Context, id int64) (*Access, error)
	PutAccess(ctx context.Context, id int64, acc Access) error
	SetGrantsHash(ctx context.Context, id int64, hash string) error
	SensitiveColumns(ctx context.Context, id int64) ([]apitypes.DataColumnRef, error)
	ReplaceSensitiveColumns(ctx context.Context, id int64, cols []apitypes.DataColumnRef) error
	SavedQueryOwner(ctx context.Context, a Actor) (int64, error)
	SavedQueries(ctx context.Context, id, user int64) ([]apitypes.DataSavedQuery, error)
	SaveQuery(ctx context.Context, id, user, queryID int64, name, sql string, at time.Time) (apitypes.DataSavedQuery, error)
	DeleteSavedQuery(ctx context.Context, id, user, queryID int64) (bool, error)
	CreateChangeset(ctx context.Context, cs Changeset) (Changeset, error)
	Changeset(ctx context.Context, id, changesetID int64) (Changeset, error)
	Changesets(ctx context.Context, id int64, pendingOnly bool, limit int) ([]Changeset, error)
	DecideChangeset(ctx context.Context, d Decision) (Changeset, error)
	ExpireChangesets(ctx context.Context, now time.Time) error
	Audit(ctx context.Context, a Actor, db D, e AuditEntry)
	QueryHistory(ctx context.Context, a Actor, db D, before int64, limit int) ([]apitypes.DataHistoryEntry, error)
}

type Options[D any] struct {
	Host   Host[D]
	Engine data.Engine
	Box    *secrets.Box // role passwords and cursors
	Roles  data.Roles   // data.DockRoles or data.TempoBaseRoles
	Words  Words        // host wording: "Dock's roles", how to set up
	Logger *slog.Logger
}

// What both hosts expose to their API and MCP endpoint.
type Ops[A, R any] interface { DataInfo, SetupData, DataSchema, DataRows, DataReferences,
	DataQuery, DataHistory, ListSavedQueries, SaveDataQuery, DeleteSavedQuery,
	ListDataChangesets, GetDataChangeset, CreateDataChangeset, CommitDataChangeset,
	RejectDataChangeset, SetSensitiveColumns } // signatures as core's today
```

Workstream A adds the `Panel` (moving the bodies of `core/data.go`,
`data_changesets.go`, `data_cursor.go`, `data_audit.go` and the decision
half of `data_policy.go`):

```go
func New[D any](o Options[D]) *Panel[D]

func (p *Panel[D]) Info(ctx context.Context, a Actor, db *Database[D]) (apitypes.DataInfo, error)
func (p *Panel[D]) Setup(ctx context.Context, a Actor, db *Database[D]) (apitypes.DataInfo, error)
func (p *Panel[D]) Schema(ctx context.Context, a Actor, db *Database[D]) (apitypes.DataSchema, error)
func (p *Panel[D]) Rows(ctx context.Context, a Actor, db *Database[D], req apitypes.DataPageRequest) (apitypes.DataPage, error)
func (p *Panel[D]) References(ctx context.Context, a Actor, db *Database[D], req apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error)
func (p *Panel[D]) Query(ctx context.Context, a Actor, db *Database[D], req apitypes.DataQueryRequest) (Stream[apitypes.DataQueryFrame], error)
func (p *Panel[D]) History(ctx context.Context, a Actor, db *Database[D], before int64, limit int) ([]apitypes.DataHistoryEntry, error)
func (p *Panel[D]) SavedQueries(ctx context.Context, a Actor, db *Database[D]) ([]apitypes.DataSavedQuery, error)
func (p *Panel[D]) SaveQuery(ctx context.Context, a Actor, db *Database[D], id int64, req apitypes.SaveDataQueryRequest) (apitypes.DataSavedQuery, error)
func (p *Panel[D]) DeleteSavedQuery(ctx context.Context, a Actor, db *Database[D], id int64) error
func (p *Panel[D]) SetSensitiveColumns(ctx context.Context, a Actor, db *Database[D], req apitypes.SetSensitiveColumnsRequest) ([]apitypes.DataColumnRef, error)
func (p *Panel[D]) CreateChangeset(ctx context.Context, a Actor, db *Database[D], req apitypes.DataChangesetRequest) (apitypes.DataChangeset, error)
func (p *Panel[D]) Changesets(ctx context.Context, a Actor, db *Database[D], status apitypes.DataChangesetStatus) ([]apitypes.DataChangeset, error)
func (p *Panel[D]) Changeset(ctx context.Context, a Actor, db *Database[D], id int64) (apitypes.DataChangeset, error)
func (p *Panel[D]) Commit(ctx context.Context, a Actor, db *Database[D], id int64, req apitypes.DataCommitRequest) (apitypes.DataChangeset, error)
func (p *Panel[D]) Reject(ctx context.Context, a Actor, db *Database[D], id int64, req apitypes.DataRejectRequest) (apitypes.DataChangeset, error)
func (p *Panel[D]) RedactDetail(perm apitypes.DataPermission, own, sensitive bool, d apitypes.AuditDetail) apitypes.AuditDetail
func (p *Panel[D]) Forget(id int64) // connection or container changed
func (p *Panel[D]) Close()

// Policy, pure functions.
func Decide(in PolicyInput) apitypes.DataPermission
func RequireLevel(perm apitypes.DataPermission, need apitypes.DataLevel, action string) error
func Why(perm apitypes.DataPermission) string
func ValidateRules(rules []apitypes.DataPolicyRule, only apitypes.EnvironmentKind, scope string) error
func Effective(rules []apitypes.DataPolicyRule, kinds []apitypes.EnvironmentKind, builtIn BuiltIn) []apitypes.DataPolicyRule
func DescribeRules(target string, rules []apitypes.DataPolicyRule) string
func ValidateTokenAccess(d *apitypes.DataTokenAccess) error
func DescribeTokenAccess(d *apitypes.DataTokenAccess) string
```

`Info` fills everything but `UsedBy`, which Dock adds. `Setup` and
`SetSensitiveColumns` need `db.Perm.Manage`; `Commit` checks
`req.Confirm == db.ConfirmName` on production. The browse throttle, the
in-flight decision claims and the cursor sealing keep their current rules
and bounds, keyed by `Database.ID` and the actor.

`Decide` reproduces `decidePermission` exactly. Dock builds its input as
today: `Admin` = scope admin, `Agent` = token without an email,
`Subject` = the scope's subject, `Token.Caps` = [the token scope's subject
(agents)] + [the creator's role subject, unless admin]. Rule rank by
`From`: `environment` and `connection` 3, `project` 2, `default` 1, else
the host's `BuiltIn`.

### What stays in Dock's core

Detection (`detectDatabase`), connection derivation from the active
release (`dbConnection`, `parseDatabaseURL`, `variableValue`), dialing
through the node runtime, `usedBy`, `UpdateDatabaseSettings`, policy
storage and scope checks (`GetDataPolicy`, `SetDataPolicy`, using
`ValidateRules`, `Effective`, `DescribeRules`), `UpdateToken`,
`GetAuditDetail` (using `Decide`, `RequireLevel`, `RedactDetail`),
tunnels (`data_connect.go`, using `Decide`), the container-event
`Forget`, and Dock's built-in rules (`DockBuiltIn`).

Core's `DataInfo`, `DataRows` and the rest keep their signatures: each
resolves the `ServiceRef` to a `*datapanel.Database[*svcData]` (service
scope, detection, `Decide`) and calls the Panel. The adapter is a small
type in `internal/core/data_host.go` implementing `Host[*svcData]` over
the existing sqlc queries and `c.audit`/`c.auditDetail`.

Unit tests of moved internals move with them (`TestDecidePermission`,
`TestPermissionWhy`, `TestBrowseThrottle`, `TestCutSQL`, masking);
`datapanel` gets an in-memory fake `Host`. Every other Dock test stays
where it is and passes unchanged.

### Engine changes

| Change | Why |
|---|---|
| `data.Roles`, `data.DockRoles`, `data.TempoBaseRoles` (committed) and `pg.New(pg.Options{Roles, AppName})`; the zero `Options` is Dock's | tempoBase's roles have their own names, so Dock and tempoBase can both manage one database without rotating each other's passwords. `application_name` becomes `tempobase:<actor>`. |
| `data.Target.TLS *tls.Config`, `TLSOptional bool` | External databases need TLS. `connect` sets `TLSConfig` (and a plaintext fallback when optional); Dock passes neither. |
| `Engine.Check(ctx, t, owner) (ServerInfo, error)` and `data.ErrCannotSetUp` | Logs in as the owner without changing anything: version 14 or later, and `CREATEROLE` or superuser. tempoBase's "Test" button and its setup message use it. |

### Errors and streams

`datapanel.Error` (code and message) is the one domain error: Dock's
`core.Error` becomes `type Error = datapanel.Error` and `core.CodeOf`
calls `datapanel.CodeOf`. `core.Stream` is already an alias of
`datapanel.Stream` (committed). Both keep every existing `errors.As`
and type assertion working.

### Shared HTTP and MCP

- **Data handlers.** Huma can't embed a generic path type, so each host
  registers its data endpoints itself (Dock's `internal/api/data.go`
  unchanged in shape, tempoBase's in `internal/tempobase/tbhttp`), and
  both use `datahttp`: `StreamFrames`, `ParseFilters`, `ParseKey`,
  `SQLBodyBytes`, `ChangesetBodyBytes`, `Errors` and `Describe(op,
  roles)` for summaries and descriptions with the host's role names.
- **MCP.** `mcpkit.Server[A]` holds today's transport (stateless POST,
  progress streams, origin and version checks, 4 MB cap), registry
  (`AddTool[A, In]`, Huma schemas, validation without echoing values),
  rendering and errors (`*datapanel.Error` keeps its code), and docs
  resources from an `fs.FS` under a URI prefix. `mcpkit.DataTools[A, R,
  L](s, ops datapanel.Ops[A, R], locate func(*Call[A], L) (R, error),
  name func(R) string)` registers the five data tools; `L` is the host's
  locator input (Dock `ServiceInput`, tempoBase `ConnectionInput`), whose
  properties are merged into each tool's schema. Dock's `internal/mcp`
  passes its options and adds its platform tools; its tests pass
  unchanged.

### `apitypes` changes (additive)

- `apitypes.PolicyConnection DataPolicyScope = "connection"`, added to the
  enums of `DataPolicyRule.From`, `DataPolicy.Scope` and
  `DataPermissionStep.Policy`.
- `DataPolicy.Connection string` (`json:"connection,omitempty"`).
- Doc strings that name `dock_reader` or "the environment's name" say
  "the panel's read-only role" and "the environment's or connection's
  name".

Dock never emits the new value or field. `web/openapi.json` and
`web/src/api/schema.d.ts` are regenerated in the same commit.

## 2. The standalone server

### Command and configuration

`cmd/tempobase` uses the standard `flag` package (no Cobra), so nothing
runs at start-up that a subcommand doesn't need.

| Command | Does |
|---|---|
| `tempobase serve [--config FILE]` | The server. |
| `tempobase admin create-user --email E --name N --role R` | Creates a user on the database directly (password from stdin, or a printed set-password link with `--link`). For automation and lost admins. |
| `tempobase admin reset-link --email E` | Prints a one-time reset link. |
| `tempobase admin backup FILE` | `VACUUM INTO` a consistent copy of the database. |
| `tempobase openapi -o FILE` | Writes the OpenAPI spec (`web/tempobase/openapi.json`). |
| `tempobase healthcheck [--url URL]` | Exit 0 when `/api/v1/health` answers; for whoever wants a container health check. |
| `tempobase version` | |

Configuration: an optional TOML file (`--config`, else
`$TEMPOBASE_CONFIG`, else none), then environment variables, which win.

| TOML | Environment | Default | |
|---|---|---|---|
| `listen` | `TEMPOBASE_LISTEN` | `127.0.0.1:7171` (image: `0.0.0.0:7171`) | HTTP address. Put TLS in front (Caddy, a tunnel) for anything public. |
| `data_dir` | `TEMPOBASE_DATA_DIR` | `/var/lib/tempobase` (image: `/data`) | `tempobase.db` and, by default, the key file. |
| `secrets_key_file` | `TEMPOBASE_SECRETS_KEY_FILE` | `<data_dir>/secrets.key` | AES-256 key (`secrets.LoadOrCreate`), created on first start. Point it at a mounted secret to keep it out of volume backups. |
| `base_url` | `TEMPOBASE_BASE_URL` | none: from the request | Used for links (setup, invites, resets, MCP snippet) and to decide `Secure` cookies. |
| `trusted_proxies` | `TEMPOBASE_TRUSTED_PROXIES` | none | CIDRs whose `X-Forwarded-For` and `X-Forwarded-Proto` are believed (rate limits, audit addresses, scheme). |
| `audit_retention_days` | `TEMPOBASE_AUDIT_RETENTION_DAYS` | `90` | Older audit events are deleted (see Audit). |

Port 7171 sits next to Dock's 7070. `GOMEMLIMIT=48MiB` is set by the image
and the example systemd unit.

### Store

`internal/tempobase/tbstore`: SQLite through `modernc.org/sqlite`, the
same setup as Dock's store (WAL, `_txlock=immediate`, `foreign_keys`,
eight kept connections, every query prepared once, `= +?` and `LIMIT +?`,
`EXPLAIN QUERY PLAN` checked, no N+1). Migrations in
`tbstore/migrations/NNNN_*.sql` applied by `PRAGMA user_version` with the
same per-file transaction and re-read as Dock's `migrate`; queries in
`tbstore/queries/*.sql`, sqlc output committed. The first migration:

```sql
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  email TEXT NOT NULL UNIQUE COLLATE NOCASE,
  name TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('viewer', 'editor', 'admin')),
  password_hash TEXT NOT NULL,           -- bcrypt; '' until a link sets it
  disabled_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL
);
CREATE TABLE sessions (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  secret_hash TEXT NOT NULL UNIQUE,      -- sha256 of tbs_...
  created_at TIMESTAMP NOT NULL,
  expires_at TIMESTAMP NOT NULL          -- 30 days, not sliding
);
CREATE INDEX sessions_user ON sessions (user_id);
CREATE TABLE links (                     -- invites and password resets
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('invite', 'reset')),
  email TEXT NOT NULL COLLATE NOCASE,
  role TEXT,                             -- invites only
  user_id INTEGER REFERENCES users (id) ON DELETE CASCADE,  -- resets only
  secret_hash TEXT NOT NULL UNIQUE,
  created_by TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL,
  expires_at TIMESTAMP NOT NULL,         -- invites 7 days, resets 24 hours
  used_at TIMESTAMP
);
CREATE TABLE connections (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,             -- [a-z0-9-], 1-63, used in URLs
  engine TEXT NOT NULL DEFAULT 'postgres',
  host TEXT NOT NULL, port INTEGER NOT NULL, database TEXT NOT NULL, username TEXT NOT NULL,
  tls_mode TEXT NOT NULL CHECK (tls_mode IN ('disable', 'prefer', 'require', 'verify-ca', 'verify-full')),
  ca_pem TEXT NOT NULL DEFAULT '',
  url_secret BLOB NOT NULL,              -- the full URL, sealed
  production INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL, created_by TEXT NOT NULL
);
CREATE TABLE tokens (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,                  -- first characters, for display
  secret_hash TEXT NOT NULL UNIQUE,      -- sha256 of tbt_...
  user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  data_production TEXT, data_non_production TEXT, data_sensitive TEXT,  -- NULL: tokens rule
  created_at TIMESTAMP NOT NULL, expires_at TIMESTAMP, revoked_at TIMESTAMP
);
CREATE INDEX tokens_user ON tokens (user_id);
CREATE TABLE token_connections (         -- none: every connection the policy allows
  token_id INTEGER NOT NULL REFERENCES tokens (id) ON DELETE CASCADE,
  connection_id INTEGER NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
  PRIMARY KEY (token_id, connection_id)
) WITHOUT ROWID;
CREATE TABLE data_policy_rules (         -- connection_id NULL: the default
  id INTEGER PRIMARY KEY,
  connection_id INTEGER REFERENCES connections (id) ON DELETE CASCADE,
  subject TEXT NOT NULL, kind TEXT NOT NULL, level TEXT NOT NULL, sensitive TEXT NOT NULL
);
CREATE UNIQUE INDEX data_policy_rules_scope ON data_policy_rules (coalesce(connection_id, 0), subject, kind);
CREATE TABLE data_access (
  connection_id INTEGER PRIMARY KEY REFERENCES connections (id) ON DELETE CASCADE,
  engine TEXT NOT NULL, server_version TEXT NOT NULL,
  reader_secret BLOB NOT NULL, writer_secret BLOB NOT NULL,
  grants_synced_hash TEXT NOT NULL DEFAULT '',
  set_up_at TIMESTAMP NOT NULL, set_up_by TEXT NOT NULL
);
CREATE TABLE sensitive_columns (
  connection_id INTEGER NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
  schema_name TEXT NOT NULL, table_name TEXT NOT NULL, column_name TEXT NOT NULL,
  PRIMARY KEY (connection_id, schema_name, table_name, column_name)
) WITHOUT ROWID;
CREATE TABLE saved_queries (
  id INTEGER PRIMARY KEY,
  connection_id INTEGER NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  name TEXT NOT NULL, sql TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL,
  UNIQUE (connection_id, user_id, name)
);
CREATE TABLE changesets (
  id INTEGER PRIMARY KEY,
  connection_id INTEGER NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
  author TEXT NOT NULL, author_kind TEXT NOT NULL,
  user_id INTEGER REFERENCES users (id) ON DELETE SET NULL,
  token_id INTEGER REFERENCES tokens (id) ON DELETE SET NULL,
  status TEXT NOT NULL, message TEXT NOT NULL DEFAULT '',
  changes TEXT NOT NULL, sql TEXT NOT NULL,
  rows_affected TEXT NOT NULL DEFAULT '', conflicts TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL, expires_at TIMESTAMP NOT NULL,
  decided_at TIMESTAMP, decided_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX changesets_connection ON changesets (connection_id, id);
CREATE INDEX changesets_pending ON changesets (expires_at) WHERE status = 'pending';
CREATE TABLE audit_details (
  id INTEGER PRIMARY KEY,
  sql TEXT NOT NULL, rows INTEGER NOT NULL, duration_ms INTEGER NOT NULL, outcome TEXT NOT NULL
);
CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY,
  created_at TIMESTAMP NOT NULL,
  actor TEXT NOT NULL, actor_kind TEXT NOT NULL,
  user_id INTEGER, token_id INTEGER,     -- no FKs: events outlive what they name
  action TEXT NOT NULL,
  connection_id INTEGER,                 -- likewise; target keeps the name
  target TEXT NOT NULL, summary TEXT NOT NULL,
  detail_id INTEGER REFERENCES audit_details (id) ON DELETE CASCADE
);
CREATE INDEX audit_events_connection ON audit_events (connection_id, id);
CREATE INDEX audit_events_user_history ON audit_events (action, connection_id, user_id, id);
CREATE INDEX audit_events_token_history ON audit_events (action, connection_id, token_id, id);
CREATE INDEX audit_events_created ON audit_events (created_at);
```

### Auth

| Topic | Decision |
|---|---|
| Users | Email, name, bcrypt password (cost 10, at most 2 hashes at once, unknown emails cost the same), at least 12 characters. One role: `viewer`, `editor` or `admin`. |
| First admin | With no users, `serve` generates a one-time setup code (128 bits, memory only) and logs `no users yet: open <base>/setup#code=<code> to create the first admin`. `POST /api/v1/setup` with the code creates the admin and signs them in; the code is gone after that or on restart. The fragment keeps it out of proxy logs. `tempobase admin create-user` is the scripted path. |
| Sessions | Cookie `tempobase_session` (`tbs2_` + 32 random bytes), `HttpOnly`, `SameSite=Strict`; behind HTTPS it is `Secure` and named `__Host-tempobase_session`, which a browser accepts only from tempoBase's own hostname, so an app on a sibling hostname can't set one (`httpkit.CookieName`, the same naming as Dock's). Stored as a sha256 hash; 30 days, not sliding (no write per request). Logout deletes it; a password change ends the user's other sessions. (2026-11-17: the prefix is new. Sessions from before it, `tbs_`, keep working under the bare name until they expire; a `tbs2_` session is never accepted there.) |
| API tokens | `tbt_` + 32 random bytes, shown once, stored hashed. Every user creates their own (admins see all). Optional expiry, optional connection list, optional data override. Bearer only. Disabling or deleting the user ends their tokens and sessions at once (checked in the lookup join). |
| Invites and resets | No email. An admin creates an invite (email, role) or a reset link and copies `<base>/link#<code>` to the person; opening it sets name and password and signs in. Invites live 7 days, resets 24 hours, single use. |
| CSRF and headers | `httpkit.Guard`, as Dock: `Sec-Fetch-Site` or Origin must be same-origin for cookie requests, JSON bodies only, CSP, `no-store` on `/api`. |
| Rate limits | `httpkit.Limiter` with Dock's login limits per client IP and per email; failures audited as `user.login_failed` (never the password). |
| OIDC | Not in v1. Later, as one generic OIDC provider (authorization code with PKCE) that signs in existing users by verified email, configured in the TOML file. Trusted-header SSO (a proxy's `Remote-Email`) is considered with it. |

Each request authenticates with one indexed query (session or token
joined to its user).

### Connections

| Topic | Decision |
|---|---|
| Adding | Admins paste a `postgres://` or `postgresql://` URL and choose a name and the production flag. Query parameters: only `sslmode` (`disable`, `prefer` (default), `require`, `verify-ca`, `verify-full`); anything else is refused with the list. A CA for `verify-*` goes in `ca_pem`; otherwise system roots (the image ships them). No Unix sockets. |
| Testing | `POST /api/v1/connections/test` dials and runs `Engine.Check` as the URL's user without saving: server version, whether setup can create roles, or what failed. The form offers it before saving. |
| At rest | The full URL sealed with the key file (`url_secret`). Host, port, database, user and TLS mode are kept in the clear for listing. Responses never contain the password: `url` comes back without it. |
| Dialing | `net.Dialer` (10 s timeout) to host and port, TLS from the mode. The engine's limits and idle close apply unchanged: at most 2 connections per connection and role, 8 in total, closed 60 s after last use. |
| Setup | The panel's normal setup with the URL's user as owner: creates or rotates `tempobase_reader`, `tempobase_writer`, `tempobase_reader_masked`. Needs `CREATEROLE` (managed Postgres owners have it). Until setup, the panel shows "Set up data access" to admins, as in Dock. A direct mode without roles is rejected: 0016's reasons hold. |
| Changing | A changed URL or flag applies at once and drops cached connections (`Panel.Forget`). If the database changed, Status says setup is needed. |
| Removing | Deletes tempoBase's state for it (setup record, marks, saved queries, changesets, policy rules, token links); audit events stay. The roles stay in the database; the confirm dialog shows the `DROP ROLE` statements. |

### Access policy

0016 section 4 applies unchanged, mapped:

| Dock | tempoBase |
|---|---|
| Scopes default, project, environment | Scopes default, connection |
| Environment kind | The connection's production flag (`production` or `non_production`) |
| Users with role `read` (subject `role:read`) | `viewer` |
| Users with role `deploy` (subject `role:deploy`) | `editor` |
| Admins (scope admin) | `admin`: everything, approve, set up, mark sensitive, manage the policy, users and connections |
| Tokens with a scope, capped by scope and creator | Tokens are agents with no scope of their own: the `tokens` rule or their override, capped by their creator's current role unless the creator is an admin. Never admin, never approve or manage. Only propose on production. |
| Token limits: project, environment | Token limits: a list of connections |
| Token override set by admins | Set by the token's creator or an admin, at creation or with `PATCH /tokens/{id}` |

The wire keeps 0016's subject values (`role:read`, `role:deploy`) so the
policy types stay shared; tempoBase's API docs and UI label them viewers
and editors.

Built-in defaults (a viewer is there to view; editors are trusted
people; agents get nothing until granted):

| Subject | Non-production | Production | Sensitive |
|---|---|---|---|
| `role:read` (viewers) | `read` | `read` | masked |
| `role:deploy` (editors) | `write` | `write` (commits confirm the connection's name) | masked |
| `tokens` | `none` | `none` | masked |

A connection is **invisible** to a caller whose level there is `none`
(not admin) or that a token's connection list leaves out: it is missing
from lists and every call on it is 404. This replaces the TypeScript
app's per-user connection roles; per-user subjects can come later, to
Dock and tempoBase together.

### Audit

The data events are 0016's, written through `Host.Audit`
(`data.query`, `data.browse`, `data.reveal`, `data.commit`,
`data.propose`, `data.approve`, `data.reject`, `data.setup`,
`data.policy`). tempoBase adds `user.login`, `user.login_failed`,
`user.create`, `user.update`, `user.delete`, `link.create`, `link.use`,
`token.create`, `token.update`, `token.revoke`, `connection.create`,
`connection.update`, `connection.delete`.

- The audit log is admin-only. People see their own queries in the SQL
  tab's history (the panel's `QueryHistory`).
- Retention without a timer: when an event is written and the last prune
  is over an hour ago (in memory), events older than
  `audit_retention_days` are deleted, at most 1000 per pass, with their
  details. One indexed delete per hour at most, only while something is
  being audited.

### MCP

`POST /api/v1/mcp`, built with `mcpkit` exactly as 0017 describes
(stateless, bearer tokens only, no cookies, progress streams, caps).

| Tool | Does |
|---|---|
| `whoami` | Token name, its user, its connection limit |
| `list_connections` | Connections the token may use: name, production, its level and sensitive visibility |
| `data_status`, `data_schema`, `data_rows`, `data_query`, `propose_data_changes` | Dock's data tools, same names, descriptions, caps and rules; input `connection` (optional when the token is limited to one) |

Resources: `tempobase://docs/<page>` from `docs/tempobase` (`tbdocs`).
The instructions summarize the safety model: access comes from the
policy, writes are proposals a person approves, tokens never write
production.

## 3. HTTP API

Prefix `/api/v1`. Errors are `apitypes.Error` problem responses with
Dock's codes. tempoBase-only types live in `internal/tempobase/tbtypes`;
the data and policy types are `apitypes`' own. `{connection}` is the
connection's name.

| Method and path | Auth | Request | Response |
|---|---|---|---|
| `GET /health` | none | | `{status}` |
| `GET /version` | none | | `{version}` |
| `GET /setup` | none | | `{needed}` |
| `POST /setup` | setup code | `{code, email, name, password}` | `Me`, session cookie |
| `POST /auth/login` | none | `{email, password}` | `Me`, session cookie |
| `POST /auth/logout` | session | | 204, cookie cleared |
| `GET /links/{code}` | link code | | `{kind, email, expires_at}` |
| `POST /links/{code}` | link code | `{name, password}` (`name` for invites) | `Me`, session cookie |
| `GET /me` | any | | `Me {kind, name, email, role, token_connections}` |
| `POST /me/password` | session | `{current_password, new_password}` | 204, new cookie |
| `GET /users` | admin | | `[]User {id, email, name, role, disabled, created_at}` |
| `PATCH /users/{id}` | admin | `{name?, role?, disabled?}` | `User` |
| `DELETE /users/{id}` | admin | | 204 (not the last admin) |
| `POST /users/{id}/reset-link` | admin | | `Link {id, kind, email, url, expires_at}` (`url` only here) |
| `GET /invites` | admin | | `[]Link` (open invites, no `url`) |
| `POST /invites` | admin | `{email, role}` | `Link` with `url` |
| `DELETE /invites/{id}` | admin | | 204 |
| `GET /tokens` | any | | `[]Token {id, name, prefix, user, connections, data, created_at, expires_at, revoked_at}`: own, or all for admins |
| `POST /tokens` | session | `{name, connections, data?, expires_in_days?}` | `{token: Token, secret}` |
| `PATCH /tokens/{id}` | creator or admin | `apitypes.UpdateTokenRequest` (Dock's) | `Token` |
| `DELETE /tokens/{id}` | creator or admin | | 204 |
| `GET /connections` | any | | `[]Connection {name, engine, host, port, database, user, tls, production, url, created_at, created_by, access: apitypes.DataPermission, pending}`; no database I/O, `pending` (open changesets) for admins in one grouped query |
| `POST /connections` | admin | `{name, url, production, ca_pem?}` | `Connection` |
| `POST /connections/test` | admin | `{url, ca_pem?}` | `{ok, server_version, can_set_up, error}` |
| `GET /connections/{connection}` | sees it | | `Connection` |
| `PATCH /connections/{connection}` | admin | `{name?, url?, production?, ca_pem?}` | `Connection` |
| `DELETE /connections/{connection}` | admin | | 204 |
| `GET`, `PUT /data-policy` | any; `PUT` admin | `apitypes.UpdateDataPolicyRequest` | `apitypes.DataPolicy` (scope `default`) |
| `GET`, `PUT /connections/{connection}/data-policy` | sees it; `PUT` admin | `apitypes.UpdateDataPolicyRequest` (its kind only) | `apitypes.DataPolicy` (scope `connection`, `connection` set) |
| `GET /audit` | admin | `?connection=&action=&before=&limit=` | `[]AuditEvent {id, actor, actor_kind, action, connection, target, summary, detail_id, created_at}` |
| `GET /audit/{id}/detail` | admin | | `apitypes.AuditDetail` |
| `POST /mcp` | token | JSON-RPC | MCP |

Data endpoints, under `/connections/{connection}/data`, with Dock's
query parameters, bodies, response types, statuses and NDJSON framing
(0016 section 8):

| Method and path | Response |
|---|---|
| `GET /data` | `apitypes.DataInfo` (`detected_by: setting`, `used_by: []`) |
| `POST /data/setup` | `apitypes.DataInfo` |
| `GET /data/schema` | `apitypes.DataSchema` |
| `GET /data/tables/{table}/rows?order&after&limit&filter&count&full&reveal` | `apitypes.DataPage` |
| `GET /data/tables/{table}/references?key&limit` | `[]apitypes.DataReferences` |
| `POST /data/query` | NDJSON of `apitypes.DataQueryFrame` |
| `GET /data/history?before&limit` | `[]apitypes.DataHistoryEntry` |
| `GET`, `POST /data/saved-queries`; `PUT`, `DELETE /data/saved-queries/{id}` | `apitypes.DataSavedQuery` |
| `GET /data/changesets?status`; `POST /data/changesets` | `apitypes.DataChangeset` |
| `GET /data/changesets/{id}`; `POST /data/changesets/{id}/commit`, `/reject` | `apitypes.DataChangeset` (`confirm` is the connection's name) |
| `PUT /data/sensitive-columns` | `[]apitypes.DataColumnRef` |

Everything outside `/api/` is the embedded UI (`httpkit.UIHandler`).
There is no event stream: nothing in tempoBase changes behind a person's
back often enough to need one. The changeset list refetches when the
window regains focus; nothing polls.

A contract test (`internal/tempobase/tbhttp/contract_test.go`) builds
both specs and checks that every schema reachable from a data endpoint
is identical in Dock's and tempoBase's, so `features/data` can't drift.

## 4. Web build

| Topic | Decision |
|---|---|
| Layout | `web/` stays Dock's app. `web/tempobase/` holds tempoBase's: `index.html`, `src/main.tsx`, `src/routes/`, `src/api/` (client and `schema.d.ts` from `web/tempobase/openapi.json`), `src/data/source.ts`, `embed.go` (`tbweb`, committed), `dist/`. |
| Builds | `web/vite.config.ts` unchanged. `web/vite.tempobase.config.ts`: root `tempobase`, `publicDir: "../public"` (fonts and the `.gitkeep` that keeps `dist/` in git), out `tempobase/dist`, TanStack router plugin with `routesDirectory` and `generatedRouteTree` under `tempobase/src`, dev proxy `/api` to `127.0.0.1:7171`. |
| Scripts | `pnpm build` builds both; `build:dock`, `build:tempobase`, `dev:tempobase`; `gen:api` and `check:api` cover both specs. `tsconfig` and Biome include `tempobase/`. |
| Sharing | tempoBase imports from `web/src` only: `features/data/**`, `ui/kit.tsx`, `ui/format.ts`, `ui/status.ts`, `shell/keys.ts`, `index.css`. Nothing in `web/src` imports `web/tempobase`. |
| Bound source | `DataSource` is bound to one database: `serviceSource(ref)` (Dock, `features/data/source.ts`) and `connectionSource(name)` (`web/tempobase/src/data/source.ts`). It carries `key` (query key prefix), `cli` (flags for command hints, absent in tempoBase), `roles` (role names in messages) and `confirmName`, and its methods drop the ref argument. Hooks and components stop taking `serviceRef`. The NDJSON reader moves to `features/data/ndjson.ts`. |
| Policy UI | `PolicyPage.tsx` splits into a shared `PolicyEditor` (one scope's rules and effective rules; props: the policy query and save, `subjectNames`, `fromText`) and Dock's page with the project tree. tempoBase passes `{"role:read": "viewers", "role:deploy": "editors", tokens: "tokens"}`. `TokenData` takes `{data, onSave}` instead of a Dock token. |
| Types | `features/data/types.ts` keeps deriving from Dock's schema; the contract test guarantees tempoBase's are identical. |

tempoBase shell routes (file routes, each its own chunk):

| Route | Shows |
|---|---|
| `/login`, `/setup`, `/link` | Sign in; first admin (code from the fragment); invite or reset (code from the fragment). |
| `/` | Connections: one dense line each (name, `user@host/db`, production in the warning colour, your level, pending changes for admins). Admins: add. |
| `/connections/new` | URL, name, production, CA (collapsed); Test, then Save. |
| `/c/$connection` | The Data panel; tab, table, order and filters in the search params, as Dock's Data route. |
| `/c/$connection/settings` | Admins: URL and production, data access status and setup, the connection's policy, sensitive columns, remove. |
| `/settings/people`, `/settings/tokens`, `/settings/policy`, `/settings/audit`, `/settings/account` | Users and invites; tokens with the MCP client snippet; the default policy; the audit log with SQL details; your password. |

Design rules as Dock's: dark, true black, white text, dense, monospace
data, no card or pill chrome, no light-gray subtitle lines, minimal copy,
no em dashes, static state text instead of spinners, no repainting
animations.

Budgets: tempoBase initial JS (entry and modulepreloads, gzip) **≤ 120
KB** (expected ~90: React, router, query, the shell); CSS shares Dock's
≤ 30 KB; the panel chunk keeps 0016's ≤ 250 KB and CodeMirror stays a
separate chunk loaded by the SQL tab.

## 5. Packaging and performance

| Artifact | Decision |
|---|---|
| Binary | `make build-tempobase`: `CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -X .../version.Version=...' -o bin/tempobase ./cmd/tempobase`, UI from `web/tempobase/dist` embedded. |
| Image | `deploy/tempobase/Dockerfile`: `gcr.io/distroless/static-debian12:nonroot` pinned by digest (CA roots, nonroot user), the release binary for `TARGETARCH` copied in, `/data` created owned by 65532 in a busybox stage and copied with `--chown`, `ENV TEMPOBASE_LISTEN=0.0.0.0:7171 TEMPOBASE_DATA_DIR=/data GOMEMLIMIT=48MiB`, `VOLUME /data`, `EXPOSE 7171`, `ENTRYPOINT ["/tempobase"]`, `CMD ["serve"]`. No `HEALTHCHECK`: it would wake the server every interval. |
| Examples | `deploy/tempobase/compose.yaml` (the image and a volume) and `deploy/tempobase/tempobase.service` (systemd, `GOMEMLIMIT=48MiB`, `DynamicUser`, `StateDirectory=tempobase`). |

Performance budgets, checked by `make perf` (new checks
`tempobase-binary`, `tempobase-daemon`, `tempobase-ui`, run by default
when `bin/tempobase` exists; `make perf` builds it):

| What | Budget | Expected |
|---|---|---|
| Binary (linux/amd64, UI embedded) | ≤ 20 MB, then baseline + 10% (`tempobase_binary_bytes` in `baseline.json`) | 19.9 MB (section 8, B as built) |
| Idle anonymous RSS, after start, a login and a connection list | ≤ 20 MB | ~10 MB |
| Idle CPU, 30 s window | ≤ 1 clock tick | 0 |
| Idle wake-ups | ≤ 0.1/s | ~0.01/s (Go's forced GC) |
| API read p95, 8 clients, 50 connections and 20 users seeded (`/me`, `/connections`) | ≤ 10 ms | < 2 ms |
| Database connections after use (with `-tempobase-pg URL`) | 0 sockets to Postgres 65 s after the last call | 0 |
| Initial JS (gzip) | ≤ 120 KB | ~90 KB |
| Import rule | no Dock daemon packages (section 1) | |

The CI perf job gets a `postgres:17` service so the connection check
runs there. New benchmarks: `datapanel.BenchmarkDecide`,
`tempobase.BenchmarkAuthenticateSession`, `tbhttp.BenchmarkListConnections`.

Periodic work in tempoBase, added to performance.md's table: the data
connection idle close (the same engine timer) and the audit prune
(inline, at most hourly, no timer). Nothing else: no statistics tick, no
session sweeper (expired sessions are deleted when presented), no
changeset timer (expired lazily, as in Dock).

## 6. Releases

- One version for both products: a `v*` tag releases `dock` and
  `tempobase` from the same commit.
- `scripts/release/build.sh` adds `tempobase-linux-amd64`,
  `tempobase-linux-arm64` and `tempobase-darwin-arm64` (local trials) to
  `dist/` and `SHA256SUMS`; it requires `web/tempobase/dist/index.html`.
- `.github/workflows/release.yml` gains a step after the GitHub Release:
  `docker buildx build --platform linux/amd64,linux/arm64 --push` of
  `deploy/tempobase/Dockerfile` from `dist/`, tagged
  `ghcr.io/nordtempo/tempobase:<version>` and, for tags without a `-`,
  `:latest`. The job gets `packages: write`.
- Where users find it: the **`NordTempo/tempoBase` repository keeps a
  README only**, pointing at the image, this repository's releases and
  `docs/tempobase`. The owner tags the TypeScript code (`ts-final`),
  replaces it with that README and archives the repository. Releases are
  not mirrored: one place to publish, no cross-repository token. If this
  repository stays private when tempoBase opens up, mirroring is
  revisited then. Nobody but the owner touches that repository.

## 7. From the TypeScript tempoBase

Ported (to the shared panel where marked, so Dock gets them too):

| Feature | Where |
|---|---|
| Invites and first-admin bootstrap | tempoBase auth (links instead of signup; setup code instead of "first signup wins") |
| Connection form: paste a URL, test before save, TLS mode, production flag | tempoBase shell |
| Agent tiers and the MCP client snippet on the tokens page | tempoBase tokens (tiers are the token override) |
| Pending proposals per connection | tempoBase connection list |
| Tables overview (every relation with rows, columns, key, references, referenced by) when no table is open | shared panel, follow-up |
| Recent tables as tabs and per-table column widths and hidden columns, in `localStorage` | shared panel, follow-up |
| Run the statement under the cursor (statement splitting that knows quotes, dollar quotes and comments) | shared SQL editor, follow-up |
| CSV export of a result, client-side from the received frames | shared SQL view, follow-up |
| Revert a committed changeset: the engine returns old rows (`RETURNING`) at commit, stored as the inverse; Revert previews it as a new changeset | shared panel and engine, follow-up |

Dropped:

| Feature | Why |
|---|---|
| TanStack Start, better-auth, Drizzle and a Postgres app database, postgres.js pools, Zustand, Radix, lucide, Geist fonts | Replaced by Dock's stack; SQLite and the shared engine. |
| Per-user per-connection roles | The policy with connection overrides and invisibility at level `none`. |
| Token `writable_tables` allow-lists and per-token row limits | Policy levels and the MCP caps cover them; per-table rules can come to the policy later. |
| Writes from the SQL editor | Every write is a changeset (0016). |
| ER diagram with React Flow and elkjs | Too heavy for the bundle budgets; a light SVG diagram stays on 0016's phase 5 list. |
| A separate query history table | History is the caller's `data.query` audit events. |
| Command palette | The panel's single-key shortcuts and the browser's own navigation suffice. |

## 8. Work breakdown

Four workstreams, started together. Each keeps `go build ./...`,
`go vet ./...`, `go test ./...`, `make lint` and (for web) `pnpm check`
green on its own branch rebased on main. Generated files
(`web/openapi.json`, `web/src/api/schema.d.ts`,
`web/tempobase/openapi.json`, `web/tempobase/src/api/schema.d.ts`, sqlc
output, route trees) are never merged by hand: take either side and run
`make generate`.

### A. Shared packages and Dock's adapter

Owns: `internal/datapanel/**`, `internal/httpkit/**`, `internal/mcpkit/**`,
`internal/mcp/**`, `internal/core/data*.go`, `internal/core/errors.go`,
`internal/core/streams.go`, `internal/core/ratelimit.go`, the panel and
limiter lines of `internal/core/core.go`, `auth.go`, `cli_login.go` and
`git.go` (the limiter becomes `httpkit.Limiter` with `Allow` and
`Exhausted`),
`internal/api/data.go`, `internal/api/ui.go`, `internal/api/guard.go`
(and their tests), `internal/data/**`, `internal/apitypes/data.go`,
`web/openapi.json`, `web/src/api/schema.d.ts` (regenerated only).

In this order, each step merged on its own so B unblocks early:

1. `datapanel` policy: `Decide` and the policy helpers; core calls them.
2. `httpkit` (UI handler, guard, limiter) and `datapanel/datahttp`.
3. Engine: `pg.Options` (roles, app name), TLS on `Target`, `Check`.
4. The `Panel` and `internal/core/data_host.go`; `core.Error` alias.
5. `mcpkit` and Dock's `internal/mcp` on top of it.
6. `apitypes` additions, regenerated spec and client.

Done when Dock's tests pass unchanged (moved unit tests aside), `make
perf` shows no regression for Dock, and `datapanel` has its own tests
with a fake host.

As built (2026-10-22), beyond the sketch above:

- `datapanel.Words` also carries `SetupAction`, `ConfirmWhat` and
  `Database` (setup reasons, the commit confirm message, API docs).
- `Host.SavedQueryOwner` may return the host's own error instead of 0,
  so Dock keeps "log in with `dock login`". Dock finds a `dock login`
  token's user by the token's ID.
- Core checks `Manage` before `Setup` and `SetSensitiveColumns` to keep
  Dock's admin messages; the Panel checks it again with a neutral one.
- Two messages name the database instead of the environment: the commit
  confirm error ("shop/production/db is production: confirm with the
  environment's name (production)") and `propose_data_changes`' refused
  commit on production.
- `mcpkit.Options[A]` takes the server info, instructions,
  `Authenticate`, the 401 realm and message, `Tools` (registered on the
  first request), `Docs{FS, URI, IndexDescription}` and error `Hints`.
  Dock's tools/list and initialize are byte-identical to before.

### B. The tempobase server

Owns: `cmd/tempobase/**`, `internal/tempobase/**` (host, auth, users,
links, tokens, connections, policy, audit, `tbstore/**`, `tbtypes/**`,
`tbhttp/**`, MCP wiring), `web/tempobase/openapi.json`.

Starts at once with config, the store and its migration, auth, users,
links, tokens, connections (URL parsing, sealing, TLS configs), the
non-data endpoints and the `openapi` command, and implements
`datapanel.Host[*conn]` against the committed interface. Data endpoints
follow A.1 to A.4, MCP follows A.5. An integration test
(`-tags integration`, Postgres in Docker) drives setup, rows, query,
masking, propose and commit through HTTP; the contract test checks the
shared schemas.

As built (2026-10-22):

- The first migration has more indexes than section 2 lists: one for
  every foreign key a delete cascades through (`links`, `sessions`,
  `token_connections`, `data_policy_rules`, `saved_queries`,
  `changesets`, `audit_events.detail_id`), `sessions (expires_at)`,
  open invites, and `audit_events (action, id)` and `(connection_id,
  action, id)` for the audit filters. Every query's plan is a `SEARCH`
  but the small, whole-table lists (users, tokens, connections, rules).
- `Connection` carries `custom_ca` (the CA itself never comes back).
  Audit also records `user.password_failed` and `link.delete`.
- `GET /audit/{id}/detail` takes the event's `detail_id`, as Dock's
  does, not the event's ID.
- The data operations have Dock's operation IDs. Besides the schemas,
  the contract test compares their query parameters, the policy
  operations, the audit detail and the token update request, and checks
  every `{param}` in tempoBase's paths is declared.
- The MCP endpoint is `internal/tempobase/tbmcp`. Token authentication
  also returns the token's connection names, so `/me` and the tools'
  default connection need no extra query.
- The binary measures 19.9 MB stripped with its UI (`make
  build-tempobase` after `pnpm build:tempobase`; 18.9 MB without it),
  just under its 20 MB budget. Most of it is the pure-Go SQLite (2 MB of
  code and its tables), Huma (1.3 MB) and pgx (1.1 MB, with
  `golang.org/x/text` for SCRAM); tempoBase's own packages have nothing
  worth trimming. When it grows, embedding the UI pre-gzipped (1 MB raw,
  about 0.3 MB compressed) is the first lever, before raising the budget.
- `make perf`: idle anonymous RSS 12.0 MB after start, a sign-in and a
  connection list, 0 CPU ticks and 0.03 wake-ups/s in 30 s; API p95
  0.4 ms for `/me` and 1.8 ms for `/connections` (50 connections, 20
  users); UI initial JS 113 KB gzipped.

### C. Web

Owns: everything under `web/` except the generated specs and
`web/src/api/schema.d.ts`: `web/src/features/data/**`, Dock's data and
policy routes, `web/package.json`, `web/vite.tempobase.config.ts`,
`web/tsconfig.json`, `web/biome.json`, `web/tempobase/**` (but
`openapi.json`).

1. Bound `DataSource`, `PolicyEditor`, `TokenData` props, `ndjson.ts`;
   Dock looks and behaves the same.
2. The second Vite entry and the shell pages against section 3, with a
   typed in-memory source for development.
3. Switch to the generated client once B commits
   `web/tempobase/openapi.json`.
4. Follow-up after 1 to 3: the panel ports of section 7.

Depends only on this ADR's contract until step 3.

### D. Packaging, performance, releases and docs

Owns: `Makefile`, `scripts/perf/**`, `scripts/release/**`,
`.github/workflows/**`, `deploy/tempobase/**`, `docs/tempobase/**` (the
guide; `embed.go` committed), `docs/performance.md`,
`docs/architecture.md`, `README.md`.

1. The guide: install (binary, Docker, systemd), first run, connections
   and setup, people and the policy, agents and MCP, backups (the data
   directory and key file).
2. Make targets (`build-tempobase`, `tempobase-deps`, `web` building
   both) and the import check in CI.
3. Release script, Dockerfile, examples, workflow step.
4. Perf checks and budgets (section 5) and the performance.md rows.

Codes against the paths, binary names and endpoints in this ADR; the
perf and image checks go green once B lands.

### Order and integration

| Needs | From |
|---|---|
| B's data endpoints and Host wiring | A.1 to A.4 (the interface is already committed) |
| B's MCP | A.5 |
| B's UI serving and guard | A.2 |
| C step 3 | B's spec |
| D's perf and image checks passing | B's binary, C's tempoBase build |

Merge order when ready: A's steps as they land, then B, C and D in any
order. The TypeScript app is retired (section 6) after the first release
that ships `tempobase`.

## Consequences

- One implementation of the panel's rules serves both products; a fix to
  masking, changesets or the policy lands in both.
- Dock's core loses about 1 500 lines of data glue to `datapanel` and
  gains a small adapter. Its API changes only by one enum value and one
  optional field it never emits.
- tempoBase is a small static binary (no Docker client, no BuildKit), one
  SQLite file and one key file, and costs nothing while idle.
- The generic `Host[D]` and `Ops[A, R]` add type parameters to the
  panel's signatures; in exchange no `any` crosses the boundary.
- A database can be managed by Dock and tempoBase at once without either
  breaking the other's roles.
- tempoBase users get no email features (invites are links) and no SSO
  until OIDC lands.
- Releases carry both products, so a tempoBase-only fix ships with a Dock
  version bump.

## Alternatives considered

| Alternative | Why not |
|---|---|
| Keep the TypeScript tempoBase as the standalone product | Two implementations of the same safety rules, two stacks and two UIs to keep in step. |
| tempoBase as a mode of the `dock` binary (`dock serve --tempobase`) | Ships Dock's daemon (Docker client, BuildKit, node runtime) to people who only want a database viewer; a bigger binary and attack surface for nothing. |
| Share Dock's SQLite schema and store package | Dock's tables hang off services, environments and its audit; tempoBase would carry Dock's whole schema and migrations. |
| A non-generic Host with `any` handles | Type assertions on every call; the type parameter costs a few brackets. |
| Same role names (`dock_reader`) in both products | One product's setup would rotate the other's passwords on a shared database. |
| One shared data handler registration | Huma needs path parameters as fields of the input type and Go can't embed a type parameter; the handlers are thin, the helpers in `datahttp` are shared. |
| Mirror releases to `NordTempo/tempoBase` | A second publishing path and token for the same files. |
| SSE in tempoBase | No event that needs it in v1; focus refetch of one list costs nothing at idle. |
