import { tableName } from "../../../src/features/data/cells";
import type {
  DataCell,
  DataChange,
  DataColumn,
  DataFilter,
  DataInsights,
  DataQueryFrame,
  DataRow,
  DataTable,
} from "../../../src/features/data/types";

// A small fake Postgres database for the in-memory backend: a shop with
// customers, products and orders, generated the same way every time, so
// the shared data panel can be developed without a server. Only what the
// panel needs: filters, sorting, keyset-like paging, a SELECT parser good
// enough to try the SQL tab, and applying changesets.

export type FakeTable = { meta: DataTable; rows: DataRow[] };

const column = (
  name: string,
  type: string,
  extra: Partial<DataColumn> = {},
): DataColumn => ({
  name,
  type,
  type_name: type.split("(")[0] ?? type,
  nullable: false,
  generated: false,
  sensitive: false,
  ...extra,
});

const table = (
  name: string,
  columns: DataColumn[],
  extra: Partial<DataTable> = {},
): DataTable => ({
  schema: "public",
  name,
  kind: "table",
  columns,
  primary_key: ["id"],
  foreign_keys: [],
  indexes: [
    {
      name: `${name}_pkey`,
      primary: true,
      unique: true,
      definition: `CREATE UNIQUE INDEX ${name}_pkey ON public.${name} USING btree (id)`,
    },
  ],
  constraints: [],
  editable: true,
  estimated_rows: 0,
  size_bytes: 0,
  ...extra,
});

/** A deterministic pseudo-random sequence, so every load looks the same. */
function random(seed: number) {
  let s = seed;
  return () => {
    s = (s * 1103515245 + 12345) % 2147483648;
    return s / 2147483648;
  };
}

const firstNames = ["Ada", "Linus", "Grace", "Ken", "Barbara", "Edsger"];
const lastNames = ["Lovelace", "Torvalds", "Hopper", "Thompson", "Liskov"];
const countries = ["NO", "SE", "DK", "DE", "NL", "FI"];
const statuses = ["paid", "pending", "shipped", "refunded"];
const products = ["Kettle", "Mug", "Grinder", "Filter", "Scale", "Tin"];

const day = (n: number) =>
  new Date(Date.UTC(2026, 0, 1) + n * 3_600_000 * 7).toISOString();

/** The shop's tables with their rows. */
export function makeTables(): FakeTable[] {
  const r = random(42);
  const pick = <T>(xs: readonly T[]) => xs[Math.floor(r() * xs.length)] as T;

  const customers: DataRow[] = Array.from({ length: 240 }, (_, i) => {
    const first = pick(firstNames);
    const last = pick(lastNames);
    return [
      i + 1,
      `${first.toLowerCase()}.${last.toLowerCase()}${i}@example.com`,
      `${first} ${last}`,
      pick(countries),
      day(i),
    ];
  });
  const productRows: DataRow[] = products.map((name, i) => [
    i + 1,
    name,
    (9 + i * 7.5).toFixed(2),
  ]);
  const orders: DataRow[] = Array.from({ length: 1000 }, (_, i) => [
    String(i + 1),
    1 + Math.floor(r() * customers.length),
    1 + Math.floor(r() * products.length),
    (r() * 300 + 5).toFixed(2),
    pick(statuses),
    i % 17 === 0 ? null : day(i / 4),
  ]);

  return [
    {
      meta: table(
        "customers",
        [
          column("id", "integer", { default: "nextval('customers_id_seq')" }),
          column("email", "text"),
          column("name", "text"),
          column("country", "text", { nullable: true }),
          column("created_at", "timestamp with time zone", {
            type_name: "timestamptz",
            default: "now()",
          }),
        ],
        {
          estimated_rows: customers.length,
          size_bytes: 49_152,
          comment: "People who ordered something.",
          constraints: [
            {
              name: "customers_email_key",
              kind: "unique",
              definition: "UNIQUE (email)",
            },
          ],
        },
      ),
      rows: customers,
    },
    {
      meta: table(
        "products",
        [
          column("id", "integer"),
          column("name", "text"),
          column("price", "numeric(10,2)"),
        ],
        { estimated_rows: productRows.length, size_bytes: 8192 },
      ),
      rows: productRows,
    },
    {
      meta: table(
        "orders",
        [
          column("id", "bigint", { type_name: "int8" }),
          column("customer_id", "integer"),
          column("product_id", "integer"),
          column("total", "numeric(10,2)"),
          column("status", "text"),
          column("shipped_at", "timestamp with time zone", {
            type_name: "timestamptz",
            nullable: true,
          }),
        ],
        {
          estimated_rows: orders.length,
          size_bytes: 106_496,
          foreign_keys: [
            {
              name: "orders_customer_id_fkey",
              columns: ["customer_id"],
              ref_schema: "public",
              ref_table: "customers",
              ref_columns: ["id"],
            },
            {
              name: "orders_product_id_fkey",
              columns: ["product_id"],
              ref_schema: "public",
              ref_table: "products",
              ref_columns: ["id"],
            },
          ],
          constraints: [
            {
              name: "orders_total_check",
              kind: "check",
              definition: "CHECK (total >= 0)",
            },
          ],
        },
      ),
      rows: orders,
    },
    {
      meta: table(
        "paid_orders",
        [column("id", "bigint"), column("total", "numeric")],
        {
          kind: "view",
          primary_key: null,
          indexes: [],
          editable: false,
          estimated_rows: -1,
          exact_rows: orders.filter((o) => o[4] === "paid").length,
        },
      ),
      rows: orders
        .filter((o) => o[4] === "paid")
        .map((o) => [o[0] ?? null, o[3] ?? null]),
    },
  ];
}

