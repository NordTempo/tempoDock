// Package dbcopy holds the job scripts that copy a template database into
// a preview environment (ADR 0092): a dump job reads the base's live
// database over its environment network into a scratch volume, and a load
// job writes that into the preview's own database. A masked copy replaces
// the sensitive columns (the Data panel's marks) while it reads the base,
// so their values never reach the scratch volume or the preview.
//
// The scripts run in the template's pinned image (pg_dump, mysqldump,
// redis-cli), never the service's own. Core runs them; this package only
// says what to run and reads what they print.
package dbcopy

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Engine is a database engine copies support.
type Engine string

const (
	Postgres Engine = "postgres"
	MySQL    Engine = "mysql"
	Redis    Engine = "redis"
)

// Path is where the scratch volume is mounted in both jobs.
const Path = "/copy"

// Column is one sensitive column: its schema (MySQL: its database), table
// and name.
type Column struct {
	Schema string `json:"s"`
	Table  string `json:"t"`
	Column string `json:"c"`
}

// Source is how the dump job reaches the base's database, and what it
// copies.
type Source struct {
	Host     string
	Port     int
	User     string
	Password string
	// Database is the Postgres database copied; MySQL copies every one
	// but the server's own, Redis everything.
	Database string
	// Masked are the columns replaced in the copy; empty copies as is.
	Masked []Column
	// MaxBytes refuses a database larger than this.
	MaxBytes int64
}

// Target is how the load job reaches the preview's database. Redis loads
// into its volume instead, mounted at DataDir before it starts.
type Target struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
	DataDir  string
}

// ExitTooBig is the dump job's exit code when the database is past the
// copy limit or the disk has no room for it: not a failure of the copy,
// a reason not to make one.
const ExitTooBig = 3

// DumpScript is the dump job's script for e.
func DumpScript(e Engine) (string, error) {
	switch e {
	case Postgres:
		return prelude + room + pgDump, nil
	case MySQL:
		return prelude + mysqlTools + room + mysqlDump, nil
	case Redis:
		return prelude + room + redisDump, nil
	}
	return "", fmt.Errorf("no copies of %s databases", e)
}

// LoadScript is the load job's script for e.
func LoadScript(e Engine) (string, error) {
	switch e {
	case Postgres:
		return prelude + pgLoad, nil
	case MySQL:
		return prelude + mysqlTools + mysqlLoad, nil
	case Redis:
		return prelude + redisLoad, nil
	}
	return "", fmt.Errorf("no copies of %s databases", e)
}

// DumpEnv is the dump job's environment for src.
func DumpEnv(e Engine, src Source) map[string]string {
	marks := src.Masked
	if marks == nil {
		marks = []Column{}
	}
	list, _ := json.Marshal(marks)
	env := map[string]string{
		"DOCK_MAX_BYTES": strconv.FormatInt(src.MaxBytes, 10),
		"DOCK_MARKS":     string(list),
	}
	switch e {
	case Postgres:
		env["PGHOST"], env["PGPORT"], env["PGUSER"], env["PGPASSWORD"], env["PGDATABASE"] = src.Host, strconv.Itoa(src.Port), src.User, src.Password, src.Database
		env["DOCK_PLAN_SQL"] = pgPlanSQL
	case MySQL:
		env["DOCK_DB_HOST"], env["DOCK_DB_PORT"], env["MYSQL_PWD"] = src.Host, strconv.Itoa(src.Port), src.Password
		env["DOCK_MARKS_HEX"] = hex.EncodeToString(list)
		env["DOCK_PLAN_SQL"] = mysqlPlanSQL
	case Redis:
		env["DOCK_DB_HOST"], env["DOCK_DB_PORT"], env["REDISCLI_AUTH"] = src.Host, strconv.Itoa(src.Port), src.Password
	}
	return env
}

// LoadEnv is the load job's environment for to.
func LoadEnv(e Engine, to Target) map[string]string {
	switch e {
	case Postgres:
		return map[string]string{"PGHOST": to.Host, "PGPORT": strconv.Itoa(to.Port), "PGUSER": to.User, "PGPASSWORD": to.Password, "PGDATABASE": to.Database}
	case MySQL:
		return map[string]string{"DOCK_DB_HOST": to.Host, "DOCK_DB_PORT": strconv.Itoa(to.Port), "MYSQL_PWD": to.Password}
	case Redis:
		return map[string]string{"DOCK_DATA": to.DataDir}
	}
	return nil
}

