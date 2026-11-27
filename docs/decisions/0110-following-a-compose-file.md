# 0110: An environment follows a Compose file in its repository

Status: accepted (decided by the agent, 2026-11-27).

## Context

[0049](0049-apply-and-compose-import.md) imports a Compose file once,
and its 2026-11-27 amendment updates a project from a newer one
(`dock import compose --prune --stage`, Update from Compose in the UI).
Both need someone to run them after every change to the file. Teams that
keep their stack in a `docker-compose.yml` next to the code want Dock's
services to follow the file the way a service follows its branch
([0018](0018-git-push-deploy.md)): change the file, push, and the
environment's structure follows. 0049 named this as a follow-up.

Forces: a file must not be a side door around review (0049's staging);
removing a service is the change people regret; secrets set in Dock
must not be overwritten by a file; and an idle Dock does nothing.

## Decision

| Topic | Decision |
|---|---|
| What follows | An environment, one Compose file each (`compose_sources`, one row per environment): repository, branch, path. A Compose file converts into one environment, and each environment already has its own staged changes to review. Production can follow `main` while staging follows `develop` |
| Reading the repository | The organization's GitHub App only ([0031](0031-github-app.md), [0066](0066-github-apps-for-many-orgs.md), [0105](0105-github-apps-per-org.md)), by repository ID and installation, as App connections are. No per-source webhook and token: a team without an App runs `dock import compose --stage` from CI ([0094](0094-deploying-from-github-actions.md)) instead |
| Trigger | The App's push delivery, routed to every source of the repository next to its service connections. A push to the source's branch that changes the file, the `.env` next to it or an `env_file` the last sync read queues a sync (latest wins). `dock compose sync` and Sync now sync the branch's head; following syncs at once |
| What a sync does | Fetches the file at the pushed commit (and its `.env` and `env_file` files, at most 16, each at most 1 MB, never outside the repository), converts it with 0049's conversion, plans it against the environment and stages the result, exactly as `dock import compose --stage`. Nothing goes live |
| Who a sync acts as | The source: the system, named after the pusher (`github:<login>`) or the person who asked, limited to the environment, with deploy; with admin when the source stages archives. Following needs deploy; turning removals on needs admin, the admin's say-so the syncs then carry |
| Removals | Only with the source's `prune`, which only an admin sets. Then a service the file drops is staged for archiving (volumes and backups kept) and still needs an admin to apply. Domains, jobs and variables are never removed by a Compose file (0049's amendment) |
| Secrets | A secret's value comes from the file only for a variable that doesn't exist yet. One set in Dock since (a rotated password) keeps its value: a sync never restages it |
| Auto-apply | Opt-in per source (`auto_apply`). A sync is applied on its own only when it removes nothing, needs no person (no secret without a value, no manual step), nothing else was staged in the environment before it (applying would take someone else's changes along), and the environment isn't frozen ([0044](0044-environment-lock.md) with `--people`). Any other sync waits for review, and its outcome says why |
| Outcome | The last sync only: commit (SHA, message, author), when, and `staged`, `unchanged`, `applied` or `failed` with a one-line summary ("+1 service, 2 other changes; 1 warning") or the reason it failed. Audited as `compose.sync`. The plan itself is in the staged changes, and `dock compose sync` prints it |
| Builds | A service with `build:` becomes a source service, as on import; it is deployed as today (its own git connection, `dock deploy`). The file shapes structure and settings only. Connecting such a service to the same repository on creation is a follow-up |
| CLI, API, UI | `dock compose follow OWNER/REPO [--branch] [--path] [--prune] [--auto-apply]`, `status`, `sync`, `unfollow`. `GET/PUT/DELETE /projects/{p}/environments/{e}/compose`, `POST .../compose/sync`. The environment's Settings: a Compose row with what it follows, the last sync and a link to the staged changes |
| Previews | A preview environment can't follow a file: it follows its pull request (0056) |
| Migration | `compose_sources`, deleted with its environment |

## Consequences

- A team's Compose file is the source of their environment's shape, and
  every change to it is reviewed in Dock before it runs, unless they
  chose auto-apply for additions and changes.
- A file that drops a service never removes it unless an admin turned
  removals on, and an admin applies the archive.
- Performance: nothing runs while nobody pushes. A push costs one indexed
  query per App delivery (`compose_sources_repo`); a sync is a handful of
  GitHub calls and one plan and staging, as an import, one at a time per
  source. Start-up resumes queued syncs (`compose_sources_pending`).
- Moving an App connection to a new installation (`adoptAppConnections`)
  doesn't move sources yet: one whose installation is gone fails its
  sync with the reason, and following again fixes it.