export const findTable = (tables: readonly FakeTable[], name: string) =>
  tables.find((t) => tableName(t.meta) === name || t.meta.name === name);

/** A cell as text, the way filters and changesets compare values. */
export const cellString = (c: DataCell | undefined): string | null =>
  c === null || c === undefined
    ? null
    : typeof c === "object"
      ? null
      : String(c);

function matches(f: DataFilter, value: string | null) {
  const v = f.value ?? "";
  const num = (s: string) => Number(s);
  const cmp =
    value === null
      ? 0
      : !Number.isNaN(num(value)) && !Number.isNaN(num(v))
        ? num(value) - num(v)
        : value.localeCompare(v);
  const like = (s: string, flags: string) =>
    new RegExp(
      `^${s
        .replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
        .replaceAll("%", ".*")
        .replaceAll("_", ".")}$`,
      flags,
    );
  switch (f.op) {
    case "is_null":
      return value === null;
    case "not_null":
      return value !== null;
    case "in":
      return value !== null && (f.values ?? []).includes(value);
    default:
      if (value === null) return false;
  }
  switch (f.op) {
    case "eq":
      return value === v;
    case "ne":
      return value !== v;
    case "lt":
      return cmp < 0;
    case "lte":
      return cmp <= 0;
    case "gt":
      return cmp > 0;
    case "gte":
      return cmp >= 0;
    case "like":
      return like(v, "").test(value);
    case "ilike":
      return like(v, "i").test(value);
  }
}

/** The rows matching every filter, sorted by `order` ("col", "-col"). */
export function select(
  t: FakeTable,
  filters: readonly DataFilter[],
  order?: string,
) {
  const names = (t.meta.columns ?? []).map((c) => c.name);
  const at = (name: string) => names.indexOf(name);
  for (const f of filters) {
    if (at(f.column) < 0)
      throw new Error(`column "${f.column}" does not exist`);
  }
  const rows = t.rows.filter((row) =>
    filters.every((f) => matches(f, cellString(row[at(f.column)]))),
  );
  if (!order) return rows;
  const desc = order.startsWith("-");
  const i = at(desc ? order.slice(1) : order);
  if (i < 0) throw new Error(`column "${order}" does not exist`);
  return rows.toSorted((a, b) => {
    const x = cellString(a[i]);
    const y = cellString(b[i]);
    const n = Number(x) - Number(y);
    const c =
      x === null
        ? 1
        : y === null
          ? -1
          : Number.isNaN(n)
            ? x.localeCompare(y)
            : n;
    return desc ? -c : c;
  });
}

/** Rows with the given columns masked. */
export function mask(t: FakeTable, rows: DataRow[], hidden: Set<string>) {
  if (!hidden.size) return rows;
  const idx = (t.meta.columns ?? []).flatMap((c, i) =>
    hidden.has(c.name) ? [i] : [],
  );
  return rows.map((r) => {
    const copy = r.slice();
    for (const i of idx) copy[i] = { masked: true };
    return copy;
  });
}

const quote = (s: string) => `"${s.replaceAll('"', '""')}"`;
const literal = (s: string | null) =>
  s === null ? "NULL" : `'${s.replaceAll("'", "''")}'`;