// Secrets are the environment keys a job's log must not show.
var Secrets = []string{"PGPASSWORD", "MYSQL_PWD", "REDISCLI_AUTH"}

// Stats is what a dump job reports: the database's size, and how many
// tables and columns it masked.
type Stats struct {
	Bytes   int64
	Tables  int
	Columns int
}

// statsMarker precedes the dump job's numbers in its output.
const statsMarker = "dock-copy:"

// ParseStats reads a dump job's numbers from its output.
func ParseStats(out string) (Stats, error) {
	for line := range strings.Lines(out) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), statsMarker)
		if !ok {
			continue
		}
		var s Stats
		if _, err := fmt.Sscanf(strings.TrimSpace(rest), "bytes=%d tables=%d columns=%d", &s.Bytes, &s.Tables, &s.Columns); err != nil {
			return Stats{}, fmt.Errorf("read the copy's numbers %q: %w", rest, err)
		}
		return s, nil
	}
	return Stats{}, fmt.Errorf("the dump job printed no numbers")
}

// prelude starts every script: stop at the first failure, in pipes too.
const prelude = `set -eu
if (set -o pipefail) 2>/dev/null; then set -o pipefail; fi
D=` + Path + `
`

// room refuses a database past DOCK_MAX_BYTES, or one the server's disk
// can't hold twice (the dump, and the preview's copy of it) while keeping
// a tenth free, as snapshots do (ADR 0047). Each dump script sets size
// (bytes) first and calls it.
const room = `room() {
  if [ "$size" -gt "$DOCK_MAX_BYTES" ]; then
    echo "dock: the database is $((size / 1048576)) MB, past the $((DOCK_MAX_BYTES / 1048576)) MB previews copy ([previews] copy_max_mb)" >&2
    exit 3
  fi
  set -- $(df -Pk "$D" | tail -n 1)
  need=$((size / 1024 * 2 + $2 / 10))
  if [ "$4" -lt "$need" ]; then
    echo "dock: the server has $(($4 / 1024)) MB free and a copy of $((size / 1048576)) MB needs $((need / 1024)) MB" >&2
    exit 3
  fi
}
find "$D" -mindepth 1 -delete
`

// pgDump dumps the base's database as of one point in time: a session
// holds a snapshot (pg_export_snapshot) while pg_dump and the masked
// tables' COPY read in it. The plan query (pgPlanSQL) lists the tables
// with masked columns; pg_dump leaves their data out (--filter), and each
// is read as COPY (SELECT <masked expressions>), so a sensitive value
// never leaves the base. pg_stat_statements stays out: the preview's own
// first start creates it (afterActive).
const pgDump = `size=$(psql -X -Atq -c 'SELECT pg_database_size(current_database())')
room
mkdir -p "$D/masked"
mkfifo /tmp/hold
psql -X -Atq -v ON_ERROR_STOP=1 </tmp/hold >/dev/null &
hold=$!
exec 3>/tmp/hold
printf '%s\n' 'BEGIN ISOLATION LEVEL REPEATABLE READ, READ ONLY;' 'SELECT pg_export_snapshot() \g /tmp/snap' >&3
i=0
until [ -s /tmp/snap ]; do
  kill -0 "$hold" 2>/dev/null || { echo "dock: couldn't hold a snapshot of the database" >&2; exit 1; }
  i=$((i + 1)); [ "$i" -lt 300 ] || { echo "dock: the database didn't answer in 30s" >&2; exit 1; }
  sleep 0.1
done
snap=$(cat /tmp/snap)
in_snap() { printf "BEGIN ISOLATION LEVEL REPEATABLE READ, READ ONLY;\nSET TRANSACTION SNAPSHOT '%s';\n%s;\nCOMMIT;\n" "$snap" "$1"; }
in_snap "$DOCK_PLAN_SQL" | psql -X -Atq -F ' ' -v ON_ERROR_STOP=1 -v marks="$DOCK_MARKS" >/tmp/plan
: >/tmp/filter
tables=0
columns=0
while read -r n filter out load; do
  tables=$((tables + 1)); columns=$((columns + n))
  printf '%s\n' "$filter" | base64 -d >>/tmp/filter; echo >>/tmp/filter
  printf '%s' "$load" | base64 -d >"$D/masked/$tables.sql"
  in_snap "$(printf '%s' "$out" | base64 -d)" | psql -X -q -v ON_ERROR_STOP=1 >"$D/masked/$tables.copy"
done </tmp/plan
pg_dump --snapshot="$snap" -Fd -f "$D/db" --filter=/tmp/filter --exclude-extension=pg_stat_statements
printf '%s\n' 'COMMIT;' >&3
exec 3>&-
wait "$hold"
echo "` + statsMarker + ` bytes=$size tables=$tables columns=$columns"
`

