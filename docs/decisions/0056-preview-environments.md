# 0056: Preview environments per pull request

Status: accepted (asked by Saile, 2026-11-03; decided by the agent); superseded
in part by [0077](0077-previews-belong-to-services.md) (services opt in, and each picks its pull requests);
amended by [0070](0070-sleeping-environments.md) (previews sleep after 30 minutes idle by default) and
[0092](0092-preview-database-copies.md) (databases start with a copy, masked or not, of the base's)

Amended by [0077](0077-previews-belong-to-services.md): previews are opted
into per service, and the base is that service's environment.

## Context

The roadmap had previews under "Later". The parts were there: cloning an
environment ([0022](0022-clone-and-export.md)), push-to-deploy
([0018](0018-git-push-deploy.md)) and the GitHub App
([0031](0031-github-app.md)), generated subdomains, and placement that
keeps non-production environments on servers that accept them
([0015](0015-server-pools-and-placement.md)). What was missing: reacting
to `pull_request` events, a lifecycle that ends, and rules strict enough
that a pull request can't walk off with production's secrets.

Forces:

| Force | Means here |
|---|---|
| A pull request runs code nobody reviewed yet | Its environment must not hold secrets nobody chose to give it, and forks (anyone on GitHub) get nothing by default |
| Webhooks are redelivered, replayed and reordered | Every event must be safe to apply twice and safe to apply late |
| Previews pile up | Their containers, volumes, networks and hostnames must go when the pull request closes, and a project can't open more than a few |
| Idle means idle ([performance](../performance.md)) | No polling of GitHub: events in, posts out, nothing on a timer |
| Feedback without spam | One place on the pull request that says where the preview is, kept current |

## Decision

| Topic | Decision |
|---|---|
| Opt in | Per project, naming a **base** environment (`dock env previews enable staging`, `PUT /projects/{p}/previews`, the project's Previews settings). One base per project. Admin of the whole project: previews run a pull request's code with what the base gives them. A preview environment can't be a base |
| Which pull requests | A `pull_request` event (`opened`, `reopened`, `synchronize`, `edited`, `closed`) for a repository a base service is connected to (App connections by repository ID, webhook connections by name), **into the branch that service deploys**. A pull request into another branch is answered "ignored" |
| Forks | Never by default. `allow_forks` turns them on; a fork's preview builds `pull/<n>/head` |
| Name | `pr-<number>`; `pr-<number>-2`... if the name is taken (another repository's pull request) |
| Kind and placement | Always `non_production`, placed by 0015: where the project's non-production environments are. A server that only accepts production never gets one |
| What is cloned | `cloneInto`, as `dock env clone` does, from the base's live config: services, settings, new empty volumes, variables |
| Variables | Values made only of references to the preview's own services and shared variables are copied (they hold no secret, and resolve inside the preview). A reference to an org variable (`${{org.KEY}}`) counts as a literal: it reads a secret the org holds. Template services' own credentials are made anew (new password). **Every other value is left out** unless it is listed in `copy_variables` (`web.PUBLIC_API_URL`, `shared.SENTRY_DSN`). Nothing is listed by default. The admin's CloneEnvironment rules (copy everything same-kind) do not apply: the actor is a pull request |
| Locks ([0044](0044-environment-lock.md)) | A preview starts **unlocked**, whatever the base's lock: it is throwaway, and nothing in it reaches the base. Making one changes nothing in the base (it reads the base's config and promotes its images), so a locked base doesn't stop previews. A preview never gets the base's data (new, empty volumes), its unlisted secrets or its domains (generated ones of its own). An admin may lock a preview; its pull request's commits then wait for approval like any push into a locked environment, because they go through the same git worker |
| What deploys | Services connected to the pull request's repository get a git connection of their own (the base's repository, directory and credentials) with the head commit queued: the push-to-deploy worker builds it. Every other service runs the base's image through promote. Services with a domain in the base get one generated domain each (`web-shop-pr-12.<base domain>`) |
| New commits | `synchronize` (or any newer event with a new head) queues the head commit on the preview's git services. Pushes never reach a preview's connections: App push routing leaves preview environments out, and a preview connection's own webhook URL is never registered |
| Idempotent, replay-safe | Delivery IDs are deduplicated for 7 days, as for pushes. Beyond that, each preview row keeps the pull request's `updated_at` of the newest event applied; an older event changes nothing. Applying an event whose state is in place (same head, already closed) is a no-op. All changes to previews are serialized by one lock, so two deliveries can't both create one |
| Limit | `max_previews` open per project, default 3, at most 20. Beyond it the pull request gets an `error` status saying so, and no environment |
| Disk | No new preview goes to a server whose last sample showed Docker's disk past `[retention] prune_disk_percent` |
| Close | `closed` (merged or not), or `dock env previews close pr-12`: the preview row closes, queued commits are dropped, and the environment is archived: every service archived (routes, containers, with their anonymous volumes), its domains **deleted** so the hostnames are free, its variables and git connections **deleted** (the values and the copy of the base's token it was given go with it), the environment renamed `pr-12~<id>` and hidden from lists. Once the containers are gone, its network and **volumes are removed** (`RemoveVolume`, node protocol 20). A deploy in flight defers the archive to its end; a restart finishes archives left over |
| Volumes | Previews are the one place Dock removes an app's volume ([node rules](../../internal/node/AGENTS.md)). Their data is a throwaway copy by design: a preview gets new, empty named volumes (staged by the clone, applied by each service's first release, so a template's data directory is never an anonymous volume) and is never backed up on a schedule. Seeding: `seed_from_snapshot` names base services (`db`) whose volumes each new preview fills from that service's newest ready snapshot on the preview's server ([0047](0047-volume-snapshots.md)), copied into the preview's own new volume before its first deploy, with the base claimed so the snapshot isn't pruned under the copy and the clone locked against deploys while it fills. A seeded template keeps the base's generated credentials, which the copied data files need. Without a snapshot it starts empty, said in the preview's last event and the `preview.seed` audit event (2026-11-07) |
| Project files ([0049](0049-apply-and-compose-import.md)) | `dock project export` leaves preview environments out: a file that named `pr-12` would make it an ordinary environment. `dock apply` only changes the environments a file names, so it never touches previews |
| Retention | An archived preview's services leave the image keep-sets like any archived service's (0020), and the archive starts a registry prune |
| What GitHub sees | The comment and statuses are public to whoever reads the repository: they name the environment, its addresses and the commit, never servers, errors or values ("a service failed to deploy; its release in Tempo Dock says why") |
| On the pull request | A `dock/preview` commit status on the head commit (pending, success with the first address as its link, failure) and **one comment**, edited in place as the preview changes, listing the addresses. Dock finds its comment by ID, or by a hidden marker if it lost the ID, so a crash doesn't make a second one. Posting runs in the background, one refresh per preview at a time, coalescing bursts. Needs Pull requests write on the App (or token); without it the status still posts and the preview's last event says why there is no comment |
| GitHub App | New Apps ask for `pull_requests: write` and the `pull_request` event. An App made before this needs both added on GitHub (App settings > Permissions & events), and installations must accept them |
| Webhook connections | Their webhook can send pull request events too, but only a connection of a service **in the base environment** (or the open preview's base, for its close) speaks for pull requests. Anyone who may connect a service anywhere gets its webhook secret; a connection elsewhere could otherwise sign a pull request and have it built with the base's credentials and copied values. Others are answered "ignored". The App's webhook secret is the App's alone, so its deliveries are routed by repository |
| Everything else | API: `GET/PUT/DELETE /projects/{p}/previews`, `GET /projects/{p}/environments/{e}/preview`, `POST .../preview/close`. `Environment.preview` names the pull request. CLI: `dock env list` marks previews; `dock env previews`. MCP: `get_previews`, and `get_preview` for one. UI: previews marked in the environment switcher and on the project page with a link to the pull request, and the settings under the project's settings |
| Migration | 0055: `preview_settings`, `previews`, `environments.archived_at` |

## Consequences

- A pull request's preview looks like the base except for secrets. Most
  apps need a few non-secret literals (a public URL, a feature flag)
  listed in `copy_variables`; that list is the one place an admin
  decides what a pull request may read.
- Environments can now be archived, only through previews. Lists,
  placement counts and server removal skip archived environments.
- Closed previews keep their history (releases, audit events) under the
  archived name; their rows are small and stay.
- Reopening a pull request makes a new preview with new volumes.
- With no previews and none configured, previews cost nothing at idle:
  no goroutine, ticker or query runs until a pull request event or a
  release of a preview environment arrives.
