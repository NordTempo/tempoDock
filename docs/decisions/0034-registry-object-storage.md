# 0034: The registry's images in S3-compatible object storage

Status: accepted (decided by the agent, on Saile's request). Amends
[0014](0014-node-protocol.md) section 4 and [0021](0021-control-plane-backup-and-move.md).

## Context

The registry ([0014](0014-node-protocol.md) section 4) keeps its blobs on
the control plane's disk. With the control plane on a small VPS and the
builders elsewhere, that disk fills with images, and a control plane
restored on a fresh server can only roll back once its images are built
or pushed again, or after a `--include-registry` archive of many GB
([0021](0021-control-plane-backup-and-move.md)).

Saile asked for the option to keep the images in object storage (Hetzner
Object Storage for him), with two conditions: it stays optional and
provider-neutral, since Dock will be open-sourced, and S3 credentials are
defined once and reused, since backups already take an S3 repository
([0019](0019-templates-and-backups.md)).

| Force | Means here |
|---|---|
| Optional | A single-server install needs nothing: the disk stays the default |
| Neutral | Endpoint, region, bucket, keys and path-style addressing: AWS, Hetzner, Cloudflare R2, Backblaze B2, MinIO and Garage all work. No Azure or GCS drivers |
| Defined once | One named connection per bucket, for the registry now and backups next, not a second S3 setup |
| Tokens stay | Nodes pull through their registry proxy with per-call tokens (0014 amendment); nothing changes for them |
| Idle means idle | No polling, no cache, no new timers |
| Secrets | Keys are sealed at rest and never returned by the API |

## Decision

| Topic | Decision |
|---|---|
| Storage connections | `storage_connections` in SQLite: name, endpoint (base URL, no bucket), region (empty signs as `us-east-1`), bucket, path-style (default on), and the access key ID and secret sealed together with the secrets key. Operators manage them: `dock storage [add\|set\|remove]`, `GET/POST /storage`, `PATCH/DELETE /storage/{name}`, the console's Storage page. Saving lists the bucket once with the keys (15 s), so a wrong endpoint, bucket, region or key fails then, with a message naming which |
| The registry's setting | `platform_settings.registry_storage_id`: NULL is the disk, else the connection. A runtime setting, not config.toml: the daemon changes it, and a restored database carries it to a new server. A connection in use can't be removed or pointed at another endpoint or bucket; new keys or addressing restart the registry |
| Config | Distribution's `s3` driver in `config.yml`: `regionendpoint`, `region`, `bucket`, `forcepathstyle`, the keys, `rootdirectory: /dock-registry`, so a bucket can hold other things next to it. `config.yml` is 0640 in a config dir owned by `dock` and setgid `dock-registry`: the daemon writes it, the registry reads it |
| Who writes it | `dock admin registry-config` (install.sh, every run) and the daemon: on start (`UseStorage`, so the config always follows the setting, even after a crash in the middle of a migration), after a migration, and when a connection's keys change. Writes are atomic and only when the content changes; a change restarts the registry and waits for it to answer |
| Redirects | `redirect: disable`. Every pull goes through the registry and so through each node's proxy and its tokens; no presigned bucket URLs leave the control plane. Revisit only if pulls through the registry turn out too slow |
| Size in the console | `Stored` pages through `ListObjectsV2` of `dock-registry/docker/registry/v2/blobs/` when an admin opens the page, 1000 objects a request. No cache |
| Migration | `dock registry migrate --to s3 [--storage NAME]` or `--to local`, the Registry page's "Move images", `POST /registry/migrate`. In the background, holding the registry like a prune (builds and pulls wait; refused while one runs): the registry restarts read-only on its current storage, the target is prepared (a bucket must hold nothing under `dock-registry/`, unless its `dock-registry/dock-install` names this install: then it is what an interrupted migration left, and is deleted; the marker is written for the next one. The storage dir is emptied, being a stale copy), every object under `docker/` except unfinished uploads is copied, the target is listed to check the count and bytes, the setting is committed, and the registry restarts writable on the target. Then the old copy is deleted. A failure before the commit restarts the registry as it was and removes the partial copy from a bucket |
| How it copies | In the daemon, with a small SigV4 client (`internal/s3`, no dependencies). Disk to bucket reads the storage dir (the `dock` group may read it) and uploads 8 objects at a time, each up to 5 GB in one PUT, retrying a failed object twice. Connecting, TLS and waiting for an answer are bounded (30 s, 30 s, 2 min), so a store that stops answering fails the migration instead of holding the registry Bucket to disk streams one tar into the registry container through Docker's archive API, so the files belong to the registry's user. Bucket to bucket streams each object through the daemon |
| Garbage collection | The same `registry garbage-collect` container. In a bucket it gets the registry's network (on disk it has none) and an hour instead of 15 minutes |
| Installer | When install.sh turns the registry on and there is a terminal, it asks: this server's disk (preselected) or S3-compatible storage. `REGISTRY_STORAGE=local\|s3` and `REGISTRY_S3_*` answer without a terminal. For S3 it runs `dock admin registry-storage` before the daemon starts: the connection is saved (the bucket listed) and the registry points at it. Refused when the storage dir already has images (use migrate) or the bucket already holds a registry |
| Backups | Unchanged in this PR; see Follow-up |

## Why this shape

**Connections, not a `[registry]` S3 section.** The plan had
`[registry] storage = "s3"` with the endpoint and keys in config.toml. A
named connection in SQLite is defined once, is sealed like every other
secret, can be changed from the console without a root shell, and comes
back with a restored database, which is the point of moving the images:
a restored control plane on a new server points its registry at the same
bucket and rolls back at once.

**A bucket per connection.** Saile's list of what S3-compatible means
includes the bucket, and the plan wants the registry and backups in
different buckets. A connection is therefore one bucket with its keys;
each user picks its own prefix in it (`dock-registry/` for the registry).
Two connections may share keys.

**In-process copying.** rclone in a container would copy just as well,
but the daemon needs an S3 client anyway (the size, the check when a
connection is saved), so the copy is a few dozen more lines instead of
another pinned image. The client is about 400 lines with comments and was checked
against AWS's published SigV4 example and against MinIO.

**Holding the registry for the whole migration.** Builds and pulls wait,
as they do for a garbage collection, rather than failing. Letting pulls
through during the copy would need a second, weaker lock for little gain:
a migration is a one-off at a quiet time. The registry is also read-only
for the copy, so nothing can write to the source even with a token
minted before it started.

## Consequences

- With the images in a bucket, the control plane's disk holds only what
  Docker runs, and `dock admin backup` never needs `--include-registry`:
  a restored control plane's registry uses the same bucket (0021's
  amendment).
