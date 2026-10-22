# 0018: GitHub push-to-deploy

Status: accepted (decided by the agent)

## Context

[0006](0006-every-way-to-build.md) promised a git push path (M4): push to a
branch, get a source release, through the same pipeline as `dock deploy`
and the same `dock.toml` handling ([0007](0007-config-db-plus-repo-file.md)).
The daemon's API listens on Tailscale or loopback and must stay there
([safety model](../safety-model.md)); GitHub has to reach something public.

## Decision summary

| Topic | Decision |
|---|---|
| Unit | One connection per service: repository, branch, optional subdirectory (`git_connections`, migration 0016) |
| Trigger | A GitHub webhook per connection, `push` events, HMAC SHA-256 with a per-connection secret |
| Source | `GET /repos/{o}/{r}/tarball/{sha}`, streamed, re-rooted at the subdirectory, into the upload path of `dock deploy` |
| Auth to GitHub | Optional fine-grained token per connection (Contents read; Commit statuses write for statuses), sealed with the secrets box |
| Public surface | Only `/hooks/github/` on `[hooks] public_host`, routed by the control plane's Caddy to a loopback listener that serves nothing else |
| Queue | Newest push wins: one queued push per connection, in the DB; a running deploy is never cancelled |
| Feedback | Commit statuses (optional), the latest delivery's outcome on the connection, the commit on the release |
| No GitHub App | See below |

## Receiving pushes

- **URL**: `https://<public_host>/hooks/github/<hook_id>`, `hook_id` 128
  random bits. Caddy matches host *and* path prefix and proxies to
  `[hooks] listen` (default `127.0.0.1:7072`), a dedicated loopback
  listener with one route. A dedicated listener rather than the internal
  one (which has Caddy's `ask`, the registry token and pprof) means no path
  trick through Caddy can reach anything else. The hostname gets a
  certificate through the normal on-demand `ask`, and can't be added as a
  domain.
- **Checks, in order**: hook exists (404), rate limit per hook (token
  bucket, burst 30, one per 2 s; 429), signature `X-Hub-Signature-256`
  with `hmac.Equal` (401), event (`ping` answers `pong`, other events are
  ignored), delivery ID dedupe, then the push rules below. Bodies are
  capped at 4 MB and at most 2 are read at once.
- **Replay protection**: `X-GitHub-Delivery` IDs are kept 7 days
  (`git_deliveries`, pruned on insert). A seen ID is answered with no
  effect. GitHub's own "Redeliver" reuses the ID, so it is refused too;
  `dock git deploy` is the retry. GitHub delivers over TLS, so a captured
  delivery older than the window needs a TLS break; and even then it only
  queues a commit of the connected branch.
- **Push rules**: other repository, other branch, tags and deleted
  branches are ignored. With a subdirectory, a push whose listed commits
  change nothing in it is ignored (no commits listed, or GitHub's maximum
  of 2048, counts as "changed"). A commit already active or deploying is
  ignored.
- The response body (one line) is what GitHub's delivery log shows, and
  the same line is stored as the connection's `last_event`.

## Deploying

- An accepted push is written to the connection's `pending_*` columns,
  replacing any older one, and the service's worker is kicked. Queued
  pushes survive restarts (`resumeGit` at start).
- The worker is a goroutine that exists only while there is something to
  do: if a release is in flight it exits (the release's end kicks it
  again); otherwise it fetches the tarball and calls the same
  `deploySource` as `POST .../releases/source` (fail-fast checks, the
  per-service upload slot, `saveUpload` with `max_upload_mb`, dock.toml
  read on the way to disk, `startRelease`). A newer push cancels a fetch in
  progress, never a release. If only the upload slot is busy (a `dock
  deploy` streaming), it looks again after 10 s.
- The fetch streams: gzip → tar reader → re-rooted tar writer → pipe →
  the upload file. Entries outside the subdirectory are dropped; a missing
  subdirectory fails the push with a clear reason.
- Releases from a push are `source: upload` with a `commit` (SHA, subject,
  author), created by `github:<pusher>`. Releases that copy an image
  (config, rollback, restart, promote) copy its commit, so "already live"
  and "what code runs" stay right. No new release source: the `source`
  CHECK constraint would need a table rebuild for no gain.
- `dock git deploy [--ref]` resolves the ref through the API and queues it
  with `pending_force` (deploys even if live), acting as the caller.

## Statuses and tokens

- With `--status` (requires a token), `pending` when the release is
  created, `success`/`failure` when it ends, `error` when the fetch fails;
  context `dock/<project>/<env>/<service>`, link to the release when
  `[hooks] ui_url` is set (off by default: the URL is visible to anyone
  who can see the repository). Posted in the background; GitHub never
  blocks or fails a deploy.
- Tokens are write-only: the API has `token_set`, the CLI reads them from
  stdin only. They travel only in the `Authorization` header; client
  errors are stripped of URL queries (GitHub's tarball redirect carries a
  short-lived token there). Commit messages and authors lose control
  characters before they reach a terminal.

## Permissions and audit

Read, connect, change, disconnect and manual deploys go through core with
the caller's actor: `read` to see, `deploy` on the environment for the
rest. The webhook secret is the authority for pushes; their releases act
with `deploy` on that one service. Audit: `git.connect`, `git.update`,
`git.disconnect`, `git.deploy`, and `release.deploy` for each push.

## Why not a GitHub App

An App gives installation tokens, one webhook for many repositories and
checks with rich output. It also needs a registered App per Dock install
(or a central one Dock doesn't have), a private key to store and rotate,
JWT signing, and installation-token exchange, all to replace one secret
and one token per service. Revisit when users connect many repositories or
want check runs; the connection model (repo, branch, dir) stays.

## Performance

Idle: nothing. No goroutine, timer or poll while no push is queued. Per
delivery: one HMAC, about six indexed queries. Per deploy end: one indexed
lookup. The retry timer (10 s) runs only while a queued push waits for a
busy upload slot. Memory: deliveries ≤ 2 × 4 MB at once; tarballs stream.

## Consequences

- Push-to-deploy works with no CI and no GitHub App, for public and private
  repositories.
- One public hostname and one path become reachable from the internet;
  everything else stays private.
- A webhook per service: monorepos with several services add one webhook
  per service (each with its `--dir`).