/** The SQL a changeset runs, as the server would render it. */
export function changeSql(changes: readonly DataChange[]) {
  const lines = changes.map((c) => {
    const [schema, name] = c.table.includes(".")
      ? c.table.split(".")
      : ["public", c.table];
    const target = `${quote(schema ?? "public")}.${quote(name ?? "")}`;
    const where = Object.entries({ ...c.key, ...(c.old ?? {}) })
      .map(([k, v]) =>
        v === null ? `${quote(k)} IS NULL` : `${quote(k)} = ${literal(v)}`,
      )
      .join(" AND ");
    switch (c.op) {
      case "update":
        return `UPDATE ${target} SET ${Object.entries(c.new ?? {})
          .map(([k, v]) => `${quote(k)} = ${literal(v)}`)
          .join(", ")} WHERE ${where};`;
      case "delete":
        return `DELETE FROM ${target} WHERE ${where};`;
      default: {
        const entries = Object.entries(c.new ?? {});
        return `INSERT INTO ${target} (${entries.map(([k]) => quote(k)).join(", ")}) VALUES (${entries.map(([, v]) => literal(v)).join(", ")});`;
      }
    }
  });
  return ["BEGIN;", ...lines, "COMMIT;"].join("\n");
}

/** Applies changes; each matches its key and old values, else a conflict. */
export function applyChanges(
  tables: readonly FakeTable[],
  changes: readonly DataChange[],
): { affected: number[]; conflict?: { change: number; message: string } } {
  const affected: number[] = [];
  const plan: (() => void)[] = [];
  for (const [i, c] of changes.entries()) {
    const t = findTable(tables, c.table);
    if (!t)
      return {
        affected,
        conflict: { change: i, message: `relation ${c.table} does not exist` },
      };
    const names = (t.meta.columns ?? []).map((col) => col.name);
    const cell = (v: string | null, col: string): DataCell => {
      const type = t.meta.columns?.find((x) => x.name === col)?.type_name;
      return v !== null && (type === "integer" || type === "int4")
        ? Number(v)
        : v;
    };
    if (c.op === "insert") {
      plan.push(() =>
        t.rows.push(names.map((n) => cell(c.new?.[n] ?? null, n))),
      );
      affected.push(1);
      continue;
    }
    const want = { ...c.key, ...(c.old ?? {}) };
    const row = t.rows.find((r) =>
      Object.entries(want).every(
        ([k, v]) => cellString(r[names.indexOf(k)]) === v,
      ),
    );
    if (!row) {
      return {
        affected,
        conflict: {
          change: i,
          message: `row ${Object.values(c.key ?? {}).join(", ")} of ${c.table} changed or is gone`,
        },
      };
    }
    plan.push(
      c.op === "delete"
        ? () => t.rows.splice(t.rows.indexOf(row), 1)
        : () => {
            for (const [k, v] of Object.entries(c.new ?? {})) {
              row[names.indexOf(k)] = cell(v, k);
            }
          },
    );
    affected.push(1);
  }
  for (const step of plan) step();
  return { affected };
}

/** Writes Postgres refuses in a read-only transaction. */
const writeRe =
  /^\s*(insert|update|delete|create|drop|alter|truncate|grant)\b/i;

