# Data

Browse, query and change the data in a database service: the **Data** tab
on the service page, or `dock db` in the terminal. Both use the same API
and the same permissions, and everything is in the audit log. The design
is [ADR 0016](../decisions/0016-database-panel.md).

Postgres 14 or newer for now. MySQL/MariaDB and Redis come later.

## Is it a database?

Dock detects Postgres from the image (`postgres`, `postgis/postgis`,
`pgvector/pgvector`, `timescale/timescaledb*`, `bitnami/postgresql`,
`supabase/postgres`) or from the template that created the service. For
anything else, say so:

```sh
dock db settings --database postgres          # or none, or detect
dock db settings --url-variable DATABASE_URL  # where the connection URL is
```

Settings apply at once and never create a release. Connection details come
from the active release: the variable `--url-variable` names (e.g.
`api.DATABASE_URL` for another service's), else the service's own
`DATABASE_URL`, else `POSTGRES_USER`, `POSTGRES_PASSWORD` and `POSTGRES_DB`.
The panel always dials the service's own container on its private network;
the database never gets a public port.

## Set up access

Dock never browses as the database owner. An admin creates two
least-privilege roles once (template databases get them automatically):

```sh
dock db setup          # asks first; --yes skips the question
dock db status         # engine, version, status, your access and why
```

`dock_reader` reads everything (`pg_read_all_data`, read-only by default) and
runs all browsing and SQL. `dock_writer` can change rows but not the schema
and only commits changesets. Their passwords are generated, stored encrypted
in Dock and never shown; running setup again rotates them. They show up in
`\du`. Row-level security still applies to them.

## Browse and query

```sh
dock db tables
dock db schema orders                                  # columns, keys, indexes
dock db rows orders -w status=paid -w total=gt:100 --order -created_at
dock db rows orders --after <cursor>                   # the next page
dock db query "select status, count(*) from orders group by 1"
dock db query -f report.sql --csv > report.csv
dock db history                                        # your past queries
dock db saved save open-orders "select * from orders where status = 'open'"
dock db saved run open-orders
```

- Pages are keyset pages (100 rows, at most 1 000), never `OFFSET`; the
  cursor for the next one is printed on stderr. Row counts are estimates
  (`~N`); tables never analysed are counted exactly when small, else `?`.
  `--count` counts exactly.
- Filters: `col=value`, `col=op:value` (`eq ne lt lte gt gte like ilike in`),
  `col=is_null`, `col=not_null`. Anything fancier is SQL.
- `query` runs one statement in a read-only transaction: 1 000 rows by
  default (`-n`, at most 100 000), a 10 s timeout (`--timeout`, at most
  60 s), at most 64 MB, cells over 8 KB cut. Results stream as they arrive;
  `--json` prints the API's NDJSON frames (header, rows, then end or error).
- Writes in `query` fail (exit 3). Change data with changesets.

## Change data

Changes are changesets: row inserts, updates and deletes committed in one
transaction. Each update and delete matches the primary key and the values
you saw, so if someone changed the row meanwhile nothing is written and the
conflict is listed (exit 5). At most 500 rows per changeset; tables need a
primary key.

```json
{
  "message": "fix the total of order 1041",
  "changes": [
    {"op": "update", "table": "public.orders", "key": {"id": "1041"},
     "old": {"total": "120.00"}, "new": {"total": "102.00"}},
    {"op": "delete", "table": "public.carts", "key": {"id": "77"}}
  ]
}
```

Values are strings in Postgres text format, or `null`.

```sh
dock db apply -f changes.json --dry-run                 # the exact SQL, nothing stored
dock db apply -f changes.json                           # commit (level write) or propose
dock db apply -f changes.json --confirm production      # production asks for its name
dock db changes list --status pending
dock db changes show 12
dock db changes commit 12 --confirm production          # admins approve proposals
dock db changes reject 12 --reason "use the refund flow"
```

With level `propose`, `apply` stores the changeset as pending and an admin
commits or rejects it. Pending changesets expire after 7 days.

## Who may do what

A **data access policy** decides, per subject and environment kind, a level
and whether sensitive columns are visible:

| Level | Allows |
|---|---|
| `none` | Nothing |
| `schema` | Tables, columns, relations; no rows |
| `read` | Browsing and read-only SQL |
| `propose` | Changesets an admin approves |
| `write` | Committing one's own changesets |

Subjects are users with role `read`, users with role `deploy`, and `tokens`.
Admins can always do everything, and only they approve proposals, run
setup, mark sensitive columns and change the policy. Built-in defaults:

| Subject | Non-production | Production |
|---|---|---|
| `role:read` | `none` | `none` |
| `role:deploy` | `read` | `none` |
| `tokens` | `none` | `none` |

Sensitive columns are masked for all of them. Change the policy as a
default, per project or per environment; the most specific rule wins:

```sh
dock db policy show                                   # the default
dock db policy show -p shop -e production             # what applies there, and from where
dock db policy set deploy.production=read -p shop
dock db policy set tokens.non-production=read,masked
dock db policy set deploy.production=inherit -p shop  # remove a rule
```

`dock db status` shows your effective access with the steps that decided it.

## Agents

Agents get nothing until you grant it, either in the policy's `tokens` rule
or on the token itself:

```sh
dock token create claude-shop --scope deploy --project shop \
  --data-non-production read --data-production none
dock token update 7 --data-non-production propose
dock token update 7 --data-clear                      # follow the policy again
```

A token never gets more than a user with its scope would, and on production
`write` acts as `propose`: an agent's changes to production data always wait
for a human to approve the exact SQL. Agents use the same commands with
`--json`; see [agents](agents.md).

## Sensitive columns

```sh
dock db sensitive set users.password_hash users.email   # replaces the list
dock db sensitive list
```

Masked callers see `(masked)` and, until masking is enforced inside Postgres,
can browse tables but not run free SQL.

## Audit

Every query, the first page of a table per person (then at most every
10 minutes), every changeset step, setup and policy change go into the audit
log (`dock audit --action data.`), with the SQL kept in the event's detail.
Your query history is read from it.