// pgPlanSQL lists the base's tables with masked columns (DOCK_MARKS, the
// psql variable marks), one line each: the number of columns masked, then
// base64 of pg_dump's filter line leaving its data out, of the COPY that
// reads it masked, and of the COPY that loads it. A marked table's
// partitions and inheritance children count as marked: they hold its rows.
// A masked column becomes NULL, or where it can't be NULL a value of its
// type that says nothing: random text (masked-<hex>, or hex alone in a
// short column), 0, false, the epoch, empty. A type
// with no such value stays NULL, so its COPY fails and nothing is copied.
const pgPlanSQL = `WITH RECURSIVE marks AS (
  SELECT m.s, m.t, m.c FROM json_to_recordset(:'marks'::json) AS m(s text, t text, c text)
), marked AS (
  SELECT DISTINCT cl.oid AS rel FROM marks
  JOIN pg_namespace n ON n.nspname = marks.s
  JOIN pg_class cl ON cl.relnamespace = n.oid AND cl.relname = marks.t
), tree (root, rel) AS (
  SELECT rel, rel FROM marked
  UNION
  SELECT tree.root, i.inhrelid FROM tree JOIN pg_inherits i ON i.inhparent = tree.rel
), cols AS (
  SELECT DISTINCT tree.rel, marks.c FROM tree
  JOIN pg_class rc ON rc.oid = tree.root
  JOIN pg_namespace rn ON rn.oid = rc.relnamespace
  JOIN marks ON marks.s = rn.nspname AND marks.t = rc.relname
), plan AS (
  SELECT cl.oid, n.nspname, cl.relname,
    count(*) FILTER (WHERE cols.c IS NOT NULL) AS masked,
    string_agg(quote_ident(a.attname), ', ' ORDER BY a.attnum) AS names,
    string_agg(CASE
      WHEN cols.c IS NULL THEN quote_ident(a.attname)
      WHEN NOT a.attnotnull THEN 'NULL'
      ELSE '(' || CASE
        WHEN t.typcategory = 'S' AND a.atttypmod > 4 AND a.atttypmod < 20 THEN 'left(md5(random()::text || clock_timestamp()::text), ' || (a.atttypmod - 4) || ')'
        WHEN t.typcategory = 'S' THEN 'left(''masked-'' || md5(random()::text || clock_timestamp()::text), '
          || CASE WHEN a.atttypmod > 4 THEN (a.atttypmod - 4)::text ELSE '32' END || ')'
        WHEN t.typcategory = 'N' THEN '0'
        WHEN t.typcategory = 'B' THEN 'false'
        WHEN t.typname = 'date' THEN '''1970-01-01'''
        WHEN t.typname IN ('time', 'timetz') THEN '''00:00'''
        WHEN t.typcategory = 'D' THEN '''epoch'''
        WHEN t.typcategory = 'T' THEN '''0'''
        WHEN t.typcategory = 'A' THEN '''{}'''
        WHEN t.typcategory = 'I' THEN '''0.0.0.0'''
        WHEN t.typname IN ('json', 'jsonb') THEN '''{}'''
        WHEN t.typname = 'uuid' THEN 'gen_random_uuid()'
        WHEN t.typname = 'bytea' THEN '''\x'''
        ELSE 'NULL'
      END || ')::' || format_type(a.atttypid, a.atttypmod)
    END, ', ' ORDER BY a.attnum) AS exprs
  FROM (SELECT DISTINCT rel FROM tree) AS leaves
  JOIN pg_class cl ON cl.oid = leaves.rel AND cl.relkind = 'r'
  JOIN pg_namespace n ON n.oid = cl.relnamespace
  JOIN pg_attribute a ON a.attrelid = cl.oid AND a.attnum > 0 AND NOT a.attisdropped AND a.attgenerated = ''
  JOIN pg_type t ON t.oid = a.atttypid
  LEFT JOIN cols ON cols.rel = cl.oid AND cols.c = a.attname
  GROUP BY cl.oid, n.nspname, cl.relname
)
SELECT masked,
  translate(encode(convert_to('exclude table_data "' || replace(nspname, '"', '""') || '"."' || replace(relname, '"', '""') || '"', 'UTF8'), 'base64'), E'\n', ''),
  translate(encode(convert_to(format('COPY (SELECT %s FROM ONLY %I.%I) TO STDOUT', exprs, nspname, relname), 'UTF8'), 'base64'), E'\n', ''),
  translate(encode(convert_to(format('COPY %I.%I (%s) FROM STDIN', nspname, relname, names), 'UTF8'), 'base64'), E'\n', '')
FROM plan WHERE masked > 0
ORDER BY nspname, relname`

