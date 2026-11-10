# 0020: Retention, disk pruning and alerts

Status: accepted (decided by the agent); superseded in part by [0062](0062-org-alerts-and-operators.md) (who sees alerts, channels per org); amended by [0043](0043-ongoing-health-checks.md) (unhealthy alerts)

## Context

M4 "day-to-day comfort" asks for disk pruning and alerts. A server that
fills its disk takes every app on it down, and an operator who only
learns about a crash loop or a failed deploy by opening the UI learns too
late. At the same time the daemon shares a small server with its apps:
[performance](../performance.md) says idle means idle, and the statistics
tick ([0015](0015-server-pools-and-placement.md)) is the one always-on
periodic job.

| Force | Means here |
|---|---|
| Idle means idle | No new timers or polling. Retention and disk checks ride on the minute tick; alerts come from events the daemon already has |
| Rollback must keep working | Nothing a rollback needs (the newest `keep_releases` releases and the active one) loses its image or history |
| Secrets stay secret | Webhook URLs (they hold tokens), signing secrets and SMTP passwords are sealed like variables and never returned |
| One way to say it | Alerts read the same in the UI, `dock alerts`, webhooks, Slack and email |

## Decision summary

| Topic | Decision |
|---|---|
| Where it runs | The statistics tick calls the alert engine and retention after storing its points. Retention runs at most hourly; a server's prune at most hourly while its disk is past the threshold |
| Release history | Build log, saved container output and progress events of a failed or superseded release go once it is out of its service's rollback window **and** older than `[retention] build_log_days` (30). Active, deploying and windowed releases keep theirs. Releases are marked `history_trimmed`, so a pass never looks at them again |
| Audit | Trimmed from the start of the log while older than `audit_days` (365) or past `audit_max_events` (0, no cap), with their details |
| Alerts history | Resolved alerts kept `alert_days` (90) |
| Statistics | Unchanged: they prune themselves on the tick |
| Disk pruning | When the filesystem of Docker's data root passes `prune_disk_percent` (80) on a server: dangling images dock made (see the amendment) are removed except the image IDs of releases a rollback may need, and on a builder its own BuildKit's cache is pruned to `build_cache_keep_mb` (2048) with BuildKit's prune API |
| Runtime interface | `Containers.PruneImages(keep, repos)` (amended) and `Builder.PruneBuildCache(keepBytes)`, over the node link as `prune-images` and `prune-build-cache`. An older node answers with an error and is skipped |
| Alert sources | Bus events (release failed or active, service status restarts, domain error or active, server online, removed or revoked) and the tick (disk, server offline, crash loops calming down, reminders). Generic: any event may carry an `alertSignal`, and `signalAlert` raises or resolves one directly |
| Conditions | disk ≥ 85% warning, ≥ 95% critical, resolved below 83%; server offline ≥ 5 min critical; ≥ 3 restarts in 10 min critical; newest release failed warning; domain in error (certificate not issued) warning; latest backup failed warning, resolved by the next successful one |
| State | One row per occurrence (`alerts`), at most one open per key; open alerts in memory. Raising an open key updates it in place; resolving closes it. Start and a lagged bus subscription reconcile deploy and domain alerts from the DB |
| Who sees them | Unlimited admins: API, `alert` events, the Alerts page, overview problems and `dock alerts` |
| Channels | Admin-configured: `webhook` (JSON, `Dock-Signature: t=<unix>,v1=<hex HMAC-SHA256 of "<t>.<body>">`), `slack` (`{"text"}`), `email` (SMTP; 465 TLS, otherwise STARTTLS required before a password) |
| What is sent | `opened`, `escalated` (severity went up), `reminder` (every `renotify_minutes`, default 240, 0 never) and `resolved` (only to channels that got the opening), filtered by the channel's minimum severity |
| Delivery | Per channel, in order, by a goroutine that runs only while the channel has something queued; each notification is tried 5 times (now, 10 s, 1 min, 5 min, 15 min). The outcome is kept on the channel (last sent, last error, failures). Queues are in memory: a restart drops what wasn't sent |

## Retention

A pass trims in batches of 200 rows per short transaction, at most 10
batches per kind, so the first pass after an upgrade (every old release
untrimmed) works off its backlog over a few hours instead of holding the
write lock. Release files go first and the rows after, so a crash in
between leaves the release untrimmed and the next pass finishes it.

