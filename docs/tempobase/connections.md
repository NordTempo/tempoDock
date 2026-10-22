# Connections

A connection is a Postgres database tempoBase can open: a URL, a name and a
production flag. Only admins add, change and remove them.

## Add one

In the UI: Connections, Add. Paste the URL, pick a name (lowercase letters,
digits and `-`, used in URLs) and say whether it is production. Test first:
it logs in as the URL's user without changing anything and shows the server
version and whether that user can set up access.

```
postgres://owner:password@db.example.com:5432/shop?sslmode=verify-full
```

- `postgres://` or `postgresql://`; no Unix sockets.
- The only query parameter is `sslmode`: `disable`, `prefer` (the default:
  TLS if the server offers it), `require`, `verify-ca`, `verify-full`. For
  `verify-*` with a private CA, paste the CA certificate in the form;
  otherwise the system's roots are used.
- The whole URL is stored encrypted with the [key file](backups.md). Host,
  port, database, user and TLS mode are kept readable for the list. The
  password never comes back in a response.
- **Production** decides which policy column applies (see
  [People and the policy](people.md)) and that committing a change asks for
  the connection's name.

Over the API: `POST /api/v1/connections/test` with `{url}`, then
`POST /api/v1/connections` with `{name, url, production}`.

## Set up access

tempoBase never browses as the URL's user. An admin opens the connection
and clicks **Set up data access** once. That creates three least-privilege
roles in the database, with generated passwords stored encrypted in
tempoBase:

| Role | Does |
|---|---|
| `tempobase_reader` | Reads everything (`pg_read_all_data`, read-only transactions): browsing and SQL |
| `tempobase_reader_masked` | The same, without the columns marked sensitive |
| `tempobase_writer` | Changes rows, not the schema; only commits changesets |

The URL's user needs `CREATEROLE` (managed Postgres owners have it) and
Postgres 14 or newer. Running setup again rotates the passwords. The names
differ from Dock's (`dock_reader`, ...), so Dock and tempoBase can both
manage one database. Row-level security still applies.

## Browse, query and change

Open a connection to get the Data panel, the same one as in Dock:

- **Tables**: keyset pages of 100 rows, filters, ordering, related rows.
- **SQL**: one statement in a read-only transaction, 1 000 rows by default
  (at most 100 000), a 10 s timeout (at most 60 s). Your past queries are
  in History; save the ones you reuse.
- **Changes**: edits become a changeset, row inserts, updates and deletes
  committed in one transaction. Each update and delete matches the primary
  key and the values you saw, so if someone changed a row meanwhile nothing
  is written and the conflict is shown. At most 500 rows; tables need a
  primary key. Writes in SQL are refused: every change is a changeset.

Whether you can commit, only propose, or only look is the policy's call.
Proposals wait for an admin, who sees the exact SQL, and expire after 7
days.

## Change or remove

Changing the URL or the production flag applies at once and closes open
database connections. If it now points at another database, the panel
asks for setup again.

Removing a connection deletes what tempoBase keeps for it: the setup
record, sensitive column marks, saved queries, changesets, its policy rules
and token limits. Audit events stay. The roles stay in the database; the
confirm dialog shows the `DROP ROLE` statements if you want them gone.

## Database connections

tempoBase opens at most 2 connections per database and role and 8 in
total, and closes each one 60 s after its last use. An idle tempoBase holds
no connection to any database.