// pgLoad restores the dump into the preview's new, empty database, in
// pg_dump's sections: tables, their data, the masked tables' rows, then
// indexes, constraints and triggers, so constraints check the whole copy
// and no trigger fires on it. Objects go to the preview's own user
// (--no-owner); the base's roles and grants aren't copied.
const pgLoad = `i=0
until pg_isready -q; do
  i=$((i + 1)); [ "$i" -lt 120 ] || { echo "dock: the preview's database didn't accept connections in 2 minutes" >&2; exit 1; }
  sleep 1
done
restore() { pg_restore --no-owner --no-acl --exit-on-error -d "$PGDATABASE" "$@" "$D/db"; }
restore --section=pre-data
restore --section=data
for f in "$D"/masked/*.sql; do
  [ -e "$f" ] || continue
  psql -X -q -v ON_ERROR_STOP=1 -c "$(cat "$f")" <"${f%.sql}.copy"
done
restore --section=post-data
psql -X -q -c ANALYZE
`

// mysqlNames makes the script's arguments ("$@") the database names in
// $DBS, one a line, each one argument whatever it holds; a name starting
// with a dash stops the job. The same as backup.MySQLNames, which says
// why.
const mysqlNames = `set -f
IFS='
'
set -- $DBS
unset IFS
set +f
for db in "$@"; do
  case "$db" in -*) echo "dock: the database \"$db\" has a name that starts with a dash, which the dump tool reads as an option; rename it" >&2; exit 1 ;; esac
done
`

// mysqlTools names the MySQL or MariaDB client and dump tool in the image.
const mysqlTools = `CLIENT="$(command -v mariadb || command -v mysql)"
DUMP="$(command -v mariadb-dump || command -v mysqldump)"
AT="-h $DOCK_DB_HOST -P $DOCK_DB_PORT -u root"
`

// mysqlDump dumps every database but the server's own, as backups do: the
// tables first without triggers, then the data of every table without
// masked columns, then the triggers, so none fires on the load. The
// tables with masked columns are read by statements the plan query
// (mysqlPlanSQL) writes: each SELECTs INSERT statements with the masked
// columns' values replaced, so a sensitive value never leaves the base.
// They read in a consistent snapshot of their own, a moment before
// mysqldump's.
const mysqlDump = `SYS="'information_schema','performance_schema','mysql','sys'"
size=$($CLIENT $AT -N -e "SELECT COALESCE(SUM(data_length + index_length), 0) FROM information_schema.tables WHERE table_schema NOT IN ($SYS)")
room
DBS="$($CLIENT $AT -N -e "SELECT schema_name FROM information_schema.schemata WHERE schema_name NOT IN ($SYS)")"
[ -n "$DBS" ] || { echo "dock: the base's server has no databases to copy" >&2; exit 1; }
` + mysqlNames + `printf "SET SESSION group_concat_max_len = 16777216;\nSET @marks = CONVERT(UNHEX('%s') USING utf8mb4);\n%s;\n" "$DOCK_MARKS_HEX" "$DOCK_PLAN_SQL" |
  $CLIENT $AT --batch --raw --skip-column-names >/tmp/plan
tables=0
columns=0
IGNORE=""
while read -r n db t _; do
  tables=$((tables + 1)); columns=$((columns + n))
  db=$(printf '%s' "$db" | base64 -d); t=$(printf '%s' "$t" | base64 -d)
  case "$db$t" in
  *.* | *" "* | *"$(printf '\t')"*) echo "dock: $db.$t has masked columns, and a name with a dot or a space can't be left out of the dump; rename it or copy without masking" >&2; exit 1 ;;
  esac
  IGNORE="$IGNORE --ignore-table=$db.$t"
done </tmp/plan
{
  echo "SET SESSION TRANSACTION ISOLATION LEVEL REPEATABLE READ; START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY;"
  while read -r _ _ _ gen; do printf '%s;\n' "$(printf '%s' "$gen" | base64 -d)"; done </tmp/plan
  echo "COMMIT;"
} | $CLIENT $AT --batch --raw --skip-column-names --default-character-set=utf8mb4 >"$D/masked.sql"
$DUMP $AT --single-transaction --no-data --skip-triggers --routines --events --databases "$@" >"$D/schema.sql"
$DUMP $AT --single-transaction --no-create-info --no-create-db --skip-triggers $IGNORE --databases "$@" >"$D/data.sql"
$DUMP $AT --single-transaction --no-data --no-create-info --no-create-db --triggers --databases "$@" >"$D/triggers.sql"
echo "` + statsMarker + ` bytes=$size tables=$tables columns=$columns"
`