The rollback window is the one image pruning already uses: a service's
newest `keep_releases` releases by number, plus active and in-flight ones
(`ListKeptDigests`). Releases inside it keep everything, so `dock rollback`
and the release page always have their log. Outside it, a release's
history stays for `build_log_days` more, so last week's failed deploy can
still be read. The release row itself is never deleted: numbers, images
and errors stay in the history.

Audit events are trimmed from the oldest: IDs and times grow together, so
a pass with nothing to do reads one row. The cap (`audit_max_events`)
counts by ID, which is exact because the log is only ever trimmed from its
start.

## Disk pruning

Release images out of the rollback window already go after each deploy
([0014](0014-node-protocol.md) section 4). What that misses fills disks:
images left dangling (a re-pulled `postgres:17`, a digest-pulled image of
a release that left the window while its server was offline) and BuildKit's
cache, which grows with every build.

Docker's own `image prune` can't be told what to keep, and images pulled
by digest have no tag, so they look dangling even while a rollback needs
them. The node lists dangling images and removes each without force,
skipping the IDs core passes as kept and any a container uses. BuildKit's
cache is pruned least recently used first down to `build_cache_keep_mb`
(`MaxUsedSpace` and `ReservedSpace` both set to it); a BuildKit container
that isn't running isn't started for it. `docker builder prune` isn't
used: the classic builder is never used, all builds go through BuildKit.

Pruning only starts past `prune_disk_percent`, below the warning
threshold, so a disk that only fills with build cache is pruned before it
alerts. A disk alert's message says when pruning already happened, so the
operator knows the rest is volumes or data.

## Alerts

Most conditions already arrive as events on the bus; the engine
subscribes like an SSE client (a goroutine blocked on a channel, no
wake-ups) and evaluates each event in memory, writing only when an
alert's state changes (about 45 ns per event that changes nothing,
`BenchmarkAlertEvent`). Conditions that need a clock (a server offline
for minutes, a disk sample, a crash loop that stopped) use the minute tick
the daemon has anyway. A server counts as offline from the later of its
last-seen time and the daemon's start, so a restart of the control plane,
when every node reconnects within seconds, never alerts.

Keys make alerts idempotent: `disk:<server id>`, `server:<server id>`,
`crash:<p/e/s>`, `deploy:<p/e/s>`, `domain:<hostname>`, and whatever key a
producer chooses (`backup:<p/e/s>`). Names are copied into the alert so
resolved alerts still read right after what they name is gone. Hysteresis
(disk) and windows (crash loops) keep conditions near their thresholds
from flapping.

Other features raise alerts without the engine knowing them: attach an
`alertSignal{Key, Kind, Severity, Title, Message, ...}` to the event they
publish anyway (`busEvent.alert`), or call `signalAlert`; a signal with
`Resolve` closes the key. Backups use this (`backup_failed`).

Admins can resolve an alert by hand, for conditions that never clear by
themselves; a condition still present opens a new alert.

## Notifications

Channels are managed with `dock alerts channel add|list|remove|test` and on
the Alerts page. Their URL, signing secret and SMTP settings are one JSON
document sealed with the secrets key; the API returns only a target
without secrets (`https://hooks.slack.com`, the email recipients). A
webhook without a secret gets a generated one, shown once.

Webhook URLs may point almost anywhere, private addresses included (a
homelab ntfy or a chat bridge): only unlimited admins configure them, and
they already control what runs on every server. Not the server itself or
cloud metadata, though (see the amendment). Redirects are not followed.

## Consequences

- Disks stay below the warning line without anyone running `docker system
  prune`, and nothing a rollback needs is removed.
- An operator hears about full disks, offline servers, crash loops, failed
  deploys, certificate failures and failed backups where they already are.
- The minute tick does a little more: a loop over servers and, while
  alerts are open, one reminder query. `BenchmarkStatsTick50` covers it;
  the hourly pass is `BenchmarkRetentionPass` (about 50 µs with nothing to
  do).
- Notifications waiting for a retry are lost on restart; the alert itself
  stays open and reminders pick it up.
- Old releases lose their build logs and progress lines after 30 days;
  their errors stay on the release.

