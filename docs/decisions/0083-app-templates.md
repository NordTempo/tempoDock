# 0083: App templates

Status: accepted (asked by Saile, 2026-11-14; the UI is mock A of the plan).

## Context

Self-hosting an app on Dock today means reading its compose file and
recreating each service by hand, or `dock import compose` and fixing what
it couldn't map. A catalog of apps that deploy in one step would do it
for them. Dock has the pieces already: database templates (ADR 0019),
project files with references, volumes and `depends_on` (ADR 0049), and
staged plans. What it lacks is a catalog and a way to fill in an app's
few inputs.

## Decision

| Topic | Decision |
|---|---|
| What a template is | A project file fragment (one environment's `services`) plus metadata and three keys: `inputs` (what the form asks; each input **is a variable** on one of its services), `generate` (variables Dock fills once: `password`, `hex:N`, `base64:N`, `uuid`) and `main` (the service the user names and the domain goes on). `domain = true` gives the main service a domain: the one given, or a generated one |
| Composition | No template syntax. Wiring is references Dock resolves anyway: `"https://${{app.DOCK_PUBLIC_DOMAIN}}"`, `"${{db.DATABASE_URL}}?sslmode=disable"`. A value two services share is generated on one and referenced from the other |
| Databases | Stay typed Go templates (ADR 0019): app templates use them with `template = "postgres"`, and the catalog lists each as a one-service template under Databases |
| Names | The main service takes the name given (default the template's), the others `NAME-<part>` (`umami`, `umami-db`); references and `depends_on` are rewritten. A taken name refuses with the `--name` that avoids it |
| Using one | Expand, plan with `planEnv`, stage with `stageEnv`, and make only the template's services live in the same transaction (`applyPending` per service): like `dock service create`, it is live at once and never applies other staged changes in the environment. Domains are added before the first releases, so `DOCK_PUBLIC_DOMAIN` is set in the first one. Each service's first release starts once the services it depends on finished theirs: apps that exit when their database isn't up yet (Miniflux) otherwise fail their first deploy. A generated domain on a server without a base domain refuses first, since its references would break the deploy. A domain or deploy that fails after the services exist is listed, not undone |
| After | The services are ordinary. Each records the template and revision that made it (`services.from_template`, migration 0115), shown on the service; nothing reads it to run them. Export and clone copy services, not their origin |
| Catalog | Embedded (`internal/apptemplates/catalog`, about 60 KB with marks), parsed on first use. No remote catalog: fetching one needs a timer or a fetch on open, and a catalog that can change images under an install is a supply chain door. Templates update with Dock releases |
| Images | `tag@sha256:<index digest>`, an index with linux/amd64 and linux/arm64. Each service names a `track` (tag pattern); `go run ./scripts/templates bump` moves to the newest tag on it, pins it and raises `revision`; `pin` re-pins the current tags. Run by hand before a release, then `e2e/templates.sh` for the templates that changed |
| Marks | Monochrome SVG paths from Simple Icons (CC0 1.0) where the app has one, its initial otherwise. Names and logos are their owners' trademarks, used to identify the apps |
| Borrowed work | Ideas only. Coolify's catalog is Apache-2.0 and `Dokploy/templates` MIT; their app lists and env var sets were a cross-check. Entries are written from each app's own docs and images. Dokploy's main repository has its own licence and nothing comes from it. `catalog/NOTICE` says so |
| Interfaces | API `GET /app-templates`, `GET /app-templates/{t}`, `POST /projects/{p}/environments/{e}/app-templates/{t}` (`plan` for a dry run, with the expanded project file). CLI `dock template list\|show\|use` (`--plan`, `--file`). MCP `list_templates`, `use_template`. UI: New service › Template (replacing the Database source) and New project › From template |
| Permissions | Using needs what creating a service needs (config write; deploy scope), through `envFor`, so a locked environment refuses tokens. Planning needs read. Listing is open to every caller: it is the same for everyone |
| Audit | `template.use` naming the services, the template and its revision, then each first release's `release.deploy` |

## The first catalog

24 apps that run on Dock as it is, plus Postgres and Redis: Umami, n8n,
Uptime Kuma, Grafana, Forgejo, Mailpit, Meilisearch, Vaultwarden, Pocket
ID, Directus, Metabase, Appsmith, Docmost, Wiki.js, Paperless-ngx, Memos,
listmonk, Twenty, Miniflux, Mealie, Actual Budget, linkding, Open WebUI
and Stirling-PDF. Each was deployed on a test server (`e2e/templates.sh`)
before it went in.

Left out on purpose: apps that need the Docker socket, host networking,
DNS or other UDP, host folders or GPUs. MinIO's community edition stopped
publishing images in October 2025. Baserow's all-in-one image starts its
web frontend on `$PORT`, which Dock sets to the service's port, so it
collides with the image's own proxy; Baserow's separate images would
work and are left for later.

## What blocks more apps

| Gap | Blocks | Plan |
|---|---|---|
| MySQL/MariaDB template | Ghost, WordPress, BookStack, Matomo | Next: Go templates with `mysqldump` backups |
| Config files in a container | Plausible's ClickHouse, Garage | Next: `files` on a service, written with Docker's archive API before start |
| Jobs in project files | Nextcloud's background jobs | Next: port of `feat/apply-jobs` |
| A command as arguments, without `sh -c` | Authentik, Keycloak (their entrypoints take the mode as arguments) | Next |
| Postgres with vector extensions | Chatwoot, Immich | Next: a `postgres-vector` variant that backs up and restores in its own image |
| Public TCP or UDP ports | Forgejo over SSH | Out of scope ([0009](0009-dynamic-domains-and-tls.md)); git works over HTTPS |
| Shared volumes, shared network namespaces | nginx plus php-fpm pairs, VPN sidecars | Out of scope: templates pick images that don't need them |

## Consequences

- An app with its database, secrets and domain is one command, one MCP
  call or one dialog, and its result is plain services.
- No new periodic work: the catalog is parsed once, a use is a plan's
  fixed queries and one write transaction.
- Template images age between bumps; updating a running app is the same
  image change as for any service. Telling services that a newer revision
  of their template exists is later.
- Organization templates reuse the format (below).

## Amendment: what more apps need

Decided by the agent while building the second catalog wave.

| Gap | What was built |
|---|---|
| MySQL and MariaDB | Database templates (MySQL 8.4, MariaDB 11.8 LTS): root password generated, an `app` database, `DATABASE_URL`, the Data panel, and backups by `mysqldump` or `mariadb-dump` of every database but the server's own, restored into a new server as its root. Migration 0116 widens the CHECKs on `services.template` and `backups.kind` |
| Redis's licence | A `valkey` template (Valkey 9, BSD): Redis's files, protocol and variables |
| Vector search | A `pgvector` template: Postgres 17 with the extension, backed up and restored in its own image |
| A command as arguments | A start command in exec form, a JSON array like a Dockerfile's `CMD ["worker"]`, starts the container with those arguments and no shell (container spec args, node protocol 29). A Compose `command` without an `entrypoint` becomes one, as Compose passes it to the entrypoint too |
| Scheduled jobs | `jobs` in project files and templates ([0049](0049-apply-and-compose-import.md), [0054](0054-scheduled-jobs.md)): set at once rather than staged, kept unless `--prune`, never a dock.toml's |
| Config files | No new feature. A template writes them in its start command (a heredoc, then `exec` the image's entrypoint), as Plausible's ClickHouse does; the file is visible and editable as the start command. A `files` setting waits for an app whose image has no shell |

Wave 2: Ghost, WordPress, BookStack, Matomo, Plausible, Nextcloud,
Authentik and Keycloak. Not yet: Chatwoot and Immich (their workers share
a volume with the web service), Garage (its image has no shell for the
config file).

## Amendment: organization templates

An organization's templates are stored as their files (`org_templates`,
migration 0117) and listed first in the catalog for its members. Admins
save them from a file in the catalog's format or from an environment's
services: settings, volumes, jobs and references between them; database
templates stay templates; variables that aren't references become secret
inputs, so a template never carries a value someone set; services that
build from source are refused. Dock's own names can't be taken, saving a
name again replaces it, and removing one leaves the services made from
it. Audit `template.save`, `template.remove`. The UI saves from Project
settings and removes from the picker; the CLI is `dock template save`,
`remove` and `show --toml`. Agents use them through the same MCP tools.