// mysqlPlanSQL lists the base's tables with masked columns (@marks, the
// JSON of DOCK_MARKS), one line each: the number masked, base64 of its
// database and table name (for mysqldump's --ignore-table), and base64 of
// a SELECT that turns its rows into INSERT statements: binary values as
// hex, others quoted, masked ones NULL, or where they can't be NULL a
// value of their type that says nothing. A type with none stays NULL, so
// the load fails rather than copy it. Generated columns are left to be
// computed again. mysqldump can't leave out a table with a dot in its
// name, so mysqlDump refuses one.
const mysqlPlanSQL = `SELECT COUNT(m.c),
  REPLACE(TO_BASE64(c.table_schema), '\n', ''), REPLACE(TO_BASE64(c.table_name), '\n', ''),
  REPLACE(TO_BASE64(CONCAT(
    'SELECT CONCAT(''INSERT INTO '', ', QUOTE(CONCAT('` + "`" + `', REPLACE(c.table_schema, '` + "`" + `', '` + "``" + `'), '` + "`.`" + `', REPLACE(c.table_name, '` + "`" + `', '` + "``" + `'), '` + "`" + `')), ', '' ('', ',
    QUOTE(GROUP_CONCAT(CONCAT('` + "`" + `', REPLACE(c.column_name, '` + "`" + `', '` + "``" + `'), '` + "`" + `') ORDER BY c.ordinal_position SEPARATOR ',')),
    ', '') VALUES ('', ',
    GROUP_CONCAT(CASE
      WHEN m.c IS NULL AND c.data_type IN ('binary', 'varbinary', 'tinyblob', 'blob', 'mediumblob', 'longblob', 'bit', 'geometry', 'point', 'linestring', 'polygon', 'multipoint', 'multilinestring', 'multipolygon', 'geometrycollection', 'geomcollection')
        THEN CONCAT('IF(', CONCAT('` + "`" + `', REPLACE(c.column_name, '` + "`" + `', '` + "``" + `'), '` + "`" + `'), ' IS NULL, ''NULL'', CONCAT(''X'''''', HEX(', CONCAT('` + "`" + `', REPLACE(c.column_name, '` + "`" + `', '` + "``" + `'), '` + "`" + `'), '), ''''''''))')
      WHEN m.c IS NULL THEN CONCAT('QUOTE(', CONCAT('` + "`" + `', REPLACE(c.column_name, '` + "`" + `', '` + "``" + `'), '` + "`" + `'), ')')
      WHEN c.is_nullable = 'YES' THEN '''NULL'''
      WHEN c.data_type IN ('char', 'varchar') AND c.character_maximum_length < 16
        THEN CONCAT('QUOTE(LEFT(MD5(RAND()), ', c.character_maximum_length, '))')
      WHEN c.data_type IN ('char', 'varchar', 'tinytext', 'text', 'mediumtext', 'longtext')
        THEN CONCAT('QUOTE(LEFT(CONCAT(''masked-'', MD5(RAND())), ', LEAST(COALESCE(c.character_maximum_length, 32), 32), '))')
      WHEN c.data_type IN ('tinyint', 'smallint', 'mediumint', 'int', 'bigint', 'decimal', 'float', 'double') THEN '''0'''
      WHEN c.data_type = 'date' THEN '''''''1970-01-01'''''''
      WHEN c.data_type IN ('datetime', 'timestamp') THEN '''''''1970-01-01 00:00:01'''''''
      WHEN c.data_type = 'time' THEN '''''''00:00:00'''''''
      WHEN c.data_type = 'year' THEN '''1970'''
      WHEN c.data_type = 'json' THEN '''''''{}'''''''
      WHEN c.data_type IN ('binary', 'varbinary', 'tinyblob', 'blob', 'mediumblob', 'longblob') THEN '''X'''''''''''
      ELSE '''NULL'''
    END ORDER BY c.ordinal_position SEPARATOR ', '','', '),
    ', '');'') FROM ', CONCAT('` + "`" + `', REPLACE(c.table_schema, '` + "`" + `', '` + "``" + `'), '` + "`.`" + `', REPLACE(c.table_name, '` + "`" + `', '` + "``" + `'), '` + "`" + `')
  )), '\n', '')
FROM information_schema.columns c
JOIN information_schema.tables tb ON tb.table_schema = c.table_schema AND tb.table_name = c.table_name AND tb.table_type = 'BASE TABLE'
LEFT JOIN JSON_TABLE(@marks, '$[*]' COLUMNS (s VARCHAR(64) PATH '$.s', t VARCHAR(64) PATH '$.t', c VARCHAR(64) PATH '$.c')) m
  ON m.s = c.table_schema AND m.t = c.table_name AND m.c = c.column_name
WHERE c.extra NOT LIKE '%GENERATED%'
  AND (c.table_schema, c.table_name) IN (SELECT s, t FROM JSON_TABLE(@marks, '$[*]' COLUMNS (s VARCHAR(64) PATH '$.s', t VARCHAR(64) PATH '$.t')) mk)
GROUP BY c.table_schema, c.table_name
HAVING COUNT(m.c) > 0
ORDER BY c.table_schema, c.table_name`

