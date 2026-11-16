# 0084: The registry's storage gateway: pushes land on local disk first

Status: accepted (decided by the agent on the coordinator's request,
2026-11-16; the Console section is mock B). Amends
[0034](0034-registry-object-storage.md) and
[0014](0014-node-protocol.md) section 4.

## Context

Prod's registry keeps its blobs in Hetzner Object Storage (hel1), which
has been partly degraded since 2026-11-13 (Hetzner incident dc7b91f2).
Distribution's S3 driver waits on the bucket for every request, without
a deadline, so a slow store became failed deploys:

- 2026-11-13: one blob's `HEAD` hung 30 s every time. Every push of that
  layer failed until a `dock registry prune` (11 minutes with the
  registry stopped) happened to clear it.
- 2026-11-15: three of four detoo deploys failed at the upload's commit
  (`PUT .../blobs/uploads/...?digest=`, "timeout awaiting response
  headers"); the fourth went through a minute later.

Nothing told the operator the bucket was degraded; #305 only explained a
push after it failed. The options were a retry, a prune, or moving the
images back to disk.

| Force | Means here |
|---|---|
| Deploys keep working | A slow or flaky bucket must not fail or hang a push |
| Operators see it first | A degraded bucket shows before deploys break, and a failed push says why |
| Idle means idle | No health-check ticker against the bucket |
| Keep Distribution | The pinned upstream `registry:3`, no fork or custom image |
| Bounded | The control plane's disk is small; whatever lands there is capped |

## Decision

In bucket mode the registry no longer talks to the bucket. The daemon
serves a small S3-compatible endpoint on loopback, the storage gateway
(`internal/registry/gateway`, `[registry] gateway_listen`, default
127.0.0.1:5001), and Distribution's driver points at it.

| Topic | Decision |
|---|---|
| Writes | Every object the registry writes lands in a spool on the control plane's disk (`[registry] spool_dir`, default `/var/lib/dock/registry-spool`), fsynced with its directory, before the registry hears it is stored. A push never waits on the bucket |
| Unfinished uploads | Everything under a repository's `_uploads/` and every multipart upload stays in the spool and never reaches the bucket. That is most of a push's requests, the commit that hung included. Listing and deleting a known upload don't ask the bucket. They don't survive a daemon restart: Docker starts such a layer over |
| Uploader | Uploads what the spool holds, 4 objects at a time. From 64 MB an object goes multipart in 32 MB parts; a part is tried 3 times, and a failed object resumes after the parts the bucket has. A failure backs off per object, 5 s doubling to 2 min; any call the bucket answers retries them all at once. A spool file is deleted only once the bucket confirmed its upload, so the directory is the queue: after a restart, whatever is in it uploads again. A request whose body or answer doesn't move for a minute is canceled |
| Reads | From the spool when the object is there (the deploy's pull right after its build). Otherwise from the bucket: 20 s to the first byte per attempt, 3 attempts, then the body streams with the one-minute stall watchdog |
| Stuck blobs | A blob whose every read attempt times out is stuck (remembered 10 minutes, at most 1024). While core has a push running (`PushStarted`, around a build that pushes), what a push writes again when told it is missing (blob contents, and a repository's layer, manifest and tag links) gets one 5 s attempt, and is then reported missing, in reads and in the listing Distribution falls back to, so Docker uploads it again: the new copy lands in the spool, is served from there and is uploaded over the stuck one. Docker waits about 30 s for an answer; a needless upload into the spool costs less than a failed push. While the bucket is failing, a push doesn't ask it at all. A pull never gets that answer: outside a push a stuck blob gets every attempt and fails as unavailable. Garbage collection (`SetStrict`) never gets it either: a blob reported missing there would look unreferenced and be deleted |
| Caps | The spool takes at most `[registry] spool_max_gb` (default the smaller of 20 GB and a quarter of its disk), and never leaves less than 10% of the disk free. Past either, writes are refused with 503 SlowDown: the push fails, its error says why, and the alert below turns critical. Refusing clears as uploads free space |
| Health | Every call the gateway makes to the bucket anyway is an outcome in a window of 64 (10 minutes back): failing, or slower than 10 s, is bad. 4 bad make a healthy bucket degraded, the newest 8 all bad make it failing, and an upload waiting over 5 minutes makes it degraded. An unhealthy bucket recovers once the newest 8 calls were good, and the window starts over then |
| Probe | While the bucket is unhealthy and nothing else talks to it, one listing at a time, 1 minute backing off to 5, so the state can recover. A healthy bucket is never probed |
| Alert | Platform alert `registry:bucket` (a connection alert, ADR 0062), raised and resolved on the gateway's transitions: warning while degraded or failing, critical while pushes are refused. It says how many calls failed, what waits to upload, and what to do |
| Failed pushes | When a build's push fails while the gateway sees trouble, the error ends with "the registry's storage: ..." (spool full, disk full, or the bucket's state), and the `push_failed` diagnosis says that instead of "the registry didn't answer" |
| Visible | `dock registry` and the Console's Registry page (a Storage section, one line while healthy) show the bucket's state and recent calls, what waits to upload, the spool and disk, whether pushes are accepted, and stuck blobs. `GET /registry` has it as `bucket` |
| Keys | The registry signs its requests with a key pair derived from the registry's signing key (`GatewayKeys`), which the gateway checks (SigV4, and the body's signed hash). The bucket's own keys are only in the daemon now, not in `config.yml`. New keys for a connection no longer restart the registry |
| Network | The registry container runs on the host's network, listening only on `[registry] listen` (now `http.addr` in `config.yml`), so it reaches the gateway on loopback. install.sh recreates it once ("host" is in its hash), which `dock admin update` runs. A daemon that finds the old container on Docker's bridge network keeps the old config, the bucket direct, until then |
| Migration | Moving the images out of a bucket first waits (up to 10 minutes) for the spool to upload, with the registry already read-only, and fails saying how many objects still wait |
| Distribution config | Path style to the gateway, and `multipartcopythresholdsize` at 5 GB, so committing an upload is one copy, which the gateway makes a hard link |

## Why this shape

**Local-first, not only retries.** Retries and deadlines turn a hang
into a quick failure. They don't turn it into a success: a stuck object
hangs on every retry, and a slow store makes every push slow. Taking the
bucket out of the push is what keeps deploys working while it is slow.

**A gateway, not a fork.** The S3 calls Distribution 3.1's driver makes
are few and known: objects, ranged reads, listings with a delimiter,
copies, batch deletes and multipart uploads. Serving them is a few
hundred lines next to the SigV4 client Dock already has, with no new
dependency. Forking Distribution, or patching its driver, would be an
image to build and pin forever.

**Pretend missing only during a push.** A HEAD that says "missing" is
how Docker learns to upload a layer. It is also how a pull learns to
give up. The gateway can't tell the two apart from the registry's
requests, but core knows when a build is pushing, so it reports a stuck
blob missing only then. A pull that overlaps a push and needs a stuck
blob fails fast instead of after a minute. It would have failed anyway.

**Health from traffic.** Connection alerts (ADR 0062) already work this
way: Dock judges the bucket only by calls it makes for another reason.
The probe runs only while the bucket is unhealthy and idle, so the
alert can resolve without a push.

**The host's network.** The registry needs to reach a loopback listener
on the host. The alternative was a listener on Docker's bridge address,
which host firewalls (ufw's default) block. On the host's network the
registry sees what any local user sees: the daemon's loopback listeners,
which already assume that (the internal listener is for any local user).
Caddy's admin API is a socket the registry can't open.

## Consequences

- **Durability.** An object is acknowledged once it is on the control
  plane's disk, not once it is in the bucket. Until the uploader has sent
  it, the spool holds the only copy. If that disk dies first, the newest
  images are lost, and the next deploy of their services builds them
  again. That is the guarantee the disk mode has for every image. A
  restored control plane (ADR 0021) has the bucket's images, not what
  waited in the spool. `dock registry` says what waits.
- Pushes are as fast as the control plane's disk. Pulls of what was just
  pushed read the disk too. Older images stream from the bucket through
  the daemon, as before through the registry.
- The daemon is in the registry's storage path: while it restarts, the
  registry's bucket calls fail (seconds; nodes reach the registry through
  the daemon's link anyway).
- The daemon writes the spool under its data dir, which `dock.service`
  already may (`ReadWritePaths=/var/lib/dock`). A `spool_dir` elsewhere
  needs its own entry.
- The registry container is recreated on the host's network at the next
  install or update. Its storage, its config dir and the bucket's
  contents are untouched; nothing is uploaded again.
- Unfinished uploads that were in the bucket before stay there until
  Distribution's upload purging removes them (a week old, through the
  gateway's listing).
- Garbage collection runs while the gateway serves, strict. A spool
  that still holds objects is listed with the bucket, so GC sees them.

## Out of scope

- A read cache of the bucket. Pulls right after a push already read the
  spool; add one if pulls of older images turn out slow.
- Hedged reads (a second request racing a slow first one).
- Moving backups ([0034](0034-registry-object-storage.md)'s follow-up)
  behind the gateway.