/** What the fake can't run; it says so instead of ignoring it. */
const unsupportedRe =
  /\b(join|group\s+by|order\s+by|having|union|intersect|except|distinct|with)\b|\w+\s*\(/i;

/**
 * Runs read-only SQL the fake way: `SELECT * | col, ... FROM table [WHERE
 * col = 'v'] [LIMIT n]` returns the table's rows, `SELECT 1` style
 * expressions one row, writes and anything else (joins, aggregates,
 * ordering) an error frame that says what the in-memory database runs.
 */
export function* runQuery(
  tables: readonly FakeTable[],
  sql: string,
  hidden: (t: FakeTable) => Set<string>,
  maxRows: number,
): Generator<DataQueryFrame> {
  const started = performance.now();
  const end = (rows: number, truncated = false): DataQueryFrame => ({
    type: "end",
    command: "SELECT",
    row_count: rows,
    truncated,
    duration_ms: Math.round(performance.now() - started) + 3,
  });
  const text = sql.trim().replace(/;\s*$/, "");
  if (text.includes(";")) {
    yield {
      type: "error",
      error: {
        message: "cannot insert multiple commands into a prepared statement",
        sqlstate: "42601",
      },
    };
    return;
  }
  const write = writeRe.exec(text);
  if (write) {
    yield {
      type: "error",
      error: {
        message: `cannot execute ${write[1]?.toUpperCase()} in a read-only transaction`,
        sqlstate: "25006",
        hint: "Edit rows in the Tables tab: every write is a reviewed changeset.",
      },
    };
    return;
  }
  const from = /\bfrom\s+([\w."]+)/i.exec(text);
  if (!from) {
    const expr = /^select\s+(.+)$/i.exec(text)?.[1];
    if (!expr) {
      yield {
        type: "error",
        error: {
          message: `syntax error at or near "${text.split(/\s/)[0]}"`,
          sqlstate: "42601",
          position: 1,
        },
      };
      return;
    }
    yield {
      type: "header",
      columns: [{ name: "?column?", type_name: "text" }],
    };
    yield { type: "rows", rows: [[expr.replace(/^'(.*)'$/, "$1")]] };
    yield end(1);
    return;
  }
  const unsupported = {
    type: "error",
    error: {
      message:
        "the in-memory database runs only select * or plain columns from one table, with where column = value and limit",
      sqlstate: "0A000",
    },
  } as const satisfies DataQueryFrame;
  if (unsupportedRe.test(text)) {
    yield unsupported;
    return;
  }
  const name = (from[1] ?? "").replaceAll('"', "");
  const t = findTable(tables, name);
  if (!t) {
    yield {
      type: "error",
      error: {
        message: `relation "${name}" does not exist`,
        sqlstate: "42P01",
        position: from.index + from[0].length - name.length + 1,
      },
    };
    return;
  }
  const where = /\bwhere\s+(\w+)\s*=\s*'?([^'\s]+)'?/i.exec(text);
  const limit = Number(
    /\blimit\s+(\d+)/i.exec(text)?.[1] ?? Number.POSITIVE_INFINITY,
  );
  const all = select(
    t,
    where ? [{ column: where[1] ?? "", op: "eq", value: where[2] }] : [],
  );
  // The select list: * or columns of the table, by index.
  const columns = t.meta.columns ?? [];
  const list = /^select\s+(.+?)\s+from\b/is.exec(text)?.[1]?.trim() ?? "*";
  const picked =
    list === "*"
      ? columns.map((_, i) => i)
      : list
          .split(",")
          .map((n) =>
            columns.findIndex((c) => c.name === n.trim().replaceAll('"', "")),
          );
  if (picked.includes(-1)) {
    yield unsupported;
    return;
  }
  const rows = mask(t, all.slice(0, Math.min(limit, maxRows)), hidden(t)).map(
    (r) => picked.map((i) => r[i] ?? null),
  );
  yield {
    type: "header",
    columns: picked.map((i) => ({
      name: columns[i]?.name ?? "",
      type_name: columns[i]?.type_name ?? "text",
    })),
  };
  for (let i = 0; i < rows.length; i += 500) {
    yield { type: "rows", rows: rows.slice(i, i + 500) };
  }
  yield end(rows.length, all.length > maxRows && limit > maxRows);
}

/**
 * Insights for the in-memory backend: the tables' sizes and indexes, a
 * worker idle in a transaction that blocks an api session, a few
 * statements and the hints the server would give for them.
 */
export function fakeInsights(
  tables: readonly FakeTable[],
  canRead: boolean,
): DataInsights {
  const now = Date.now();
  const at = (s: number) => new Date(now - s * 1000).toISOString();
  const since = at(3 * 86_400 + 4 * 3600);
  const views = canRead
    ? {
        queries: [
          {
            id: "-4785384450269883726",
            text: "SELECT * FROM orders WHERE customer_id = $1 AND status = $2 ORDER BY created_at DESC LIMIT $3",
            user: "postgres",
            calls: 1204,
            total_ms: 2_468_000,
            mean_ms: 2050,
            max_ms: 9800,
            rows: 60_200,
            cache_hit: 0.971,
            temp_bytes: 0,
          },
          {
            id: "7234589265751732083",
            text: "UPDATE customers SET seen_at = $1 WHERE id = $2",
            user: "postgres",
            calls: 2_400_000,
            total_ms: 760_000,
            mean_ms: 0.32,
            max_ms: 880,
            rows: 2_400_000,
            cache_hit: 0.999,
            temp_bytes: 0,
          },
          {
            id: "367282865999414690",
            text: "SELECT o.*, c.email FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.status = $1",
            user: "postgres",
            calls: 8812,
            total_ms: 362_000,
            mean_ms: 41,
            max_ms: 1200,
            rows: 1_100_000,
            cache_hit: 0.996,
            temp_bytes: 38 << 20,
          },
        ],
        queries_tracked: 1812,
        sessions: [
          {
            id: "4121.1",
            pid: 4121,
            user: "postgres",
            application: "worker",
            client: "10.8.0.4",
            kind: "client" as const,
            state: "idle_in_transaction" as const,
            transaction_at: at(372),
            query_at: at(372),
            state_at: at(372),
            text: "UPDATE orders SET status = $1 WHERE id = $2",
          },
          {
            id: "4188.1",
            pid: 4188,
            user: "postgres",
            application: "api",
            client: "10.8.0.3",
            kind: "client" as const,
            state: "active" as const,
            transaction_at: at(123),
            query_at: at(123),
            state_at: at(123),
            wait: "Lock: transactionid",
            blocked_by: ["4121.1"],
            text: "UPDATE orders SET status = 'paid' WHERE id = 1041",
          },
          {
            id: "4302.1",
            pid: 4302,
            user: "postgres",
            application: "api",
            client: "10.8.0.3",
            kind: "client" as const,
            state: "idle" as const,
            state_at: at(4),
            query_at: at(4),
            text: "SELECT 1",
          },
        ],
      }
    : {};
  return {
    engine: "postgres",
    read_at: new Date(now).toISOString(),
    since,
    since_reason: "start",
    overview: {
      connections: {
        used: 41,
        max: 50,
        reserved: 3,
        active: 3,
        idle: 37,
        idle_in_transaction: 1,
        waiting: 1,
        oldest_transaction_at: at(372),
        full: false,
      },
      cache_hit: 0.994,
      bytes:
        tables.reduce((n, t) => n + (t.meta.size_bytes ?? 0), 0) + (8 << 20),
      deadlocks: 0,
      temp_bytes: 41 << 20,
      transaction_id_age: 212_000_000,
      query_stats: "on",
      started_at: since,
    },
    ...views,
    tables: tables.map((t) => ({
      schema: t.meta.schema,
      name: t.meta.name,
      total_bytes: (t.meta.size_bytes ?? 0) + 16_384,
      index_bytes: 16_384,
      estimated_bytes: t.meta.size_bytes ?? 0,
      estimated_rows: t.rows.length,
      dead_rows: t.meta.name === "orders" ? Math.round(t.rows.length * 0.4) : 0,
      seq_scans: t.meta.name === "orders" ? 1204 : 12,
      seq_rows_read: t.meta.name === "orders" ? 1204 * t.rows.length : 120,
      index_scans: 30,
      vacuumed_at: at(t.meta.name === "orders" ? 3 * 86_400 : 3600),
      analyzed_at: at(3600),
      cache_hit: 0.99,
    })),
    indexes: tables.flatMap((t) =>
      (t.meta.indexes ?? []).map((x) => ({
        schema: t.meta.schema,
        table: t.meta.name,
        name: x.name,
        bytes: 16_384,
        scans: x.primary ? 410 : 0,
        unique: x.unique,
        primary: x.primary,
        constraint: x.primary,
        valid: true,
        duplicate: false,
        definition: x.definition,
      })),
    ),
    hints: [
      ...(canRead
        ? [
            {
              kind: "idle_in_transaction" as const,
              level: "warn" as const,
              view: "sessions" as const,
              subject: "4121.1",
              title: "Idle in transaction 6m 12s",
              detail:
                "worker (postgres), pid 4121; holds its locks and holds back vacuum; blocks 1 session",
            },
          ]
        : []),
      {
        kind: "dead_rows",
        level: "warn",
        view: "tables",
        subject: "public.orders",
        title: "Dead rows",
        detail: "40% of the rows are dead; last vacuumed 3d 0h ago",
        copy: 'VACUUM (ANALYZE) "public"."orders";',
      },
      {
        kind: "full_scans",
        level: "note",
        view: "tables",
        subject: "public.orders",
        title: "Likely missing index",
        detail: "1,204 full scans, 30 index scans",
      },
    ],
    unavailable: canRead
      ? []
      : [
          {
            view: "queries",
            reason: "needs data access level read; yours is schema",
          },
          {
            view: "sessions",
            reason: "needs data access level read; yours is schema",
          },
        ],
  };
}
