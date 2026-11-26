# 0103: Deleting an environment

Status: accepted (decided by the agent, on Saile's ask, 2026-11-26).
Extends [0078](0078-deleting-archived-projects.md) from projects to their
environments.

## Context

An environment could be created, cloned, locked and moved, but never
removed: only a preview could be closed. A staging environment nobody used
kept its services, volumes, name and domains until the whole project was
archived and deleted. Saile looked for it on the environment's Settings
tab.

## Decision

- **Delete permanently, in one step.** `DELETE
  /projects/{project}/environments/{environment}` with `{"confirm":
  "<name>", "id": <id>}`, `dock environment delete <name> -p <project>
  [--confirm <name>]`, and a Delete row at the bottom of the environment's
  Settings tab. There is no archived environment to go through first: an
  archive would keep everything a delete is asked to free, and the typed
  name, the recent sign-in and the unlimited admin are the same gates a
  project's delete has.
- **Who:** what deleting a project takes. An org admin with an unlimited
  scope, a recent sign-in for sessions; a token is refused in a locked
  environment and files an access request (ADR 0044).
- **What goes:** its live services are archived first (routes and DNS
  records removed, containers stopped), then on its server its
  containers, network, volumes and volume snapshots, its sites' analytics
  events, and every row of it, by the same statements a project's delete
  runs (`projectDeletes`, now written over a set of environments). Closed
  previews cloned from it go with it. Its name and its domains'
  hostnames are free again.
- **What stays:** the audit log, backups in their restic repositories
  (the answer's notes say where), and the project's own rows (its
  placements, project-wide alerts and grants).
- **Refused:** the project's only environment (a project always has
  one; archive the project instead), a preview (close it), an
  environment with open previews (close them first), one that is moving,
  one with a backup, snapshot or restore running, a service deploying,
  and any server it has something on offline, as for a project.
- **A failure halfway** leaves its services archived and its rows in
  place, so deleting again picks up where it stopped.
- **No MCP tool**, as for projects (ADR 0078).

## Consequences

- `projectDeletes` serves both deletes: a new table holding an
  environment's, service's, release's or site's ID gets one statement
  there, and `TestProjectDeletesCoverForeignKeys` still catches a missing
  one with a foreign key.
- The audit event `environment.delete` names the environment; live pages
  drop that environment's cached queries instead of refetching them.
