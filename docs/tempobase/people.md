# People and the policy

## Roles

Every person has one role:

| Role | Can |
|---|---|
| `viewer` | Look at data, as the policy allows |
| `editor` | Change data, as the policy allows |
| `admin` | Everything: connections, setup, people, tokens, the policy, approving changes, the audit log |

## Invites and resets

There is no email. In Access, an admin invites an email address
with a role and copies the link to the person; opening it sets their name
and password and signs them in. Invites last 7 days, resets 24 hours, and
both work once. The link's code sits after `#`, so it stays out of proxy
logs.

Admins change a person's name and role, disable them or delete them.
Disabling or deleting someone ends their sessions and tokens at once. The
last admin can't be removed.

## Who may do what

The **data access policy** gives each subject a level and sensitive column
visibility, for production and non-production connections:

| Level | Allows |
|---|---|
| `none` | Nothing. The connection is hidden: missing from lists, and every call on it is not found |
| `schema` | Tables, columns, relations; no rows |
| `read` | Browsing and read-only SQL |
| `propose` | Changesets an admin approves |
| `write` | Committing one's own changesets |

Subjects are viewers, editors and tokens. Admins can always do everything.
Built-in defaults:

| Subject | Non-production | Production | Sensitive columns |
|---|---|---|---|
| Viewers | `read` | `read` | masked |
| Editors | `write` | `write` (commits confirm the connection's name) | masked |
| Tokens | `none` | `none` | masked |

Admins change the default in Access, and override it per
connection in the connection's settings; the connection's rule wins. The
connection page shows your effective access and which rule decided it.

Over the API the subjects are `role:read` (viewers), `role:deploy`
(editors) and `tokens`, the same values Dock uses:
`PUT /api/v1/data-policy` and `PUT /api/v1/connections/{name}/data-policy`.

## Sensitive columns

Admins mark columns sensitive in the connection's settings; Schema map
suggests some from their names (email, password, token, phone, ...). Table
pages show them as `(masked)`. Callers whose policy says `masked` never see
them: their SQL runs as `tempobase_reader_masked`, which Postgres doesn't
let select them. Callers with `visible` (admins always) can reveal them per
table, and each reveal is audited.

## Audit

Every query, page of rows, reveal, changeset, setup, policy change,
sign-in (and failed sign-in), invite, token and connection change is an
audit event with who, when and what. Admins read the log in Activity,
with the SQL of each query. Everyone sees their own queries in the SQL
editor's history. Events older than `audit_retention_days` (90) are
deleted.
