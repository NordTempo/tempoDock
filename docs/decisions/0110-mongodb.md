# 0110: MongoDB as a first-class database

Status: accepted (decided by the agent for the coordinator, 2026-11-27).
Extends [0019](0019-templates-and-backups.md) (templates and backups),
[0061](0061-one-restore-model.md) (one restore model),
[0092](0092-preview-database-copies.md) (preview copies),
[0096](0096-checked-backups.md) (checked backups) and
[0072](0072-mysql-and-redis-in-the-data-panel.md) (the Data panel's
engines).

## Context

Postgres, MySQL, MariaDB, Redis and Valkey are templates: a pinned image,
generated credentials, backups with the database's own tools, restores
into a new service, preview copies and restore drills. MongoDB is the
document database most self-hosted apps and Node stacks ask for, and on
Dock it ran as a plain image service: its volume backed up file by file
while it wrote (an inconsistent copy), no restore that loads a dump, a
compose import that warned "Dock has no template for mongo".

## Decision

### Phase 1: the template

- **`mongodb`**, on the official `mongo:8.0` image, pinned by digest and
  following its tag like every template ([0074](0074-image-vulnerability-scanning.md),
  amendment 2026-11-25). Its licence is the SSPL, shown in the catalog.
  Dock runs the image unchanged; it doesn't ship or modify MongoDB.
- **Credentials:** `MONGO_INITDB_ROOT_USERNAME` (`root`) and
  `MONGO_INITDB_ROOT_PASSWORD` (generated), which the image's entrypoint
  turns into the root user on a new volume before it enables
  authentication. Others reference **`MONGODB_URI`**,
  `mongodb://root:...@db:27017/app?authSource=admin`: the name MongoDB's
  own drivers and docs use (`MONGO_URL` is a few frameworks' habit; one
  name is enough). The app's database is `app`, like the other templates.
- **Memory:** 512 MB, with `--wiredTigerCacheSizeGB 0.25` (the cache's
  smallest). Left alone, WiredTiger takes half the memory it sees less a
  gigabyte. Idle, a new server holds about 100 MB.
- **Health:** a TCP connect on the network, as for MySQL and Redis. The
  entrypoint creates the root user on a server listening on its loopback
  alone, so the port opens only once the real server runs.
- **Backups:** `mongodump --archive` of every database, from the
  template's own image, streamed into restic (`mongodb.archive`). A
  single server has no oplog, so `--oplog` can't make the dump one moment
  in time: writes during the dump can leave it inconsistent, within a
  collection as well as across them. For an exact copy, stop the app's
  writes while it runs (or take a volume snapshot, ADR 0047). The
  tools read the password from a config file the job writes (`--config`),
  never an argument, which every process on the server could see.
- **Restores** go into a new service (0061): `mongorestore` into the new,
  running server as its root, leaving out `admin` and `config`. Those
  hold the server's users and settings, and the new one has its own root
  password; restoring `admin.system.users` would put the old password
  back. So, as with MySQL's `mysql` schema, users an app created with
  `db.createUser` are not restored. Recovering a lost server (0098) loads
  the same way into the service it recreates.
- **No point-in-time restore.** It needs the oplog, so a replica set,
  which the template isn't. `to_time` stays Postgres's (0093).
- **Restore drills** (0096) restore the newest snapshot and check that
  the load finished and the copy runs ("restored and running"): there
  is no Data panel connection to count with until phase 2, and a drill
  reads a database through nothing else. Phase 2 counts its collections.
- **Preview copies** (0092): `copy` dumps the base into the scratch
  volume and loads it into the preview's own new server, as for MySQL;
  the size checked against `copy_max_mb` is the databases' `dataSize`,
  uncompressed as the dump is, plus a tenth for the archive's own
  framing, plus their `indexSize` for the indexes the load builds again. **Masking** isn't possible yet: documents have
  no columns, and a masked copy of a MongoDB with sensitive marks starts
  empty with that reason. Without marks it is a plain copy, as Redis's is
  without key patterns. `snapshot` works as for any volume.
- **Compose import:** the official `mongo` image becomes the template.
  `MONGO_INITDB_ROOT_PASSWORD` is generated instead (other services'
  copies of the old value become references), a username other than
  `root` is kept, and `mongodb://` URLs pointing at it keep their path
  and options and take the template's credentials with
  `authSource=admin`. Other repositories (`bitnami/mongodb`) stay image
  services with a warning.
- **Store:** migration 0141 widens the CHECKs on `services.template`,
  `backups.kind`, `services.database` and `data_access.engine`; the last
  two for phase 2, so one migration covers the feature.

### Phase 2: the Data panel

Browse databases and collections, view and query documents, and edit
them within the panel's rules and audit, in Dock and tempoBase. Documents
aren't rows, so the UI starts from mocks.

The client: the official Go driver (`go.mongodb.org/mongo-driver/v2`
v2.10.1, Apache-2.0, with `xdg-go/scram`, `xdg-go/stringprep` and
`youmark/pkcs8`) adds 2.98 MB to `bin/dock` (64.15 to 67.13 MB,
stripped, measured 2026-11-27 with connect, list, find, update,
aggregate and a command linked in). That is past the binary budget
(baseline + 3%) on its own, and tempoBase's 21 MB limit would go with it.
So, as for MySQL and Redis (0072), Dock gets its own small client: BSON,
`OP_MSG` and SCRAM-SHA-256 over `Target.Dial`, no pool, no monitoring
goroutines, through `conncache` like the other engines.

## Consequences

- One more template to keep pinned (`scripts/templates pin-db` covers
  it) and one more engine in the backup, restore and copy scripts.
- MongoDB 5.0 and later need AVX on x86; some virtual machines hide it.
- The image is large (about 800 MB unpacked) next to the others, paid
  once per server that runs it.
- The image declares a second volume (`/data/configdb`, used only by
  config servers), which stays an empty anonymous volume.
