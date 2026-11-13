# 0077: Renaming and deleting archived projects

Status: accepted (decided by the agent, on Saile's report, 2026-11-13).
Amends the safety model's "non-destructive by default" and the archive of
[0020](0020-retention-and-alerts.md).

## Context

Archiving a project stops its services and hides it, and keeps
everything: volumes, releases, variables, domains, backups, history. Its
name stays taken by the `UNIQUE (org_id, name)` of `projects`, and an
archived project couldn't be renamed, so the name was gone for good: on
prod, Saile couldn't make a new project with an old project's name. Its
domains' hostnames stayed taken too, and its volumes kept their disk.
There was no way to get either back.

## Decision

- **Rename works on archived projects.** `PATCH /projects/{project}`,
  `dock project rename`, and the UI (below) accept an archived project,
  which frees its name and keeps everything else. Nothing else about
  renaming changes.
- **Delete permanently, for archived projects only.**
  `DELETE /projects/{project}` with `{"confirm": "<name>", "id": <id>}`,
  `dock project delete <name> [--confirm <name>]`.
  - **Who:** an admin with an unlimited scope (`requireUnlimited`); a
    session needs a sign-in or confirmation in the last 15 minutes
    ([0057](0057-two-factor-sign-in.md)). Tokens may delete, like they
    may archive, but a locked environment refuses them and files an
    access request ([0044](0044-environment-lock.md)), as for any other
    change there. A token limited to the project can't delete it.
  - **Confirmation:** the project's name, typed again (`confirm`). The
    CLI asks for it on a terminal and needs `--confirm` otherwise; the UI
    uses the typed confirmation dialog. A project that isn't archived is
    refused with the command that archives it: deleting is always two
    steps.
  - **What goes:** on its servers, its containers (found by label), its
    environments' networks, its volumes and its volume snapshots' files;
    in the analytics store, its sites' raw events, errors, Web Vitals and
    replays; in Dock, every row that belongs to it (`store.DeleteProject`,
    one transaction): environments, services, releases and their events,
    variables, domains (their hostnames are free again; the DNS records
    Dock made were already marked for removal by the archive), git
    connections, backup and snapshot records, jobs and runs, alerts,
    access requests, data panel settings, previews, sites, uptime checks,
    traffic numbers, and the tokens limited to it (without it they could
    do nothing; dropping the limit would widen them to the whole org).
    Log archives on servers go at the hourly retention pass, which
    removes those of services that no longer exist
    ([0046](0046-log-history-and-drains.md)). Its images leave the
    registry and the servers with the next prune, as after an archive.
  - **What stays:** the audit log: its events keep the project's ID, and
    their targets and summaries name it, so Activity still reads "created
    project shop". Backups stay in their restic repositories: they are
    what is left to recover from a mistaken delete, they live in the
    operator's bucket (or the local repository on the server), and
    forgetting snapshots needs a restic job on a server. The answer's
    notes name each repository, the tags (`dock-s<service ID>`) and the
    commands that list and remove them. The analytics store's daily
    totals stay under the sites' IDs, and traces and metric points stay
    until their days age out ([0075](0075-tracing-over-otlp.md)): the
    store has no per-service delete, and nothing in Dock reads them once
    the services are gone.
  - **Every server must be online.** Deleting removes things on the
    servers before any row goes, so a failure halfway leaves a project
    that can be deleted again, and nothing is left on a server unknown to
    Dock. A server that is offline or revoked refuses the delete; the
    answer says to wait for it or have an operator remove the server
    (`dock server remove`, which moves archived projects' environments to
    the control plane's server and leaves what is on that machine).
  - **Previews:** an open preview's pull request comment says the
    preview was deleted with the project, and its `dock/preview` status
    stops linking to an address that is gone. The status stays a success,
    so a deleted project never blocks a merge.
  - **Busy services:** a backup, snapshot or restore of one of its
    services, or a move of one of its environments, refuses the delete
    until it ends.
  - **Audit:** `project.delete`, with what went and the notes.
- **IDs are never reused** (migration 0101). SQLite hands a new row the
  highest ID plus one, so deleting the newest project would give the
  next one its ID, and with it what outlives the rows: analytics events
  by site, traces and metrics by service, trace tokens, Docker names and
  labels, restic tags, log archives. `projects`, `environments`,
  `services` and `sites` are rebuilt with `AUTOINCREMENT`. Releases don't
  need it: everything keyed by a release is also keyed by its service.
- **No restore.** Archived services keep their names in their
  environment and can't be redeployed ([0020](0020-retention-and-alerts.md)):
  their images have left the keep sets. An unarchived project would be
  an empty shell whose service names are still taken. Renaming frees the
  name; `dock project export` keeps the config for a new project.
- **UI:** an archived project's page has Rename and Delete permanently in
  its header, and Organization settings, General lists the archived
  projects with the same two actions, for unlimited admins.
- **No MCP tool.** Agents don't archive projects
  ([0017](0017-mcp-endpoint.md)), so they don't delete them either.

## Consequences

- A project's name and hostnames can be had back: by renaming the
  archived project, or by deleting it and its data.
- "Volumes are never deleted as a side effect" still holds: deleting a
  project is the one operation whose point is deleting them, behind an
  archive, a typed name and a recent sign-in.
- A new table holding a project's, environment's, service's, release's
  or site's ID must be added to `projectDeletes`
  (`internal/store/project_delete.go`); a test fails for one with a
  foreign key that is missing.
- Leftovers on servers that were removed, and the copies old servers
  keep after a move ([0053](0053-moving-environments.md)), are not
  Dock's to delete: Dock no longer reaches them.