## Amendment (2026-10-22): where channels may deliver

Decided by the agent after a security review.

- **Not the server itself, not metadata.** "Webhook URLs may point
  anywhere" let a channel reach what only the daemon can: its
  loopback-only internal listener and registry, which the app firewall
  keeps apps away from, and the cloud metadata service. Webhook and Slack
  channels now refuse loopback and unspecified addresses, link-local and
  multicast ones (169.254.169.254) and the metadata addresses outside
  them (`fd00:ec2::254`, `100.100.100.200`). Literal addresses are refused
  when the channel is added; names are checked on every connection, after
  DNS, so a name that later resolves to 127.0.0.1 is refused too. Private,
  CGNAT and tailnet addresses stay allowed. Email is unchanged: a local
  MTA on 127.0.0.1 is common, and SMTP to an HTTP service reads nothing.
- **Signatures are made per attempt.** `t` in `Dock-Signature` is when the
  attempt is sent, not when the alert was: receivers reject old
  timestamps, and the last retry goes out about 20 minutes later.
- **Slack text is escaped** (`&`, `<`, `>`): alert messages quote release
  errors and build output, which must not become `@channel` mentions or
  disguised links.
- Email envelopes use the bare address, so a `From` or recipient with a
  name works; the subject is MIME-encoded.

## Amendment (2026-10-22): prune only what dock made

Decided by the agent after a test on real hardware: a node joined to a
machine also used for other work had its disk past `prune_disk_percent`,
and the prune removed 43 untagged images that weren't dock's (10 GB). Any
dangling image on the host counted. Now pruning only ever touches what
dock made, and a few neighbouring gaps are closed.

| Topic | Now |
|---|---|
| Built images | Every image dock builds carries the label `dock.image=true`. The frontend (Dockerfile or Railpack) runs from a BuildKit build function, which adds the label to the image config it returns before the export; Railpack's frontend takes no label options, so this is the one way that works for both |
| Pulled images | A digest pull can't be relabelled. Images pulled from the platform registry are known by their repository: a digest reference under `<registry host>/dock/p` (`127.0.0.1:5000/dock/p1_e2_s3@sha256:...`). Core passes that prefix with the keep set (`PruneImagesRequest{Keep, Repos}`) |
| What is pruned | Dangling images with the label or such a digest reference, not in the keep set, used by no container. Anything else is never removed, however long it has been dangling: CI images (`--image`), templates (`postgres:17`, whose old copy dangles when the tag moves) and every image of other work. Those are the operator's to prune; dock can't tell its `postgres:17` from anyone else's |
| Older nodes | Their `prune-images` removes every dangling image, so the control plane doesn't call it on nodes below protocol 3: they are skipped with an error in the log, like a node without the operation |
| BuildKit cache | Pruned only through dock's own BuildKit container (`dock-buildkit`, reached by `buildctl dial-stdio` inside it), so buildx builders, Docker's own builder and their caches are out of reach. The container must carry dock's labels and this runtime's owner (below), or nothing is pruned |
| One runtime per Docker host | The BuildKit container's name is fixed, and two runtimes on one Docker host (with different settings) recreated each other's container and killed builds. Rather than a configurable name, the container records its owner (`dock.owner`, the runtime's data dir); another runtime refuses to use, recreate or prune it, with an error saying to run one dock runtime per Docker host or remove the container if its runtime is gone. A container from before the label is taken over once. The smaller and safer option: a name prefix would let two runtimes build side by side, which nothing needs, and the default would still collide |
| Archived services | They are never rolled back and can't be restored, so their releases leave the keep sets: `ListKeptDigests` (registry) and `ListKeptImageIDs` (servers) skip archived services. An archive starts a registry prune (at most hourly, as after deploys), so a test project's images leave the registry without another deploy. A digest another, live release uses stays |
| Archived environments' networks | A project archive removes its environments' networks (`Containers.RemoveNetwork`, `remove-network`, protocol 3) once their containers are stopped, so their /24s go back to `[network] pool`, which otherwise ran out after 256 archived environments. Networks on offline or revoked servers stay; the archive's notes name them for `docker network rm` |

No new periodic work: the prune is still a list and removals while a disk
is full, and archives prune the registry the way deploys do.
