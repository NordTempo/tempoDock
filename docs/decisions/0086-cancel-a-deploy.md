# 0086: Cancel a deploy

Status: accepted (asked by Saile through the coordinator, 2026-11-16;
the core sketch was the old branch `feat/deploy-cancel`). Amends
[0018](0018-git-push-deploy.md): a running deploy can now be canceled.

## Context

A deploy that was a mistake (the wrong image, the wrong branch, a build
that will take 20 minutes for a typo) had to run to its end: build,
start, health check, cutover. Then it needed a rollback, which is a
second deploy. ADR 0018 and the guide said "a running deploy is never
cancelled". There was no way to stop one in the API, CLI, MCP or UI.

## Decision

**What can be canceled.** A release that is deploying, at any step
before its cutover: queued (waiting for a move, an earlier promotion
step or the builder), building, pushing, pulling, running its pre-deploy
command, starting, health checking. Once the cutover begins (routes are
being switched to it) it is too late: the cancel is refused with a
conflict that says to roll back once it is live. `dock release cancel
[N]` (default: the release deploying now), `POST
.../releases/{number}/cancel`, MCP `cancel_deploy`, and a Cancel action
in the UI.

**How.** Every deploy runs with its own context, derived from the
daemon's and registered by release before its goroutine starts (`launch`,
`deployRuns`). Canceling cancels that context: a build stops in BuildKit
(on a remote builder too: the node link cancels the call there), a pull
or push stops, the pre-deploy job's container is removed, a health check
stops waiting. The deploy then takes the failure path it already had:
whatever it started is removed (by label, so a container started just
now is found too), its host port is freed, and a stateful service's
previous release, stopped first, is started again.

**The cutover race.** A deploy's run holds a small state: running,
canceled, switching, finished. The cutover asks `switching()` before it
touches routes: if a cancel came first, traffic never moves; otherwise no
cancel is taken from then on. Either the cancel wins and nothing switches,
or the cutover wins and the cancel is refused. A cancel that arrives
after the deploy failed on its own is refused as finished, so a real
failure is never relabelled.

**The half-built release.** It reads as `canceled`, a status of its own in
the API, CLI, MCP and UI. In SQLite it is `failed` with error code
`deploy_canceled` and error "canceled by NAME": the releases table's
CHECK constraint dates from migration 0001, and changing it means
rebuilding the busiest table for a word. `releaseStatus` maps it in one
place. A partial push leaves no tag behind; a pushed image no release
keeps is removed by the registry's prune, a built one by the next
deploy's image cleanup, as for a failed release.

**A cancel is no outcome.** Nothing that reacts to a failed deploy
reacts to a canceled one: no deploy alert (live, or on reconciliation,
which looks past canceled releases to the newest real outcome), no
diagnosis, no Needs attention, no `last_failed_deploy` (which looks past
them too, so an earlier failure stays the news), no failure in a
preview's status. A GitHub commit status, which has no canceled state,
says `error` "Release N canceled by NAME" rather than `failure`. The
changes the release carried are staged again, as after a failure. A
canceled promotion step cancels the steps after it ("not promoted: web
was canceled first"), so a promotion never fails services it didn't try.

**What keeps serving.** The release that served before keeps serving
throughout; for a stateful service, which stops its old release first,
that release is started again as on a failure. A stopped service stays
stopped.

**Who.** The deploy permission (`PermDeploy`), the same as starting one,
through `serviceFor` with deploy scope, so a locked environment refuses
tokens. Recorded as the audit event `release.cancel`. The API answers
once the release is canceled (cleanup takes seconds, at most the cleanup
timeout), so the CLI prints the outcome rather than a promise.

**Exit codes.** Following a release that was canceled (`dock deploy`,
`dock release wait`, apply, promote) exits 10, `deploy_canceled`, not 6:
a script can tell a deliberate stop from a broken build.

## Consequences

- A mistaken deploy costs seconds instead of its whole run plus a
  rollback.
- A pre-deploy command that already ran (a migration) is not undone by a
  cancel; the guide says so. Canceling while it runs stops it where it
  is, as its timeout would.
- Cancels exist only on the control plane running the deploy, in memory.
  A deploy in flight across a daemon restart is failed by recovery, as
  before; a cancel in that window is refused with "try again in a
  moment", by when recovery has failed it.
- Every client that branched on `failed` had to learn `canceled`;
  `inFlight` checks in the UI now list the in-flight statuses instead of
  the finished ones, so a new status can't read as in flight.

## Amended 2026-11-27: the deploy queue

Canceling needs knowing what to cancel. Each builder builds one release
at a time, so with a few services deploying at once (a promotion, a
batch of pushes, previews) a deploy could wait minutes at a builder with
nothing saying behind what. There was no place to see what builds, what
waits and for how long.

**One view, beside every page** (Saile's pick B of three mocks,
Postplan `lx9q1wcrx4xw`). While any of the org's releases deploy or
wait, the top bar shows a count ("2 deploying · 3 waiting") that opens a
panel at the right edge, where Help opens, one of the two at a time. It
lists each builder's build and the releases waiting behind it in order,
then the deploys not at a builder (pulling an image, starting, health
checking, queued behind a promotion step or a server move), each with
who or what started it, its commit, the push waiting behind it (ADR
0018), how long it has waited or run, and Cancel where the reader may
deploy. The palette's "Deploy queue" and `dock queue` show it too. Idle,
the top bar shows nothing.

**Who sees what.** People see the releases they can read (`canSee`).
Another org's release, or one they can't read, is a line in its place
("2 releases of others"), so the wait it causes is still clear without
naming it. Builders are "Builder 1, 2..." in the order the servers
joined, not named: servers are the platform's (ADR 0062). The console
has no top bar, so its sidebar has a Deploys row with the count, opening
the same panel over every org: builders by name, each release with its
org, commits by SHA only (an org's content isn't the console's).
Platform viewers read it; operators cancel any release from it
(`POST /console/releases/{id}/cancel`, orgs.administer), recorded in the
org's log and the platform's, to free a builder without joining the org.

**How.** A builder's slot is no longer a channel semaphore but an
ordered line in memory (`buildSlots`): the holder and its waiters, each
with since when, first come first served. A release waiting behind N
builds now waits up to N build timeouts, not one. The view is one query
(the releases in flight, from the partial index that holds only them,
with their service, environment, project, org and waiting push) plus the
servers, and the lines from memory. A `deploy_queue` event, with no
payload, goes to each org with a release at a builder whose line moved
and to the console; pages read the queue again only while they show
it, and patch it from release events in between.
