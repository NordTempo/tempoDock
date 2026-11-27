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
goroutines, through `conncache` like the other engines. Measured the
same way with it: 0.29 MB (64.06 to 64.34 MB).

How it maps onto the panel (Saile's mock pick A, 2026-11-27):

- **Engine `mongodb`** (`internal/data/mongo`) implements `data.Engine`
  and `data.Tables`: a collection is a table named `database.collection`,
  its documents' top-level fields are the columns (named from a sample
  of 20 documents, typed `bson:<type>`, `mixed` when they differ), and
  `_id` is the key. A cell is the field's canonical Extended JSON text
  (`{"$numberInt": "2"}` stays an int), null when the document lacks the
  field. So the existing table API, `dock db` and the MCP data tools work
  for MongoDB unchanged, and values make the round trip without losing
  their type.
- **The Documents tab** (mock A): the collection's grid, cells as the
  shell shows values (`open`, `249.00`, `{ email, name }`, `[3]`), and
  the open document as a tree of typed fields in the drawer. A filter box
  above the grid takes a find filter (`where` on the rows API, `dock db
  rows --where`), ANDed with the column filters; it refuses what runs
  code and, for masked callers, any field that touches a masked path
  (`$expr` and `$jsonSchema` too, which name fields inside strings). The
  panel says documents and collections for MongoDB.
- **Filters, keys and changes** take Extended JSON as typed in the
  shell: unquoted keys, single quotes, `ObjectId("...")`,
  `ISODate("...")`, `NumberLong(..)`, `NumberDecimal("..")`. A bare word
  is a string (`status = open`); `like` and `ilike` become an anchored
  `$regex`. Pages go by offset (sorted by `_id`, or a field then `_id`),
  10 000 documents deep at most, as MySQL's keyless tables do.
- **Users:** `dock_reader` (`readAnyDatabase`), `dock_writer`
  (`readWriteAnyDatabase`) and `dock_monitor` (`clusterMonitor`), in
  `admin`, created by the root user at setup, SCRAM-SHA-256 only. The
  template sets `database = mongodb`, so a new MongoDB gets its Data tab
  set up when it first runs, as Postgres does.
- **Masking:** MongoDB can't deny a role one field, so the engine masks.
  A sensitive mark is a field path (`customer.email`); a page shows the
  top-level field masked, or the path inside it as `{"$masked": true}`,
  and refuses to filter, sort or change anything that touches it, array
  positions included (`items.0.email` touches `items.email`). A mark may
  name a path inside a column, which the panel checks by its first part.
  A masked caller gets no console while any field is marked
  (`data.MasksItself`), as Redis does for key patterns, and no view of a
  database with marks (nor its sampled columns): a view can show a marked
  field under another name, and Dock doesn't follow its pipeline. A
  masked caller's filter can't use `$text`, which searches whatever
  fields a text index covers.
- **Changes:** one document per changeset. A single server has no
  transactions, and one `updateOne`, `insertOne` or `deleteOne` is atomic
  on its own. An update and a delete match `_id` (with `$eq`, so a key
  shaped like an operator stays a literal) and every old value the
  editor saw, compared whole with `$expr` (a query's `{x: "B"}` would
  also match an array holding `"B"`, and null a missing field); a field
  seen missing must still be missing. A document whose seen fields
  changed since is a conflict with its current values, and nothing is
  written. The guard covers the fields the editor saw: an update writes
  only those, and a delete removes the document even if it gained other
  fields since. Guards compare strings byte for byte (`collation: simple`),
  whatever the collection's collation, and a top-level field's BSON
  type too (`$eq` takes int32 7 for int64 7); inside a document or an
  array, values compare as `$eq` does, so a nested int32 7 that became
  int64 7 isn't a conflict. A change names a top-level field, as
  the grid's columns are: a dotted name would be read as a path (the
  field `a.b` isn't `a.b` inside `a`, and `tags.0` or `tags.$[]` reach
  into arrays the guard can't compare), so it changes the whole field. A
  failed write says the server's error code, never its message, which
  can quote values (a duplicate key's).
- **Console** (the Query tab, `dock db query`): one read in the shell's
  words: `find` (with `sort`, `project`, `limit`, `skip`), `findOne`,
  `aggregate`, `countDocuments`, `estimatedDocumentCount`, `distinct`,
  `show dbs` and `show collections`, in the URL's database unless
  `db.getSiblingDB("x")` says otherwise. `$out`, `$merge`, `$where`,
  `$function` and `$accumulator` are refused before anything is sent; the
  reader couldn't write anyway. Columns are the first batch's fields,
  and a later document's new ones go in a last `(other fields)` column,
  so the result still streams.
- **Health:** the overview from `serverStatus` (connections, uptime,
  size) and the sessions from `currentOp`, with Kill (`killOp`) as the
  owner. Dock doesn't turn the profiler on, so there is no queries view.
- **Restore drills** count the restored copy's collections.
- **tempoBase** registers Postgres only, as for MySQL and Redis (0072);
  the engine and the panel's MongoDB view are shared, so adding it there
  is a registration and a look at its budget.

## Consequences

- One more template to keep pinned (`scripts/templates pin-db` covers
  it) and one more engine in the backup, restore and copy scripts.
- MongoDB 5.0 and later need AVX on x86; some virtual machines hide it.
- The image is large (about 800 MB unpacked) next to the others, paid
  once per server that runs it.
- The image declares a second volume (`/data/configdb`, used only by
  config servers), which stays an empty anonymous volume.
