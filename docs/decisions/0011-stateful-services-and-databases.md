# 0011: Stateful services and databases

Status: accepted

## Context

Apps need databases. Railway lets you add Postgres or Redis next to your app
in a project. We want the same, without the platform becoming a database
company, and without risking data with deploy strategies meant for stateless
apps.

Options:

| Option | Verdict |
|---|---|
| **Database = a service from a template** | Default. Isolated, matches the Railway model. |
| One shared Postgres per server, a database per app | Saves RAM, but one bad query or upgrade hits every app, and all apps share a version. Maybe later as an opt-in for tight servers. |
| External managed database (Neon, Supabase, RDS) | Always supported: it's just a variable. |

## Decision

**Databases are services**, created from templates (Postgres, Redis first). A
template gives a container with a volume, generated credentials, tuned
small-server defaults, and variables like `DATABASE_URL` that other services
reference with `${{postgres.DATABASE_URL}}`.

**Any service with a volume is stateful**, and is treated differently:

- **Recreate, not side by side.** Stop the old container, then start the new
  one. Two processes must never share one volume. Expect a short pause on
  upgrades.
- **Exactly one replica.**
- **Consistent backups.** Templates back up with the database's own tool
  (`pg_dump`, Redis snapshots) on a schedule, shipped off-box with restic.
  Generic volumes get file-level backups with a warning that running databases
  need the template path.
- **Non-destructive restores.** A restore creates a new service from a backup;
  you switch over when it checks out. Restores never overwrite a live database.
- **Private by default.** No public port. `dock connect <service>` tunnels
  through the API to a local port. This needs a stronger permission than
  `deploy`, since it gives direct data access.
- **One per environment.** Staging gets its own database, on the homelab
  ([0010](0010-environments-on-servers.md)), so staging work never touches
  production data.

## Consequences

- No database-specific runtime: templates are regular services plus a backup
  preset and a deploy strategy.
- Database upgrades have a short pause. Zero-downtime database upgrades are out
  of scope.
- Scaling a database means a bigger server. Clustering and replicas are out of
  scope; an external managed database is the answer when that's needed.
- A stateful service is pinned to the server holding its volume.
