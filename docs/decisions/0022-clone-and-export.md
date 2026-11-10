# 0022: Clone an environment, export a project's config

Status: accepted (decided by the agent, M4); superseded in part by [0049](0049-apply-and-compose-import.md) (import, as `dock apply FILE`)

## Context

M4 ends with "Clone an environment. Export a project's config."
[0007](0007-config-db-plus-repo-file.md) promised the export ("dumps a
project as a file for backup and reproducibility"). Clone is how a
`staging-2`, a preview or a first staging from production comes about
without retyping a project.

Forces:

| Force | Means here |
|---|---|
| Secrets stay secret | Every variable value is sealed and readable only by admins ([variables](../guide/variables.md#who-can-see-what)). A clone must not let a `deploy` token move values it can't read, and an export never contains one |
| Placement ([0015](0015-server-pools-and-placement.md)) | A clone is a new environment: its kind picks the server, and a project stays together per kind |
| Stateful services ([0011](0011-stateful-services-and-databases.md)) | Staging never touches production data. A clone's database is a new, empty one |
| Reuse | Promote ([0014](0014-node-protocol.md#4-registry-and-promotion)) already deploys one environment's images into another by digest |
| Performance | No new periodic work; the export is a fixed number of queries |

## Decision summary

| Topic | Decision |
|---|---|
| Commands | `dock environment clone <from> <to> [--kind] [--deploy] [--copy-values] [-d]` (also `dock env clone`); `dock project export [<name>] [--format toml\|json]` |
| API | `POST /projects/{p}/environments/{from}/clone` (201, `CloneResult`); `GET /projects/{p}/export` (`ProjectExport`) |
| MCP | `clone_environment` (mutating, waits for releases with deploy) and `export_project` (read-only) |
| UI | A "clone" action in each environment's header on the project overview: name, kind, "deploy the same images", and for admins cloning into the other kind "copy values from <kind>" |
| Permissions | Clone: read on `from`, deploy on the project (creating an environment; environment-limited tokens can't). Export: read; environment-limited readers get their environment only |
| Placement | Like `dock environment create`: kind from `--kind`, else from the name (`production` is production, everything else non-production); the project's server for that kind, or the eligible server with most room for a new kind. No eligible server: the error of 0015, nothing created |
| What a clone copies | Live state only: every service that isn't archived (all settings, template, data panel settings), its applied volumes as **new, empty** volumes, and live variables |
| Variable values | A value made only of references (`${{postgres.DATABASE_URL}}`) is always copied. Literal values are copied when the caller may read them (admin, the `--reveal` rule) **and** the clone has the source's kind. Across kinds (production to non-production or the reverse) they are copied for nobody, admins included, unless an admin passes `--copy-values`, and then the result lists them. Not copied: a template service's own variables get the template's values anew (a new password, the defaults); everything else is not created and is listed (`skipped`) |
| Never copied | Domains, git connections, releases, backups, backup schedule overrides, data access setup and policy rules, staged changes. The result counts what it left behind |
| State of the clone | Settings and variables live at once (nothing runs yet, like `service create`); volumes staged until each service's first deploy |
| Deploy | Off by default. `--deploy` runs promote from `from` into the clone; each release takes its service's staged volume live with it |
| Export content | Live config: environments (name, kind), services (non-default settings, template, database settings), shared and service variable **names**, applied volumes, domains, git connection (repository, branch, directory, statuses). No values, tokens, webhook secrets, servers, releases or staged changes |
| Export format | TOML by default (`version = 1`), JSON with `--format json`/`--json`. Sorted by name at every level, no timestamps |
| Import | Not in this milestone (below) |
| Migration | None: two query files, no schema change |

## Clone

### Values and who may copy them

Literal values are copied in one case by default: an admin cloning into
an environment of the **same kind**. That clone is exact; the sealed
values are copied as they are (the secrets box binds no row, so the
ciphertext is reused).

Everyone else gets the rules below, and so does an admin whose clone
**crosses kinds** (production to non-production, or the reverse) without
`--copy-values`:

- A `deploy` caller, typically an agent's token, can replace a value but
  never read one. Copying values it can't read into an environment it then
  controls would make "write-only" a matter of one extra step.
- Across kinds, copying is the dangerous default. Cloning production into
  staging would copy production's literal values, including an external
  `DATABASE_URL`, so staging would point at production's database (against
  [0011](0011-stateful-services-and-databases.md)'s "staging never touches
  production data"), and production's secrets would quietly move to the
  homelab ([0010](0010-environments-on-servers.md)). The reverse puts
  staging's credentials into production. Neither should happen because
  someone forgot a flag.

The rules:

1. A value that is nothing but references is copied. It wires services
   together and holds no secret of its own. Strict: any literal text
   around a reference (`http://${{api.HOST}}`, `postgres://u:pw@...`)
   makes it a value like any other.
2. A template service's own variables (`POSTGRES_PASSWORD`, `REDIS_URL`,
   ...) are made from the template again, with a new password. The clone's
   database is new and empty anyway; its own credentials are the right
   ones, and references to it (`${{postgres.DATABASE_URL}}`) resolve to
   them.
3. Everything else is not created, and the result lists it
   (`web.STRIPE_KEY`, `shared.SENTRY_DSN`) for someone who may to set.
   An empty variable would be worse: an app would start with it blank.

An admin who wants the values across kinds anyway passes `--copy-values`
(API `copy_values`, the UI's "copy values from production" checkbox,
shown to admins only when the kinds differ). It is refused for anyone
else, ignored for same-kind clones, and the result names every literal
value that came from the other kind (`copied_across_kinds`, "Values
copied from the production environment production: ..."), as does the
audit entry.

When literal values are copied (same kind, or `--copy-values`), the
template password is copied too: "clone" means a copy, and an app that
spells the password out literally keeps working.

### Live state, new volumes, nothing deployed

The clone copies what is live in `from`: staged changes are someone's
unfinished edit and stay where they are (the result counts them). Settings
and variables are written live, since nothing runs yet, so a first deploy
or a promote sees all of them, shared variables included. Volumes are
staged, as `service create` and templates stage theirs: an applied volume
means "exists and is mounted", the backup scheduler treats it as stateful,
and backing up a service that never ran fails. Each volume goes live with
its service's first release.

`--deploy` is cheap with promote: services exist in the clone with the
same names, so promote from `from` to the clone does the rest, by digest,
one service at a time, stopping at the first failure. One addition: a
promote release normally doesn't apply the target's staged changes; the
clone's promotion asks it to (`applyStaged`), so each release takes its
service's volume live and, if it fails, stages it again like any failed
release's carried changes. Without a registry, a service built on another
server is skipped with promote's own `no_digest` reason. If the clone's
server is offline the clone stands and the result says nothing was
deployed.

## Export

The export is for reading, review and backup: commit it, and `git diff`
shows what changed in a project's configuration. Determinism is the
design: every list is sorted by name (domains by hostname), zero settings
are left out, and nothing that changes without a config change is in it
(no timestamps, release numbers, statuses or servers). The variable query
selects names only; the value column is never read on this path, so a bug
in formatting can't leak one.

## Why no import yet

An import has to answer questions an export doesn't: merge or replace,
what to do with services missing from the file, how secrets get in, and
how domains and git connections (which need DNS and webhook secrets) are
created. Staged changes already give a reviewable path for edits made in
the CLI or UI, and clone covers "another environment like this one". The
format is versioned (`version = 1`) so `dock project apply` can come later
without breaking files committed today, when there is a second install or
a GitOps workflow that needs it.

## Consequences

- Agents can make their own preview environments with a `deploy` token and
  get a working app plus database, with a list of the secrets a person
  still has to set.
- Making staging from production is safe by default: it gets references,
  new database credentials and a list of values to set, never
  production's secrets or database URL. An admin who wants an exact copy
  across kinds says so with `--copy-values`, and the output names what
  came over.
- A project's config can live in git as a readable, diffable file.
- `promote` gained an internal option to apply staged changes; its public
  behaviour is unchanged.
- Performance: no new periodic work. The export is seven indexed queries
  however big the project; a clone is one write transaction proportional
  to the environment's size.