// mysqlLoad loads the dump into the preview's new server as root, with
// the binary log off (the copy is no change to replicate), foreign key
// checks off while tables fill, and the dump's SQL mode.
const mysqlLoad = `i=0
until $CLIENT $AT -e 'SELECT 1' >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 120 ] || { echo "dock: the preview's database didn't accept connections in 2 minutes" >&2; exit 1; }
  sleep 1
done
{
  echo "SET SESSION sql_log_bin = 0;"
  cat "$D/schema.sql"
  echo "SET SESSION sql_mode = 'NO_AUTO_VALUE_ON_ZERO'; SET FOREIGN_KEY_CHECKS = 0; SET UNIQUE_CHECKS = 0; SET autocommit = 0;"
  cat "$D/masked.sql"
  echo "COMMIT; SET autocommit = 1;"
  cat "$D/data.sql" "$D/triggers.sql"
} | $CLIENT $AT --default-character-set=utf8mb4
`

// redisDump fetches a consistent snapshot over the replication protocol,
// as backups do.
const redisDump = `size=$(redis-cli -h "$DOCK_DB_HOST" -p "$DOCK_DB_PORT" INFO memory | tr -d '\r' | sed -n 's/^used_memory://p')
room
redis-cli -h "$DOCK_DB_HOST" -p "$DOCK_DB_PORT" --rdb "$D/dump.rdb" >/dev/null
echo "` + statsMarker + ` bytes=$size tables=0 columns=0"
`

// redisLoad puts the snapshot in the preview's volume (DOCK_DATA, empty
// or emptied) as its append-only file's base, before Redis first starts:
// with appendonly on, Redis ignores a plain dump.rdb.
const redisLoad = `find "$DOCK_DATA" -mindepth 1 -delete
mkdir -p "$DOCK_DATA/appendonlydir"
cp "$D/dump.rdb" "$DOCK_DATA/appendonlydir/appendonly.aof.1.base.rdb"
printf 'file appendonly.aof.1.base.rdb seq 1 type b\n' >"$DOCK_DATA/appendonlydir/appendonly.aof.manifest"
`
