# 0061: One restore model: restore points, a restored service, and switching to it

Status: accepted (Saile decided the model; the details below are the
agent's)

## Context

A service has two kinds of copies of its data, and they grew apart:

- **Backups** ([0019](0019-templates-and-backups.md)): daily restic runs,
  off-box. A template database restores one into a new service
  (`<db>-restored` by default). Nothing else restores from the UI.
- **Snapshots** ([0047](0047-volume-snapshots.md)): quick copies of the
  volumes on the service's own server. A template database restored one
  into a new service named `<db>-snapshot-<id>`, or in place with
  `in_place`; any other service in place only.

They sat on one tab with two lists, two Restore buttons and two dialogs
that asked different questions. And both stopped at the hard part: once a
restored database checks out, the services using it still point at the
old one. The guide told people to rewrite `DATABASE_URL` by hand with
`dock env set`, one service and one key at a time, which is exactly where
a typo takes an app down.

## Decision

| Topic | Decision |
|---|---|
| Restore points | Backups and snapshots are both *restore points* of a service: listed together, newest first, each saying what it is (backup or snapshot, where it lives, its size and age), and restored with one action and one dialog |
| A template database | Always restores into a new service, `<db>-restored` by default (the dialog can rename it), from a backup or a snapshot alike. The live service is not touched. A snapshot's restore keeps the snapshot's credentials; a backup's gets new ones, as before |
| Using the restored database | Once it checks out, **Use db-restored** switches the environment's references to it: `POST {service}/switch-references` (`dock service switch-references db --to db-restored`) rewrites every `${{db.KEY}}` in the environment's variables, service and shared, to `${{db-restored.KEY}}` and applies it, so the services using db get config releases. `dry_run` lists them first; the dialog shows that list |
| Switching back | The same action the other way (`switch-references db-restored --to db`). The old service keeps running until someone archives it, so going back is one click while it exists |
| Refusals | The target must be a live service of the same environment with every referenced key (the error names the missing ones). The switch stages and applies like `dock env set --apply`, so it is refused while the environment has other staged changes, which would otherwise go live with it unasked. Scope deploy, audited as `service.switch_references` |
| Any other service | Restores a snapshot in place: it stops, a safety snapshot keeps what it holds now (restoring that one undoes the restore), the snapshot is copied in, and it starts again. Nothing references such a service by name, so there is nothing to switch |
| The API | `in_place` stays on the snapshot restore for databases, for the CLI, MCP and scripts that use it. The UI and the docs lead with the one model and don't offer it for a template database |

### Why a new service, not in place, for databases

An in-place restore of a database throws away whatever was written since
the restore point, and the safety snapshot is the only way back. A new
service costs a little disk for a while and makes every step reversible:
restore, look at the data (the Data panel works on it like on any
database), switch, switch back. The switch is a config release of the
services that use it, the same path as any variable change, health-checked
and rolled back the same way.

### Why rewrite references rather than rename services

Renaming `db-restored` to `db` would keep the variables as they are, but
the service's name is its hostname on the environment's network, its
volumes and backups hang off it, and the old `db` would have to be
renamed out of the way first: three changes that each break something if
the next one fails. Rewriting `${{db.KEY}}` is one transaction of
variable changes plus releases that already know how to fail cleanly.
Literal URLs naming the host (`postgres://db:5432/...`) are not rewritten:
they don't say which keys they mean, and the Data panel's reference check
already flags them.

## Consequences

- The Backups tab becomes a list of restore points with one Restore
  action; "use the restored database" is a button on the restored
  service, not a guide section.
- A snapshot restored into a new service is named like a backup's,
  `<db>-restored`. A second restore while that service exists needs `--as`
  (or the dialog's name), as for backups.
- A shared variable that references the database reaches every service,
  so switching it gives every running service of the environment a config
  release, the database's own included. The dry run lists them.
- Services restored in place keep the safety snapshot as their undo; there
  is still no restore of a backup in place.
