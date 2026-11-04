# 0045: Pre-deploy command

Status: accepted (2026-11-03).

## Context

Apps with a database need their migrations applied before new code takes
traffic. Without a hook, people put `migrate && node server.js` in the
start command. That runs the migration in the serving container, again on
every crash restart, and a failed migration surfaces as a failed health
check after the old release may already be gone (stateful services stop
first). Railway, Render and Fly all have a release or pre-deploy command
for this.

## Decision

| Topic | Decision |
|---|---|
| Setting | `pre_deploy_command` and `pre_deploy_timeout_seconds` on the service (staged like the other settings), and `deploy.pre_deploy_command` / `deploy.pre_deploy_timeout` in dock.toml, which win as for the other deploy keys (ADR 0007). Resolved into the release's sealed config, so the release records what it ran |
| When | In `starting`, after the image is built or pulled and before anything is started or stopped. No new release status: a new value would rebuild the releases table's CHECK constraint for a label; the release's events say "Running pre-deploy command" |
| How | `node.Runtime.RunJob` (as backups, ADR 0019) on the release's server: the new image by ID, `sh -c` the command, the release's resolved variables and memory/CPU limits, the environment's private network so the database resolves. No volumes: a stateful service's old container still has them mounted |
| Output | The job's output (its last 256 KB, `node.MaxJobOutput`) goes to the release's build log; on failure the last 10 lines also go to its events |
| Failure | A non-zero exit, a job that can't run, or the timeout fails the release with `deploy_failed`. The old release never stopped, so it keeps serving. Staged changes the release carried are staged again as for any failure |
| Timeout | 10 minutes by default, at most 1 hour (the same cap as health checks). The job's container is removed when it ends or the deploy is cancelled |
| Which releases | Upload, image, config (apply, git push) and promote releases run it. Rollback and restart don't: their image already served traffic, and an older migration tool may refuse a schema a newer one migrated, which would make rollbacks unreliable exactly when they're needed |
| Locked environments | Setting the command is a settings change through `UpdateService` (`serviceFor` with deploy scope), so a token is refused in a locked environment like any other change (ADR 0044). The command a release runs comes from settings staged and applied by someone allowed to |
| Remote servers | `run-job` exists since node protocol 2, so no protocol bump. The `Client` used the 30 s call timeout for it, which also cut long backups short; a job call now gets its ctx's deadline plus the call timeout (6 hours without one) |

## Consequences

- Migrations run once per release, from the code being released, and a
  bad one never takes the running release down.
- Nothing rolls back a migration that succeeded when a later step (start,
  health check) fails. Migrations have to be compatible with the release
  still serving (expand, then contract in a later release), as on every
  zero-downtime platform. The guide says so.
- The output isn't streamed while the job runs: it lands in the build log
  when the job ends. Streaming would need a new node op; add it if long
  migrations make that matter.
- A daemon crash mid-job leaves the exited job container behind (recovery
  doesn't list job containers); on a clean shutdown it is removed.