- Two control planes must never run their registries on one bucket
  prefix: each one's garbage collection deletes the other's layers.
  Migration and install.sh refuse a bucket that already holds
  `dock-registry/`; a move (0021) retires the old registry, and
  `--old-server-becomes-node` removes its container.
- Pulls from a bucket cost a request to it per blob instead of a disk
  read. Measured locally (MinIO on the same machine, `scripts/registry-spike.sh`,
  docs/experience.md) the difference is under a second for a 281 MB image;
  against a real bucket it depends on the provider and the distance, and
  Saile measures Hetzner next.
- Garbage collection lists the bucket and is slower than on disk; it runs
  when a deploy or archive prunes, at most hourly, and only stops pushes
  and pulls while it runs.
- Distribution 3.1's S3 driver (aws-sdk-go v1) sends no checksum headers
  that some S3-compatible stores reject. Stores other than MinIO are
  untested here.
- A migration's speed is the link to the bucket; the registry is read-only
  and deploys wait until it ends.
- A migration copies each object in one request, so a layer over 5 GB
  fails it (and it rolls back). Distribution's own driver uses multipart
  uploads for those; the migration can follow if images that large show
  up.
- One migration (0029), a config key (`[registry] config_dir`), install.sh
  makes the storage dir 0750 group `dock` and the config dir setgid.

## Follow-up: backups on storage connections

Backups keep their own repository settings (a restic URL, sealed keys
and a region) in this PR: moving them is a separate change to a working
production path, with its own migration of the existing sealed settings.
The connection already has the shape they need:

| Backup setting | From a connection and a path |
|---|---|
| `RESTIC_REPOSITORY` | `s3:<endpoint>/<bucket>/<path>` |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | the connection's keys |
| `AWS_DEFAULT_REGION` | its region |
| path-style | restic's `-o s3.bucket-lookup=path` (else `dns`) |

The follow-up: `backup_settings` gains `storage_id` and `path` and loses
`repository` and `credentials`; a migration turns an existing `s3:` URL
into a connection named after its bucket; `dock backup configure
--storage NAME [--path P]` and the Backups page pick a connection;
`backups.repository` keeps the derived URL, so the history and
`markPruned` work as before.

## Amendment (2026-10-30): backups follow

The follow-up above is done in [0039](0039-backups-on-storage-connections.md):
backups go to a storage connection and a folder, an old repository URL
becomes a connection on start, and `dock backup move` moves the existing
backups there and empties where they were. Moving between buckets needs
the same endpoint and keys.

