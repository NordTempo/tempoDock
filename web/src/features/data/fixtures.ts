import type { DataSource } from "./api";
import type {
  DataCell,
  DataChange,
  DataColumn,
  DataFilter,
  DataHistoryEntry,
  DataInfo,
  DataPage,
  DataPermission,
  DataQueryFrame,
  DataReferences,
  DataRow,
  DataSavedQuery,
  DataSchema,
  DataTable,
} from "./types";

// Fixtures for the database panel: a small shop database and an in-memory
// DataSource that pages, filters, sorts and streams it like the daemon
// would. Imported only by the dev demo (./demo) and tests, never by
// production code paths.

// --- Schema ---------------------------------------------------------------

const col = (
  name: string,
  type: string,
  typeName: string,
  extra: Partial<DataColumn> = {},
): DataColumn => ({
  name,
  type,
  type_name: typeName,
  nullable: false,
  generated: false,
  sensitive: false,
  ...extra,
});

const pkIndex = (table: string, columns: string) => ({
  name: `${table}_pkey`,
  unique: true,
  primary: true,
  definition: `CREATE UNIQUE INDEX ${table}_pkey ON public.${table} USING btree (${columns})`,
});

const tables: DataTable[] = [
  {
    schema: "public",
    name: "customers",
    kind: "table",
    estimated_rows: 1_842,
    size_bytes: 1_212_416,
    columns: [
      col("id", "bigint", "int8", {
        default: "nextval('customers_id_seq'::regclass)",
      }),
      col("email", "text", "text"),
      col("name", "text", "text"),
      col("plan", "text", "text", { default: "'free'::text" }),
      col("password_hash", "text", "text", { sensitive: true }),
      col("created_at", "timestamp with time zone", "timestamptz", {
        default: "now()",
      }),
    ],
    primary_key: ["id"],
    foreign_keys: [],
    indexes: [
      pkIndex("customers", "id"),
      {
        name: "customers_email_key",
        unique: true,
        primary: false,
        definition:
          "CREATE UNIQUE INDEX customers_email_key ON public.customers USING btree (lower(email))",
      },
    ],
    constraints: [
      {
        name: "customers_email_key",
        kind: "unique",
        definition: "UNIQUE (email)",
      },
      {
        name: "customers_plan_check",
        kind: "check",
        definition:
          "CHECK (plan = ANY (ARRAY['free'::text, 'pro'::text, 'team'::text]))",
      },
    ],
    editable: true,
  },
  {
    schema: "public",
    name: "orders",
    kind: "table",
    estimated_rows: 12_431,
    size_bytes: 4_734_976,
    comment: "One row per checkout.",
    columns: [
      col("id", "bigint", "int8", {
        default: "nextval('orders_id_seq'::regclass)",
      }),
      col("customer_id", "bigint", "int8"),
      col("total", "numeric(10,2)", "numeric"),
      col("status", "text", "text", { default: "'pending'::text" }),
      col("notes", "text", "text", { nullable: true }),
      col("metadata", "jsonb", "jsonb", { default: "'{}'::jsonb" }),
      col("total_cents", "bigint", "int8", {
        generated: true,
        comment: "GENERATED ALWAYS AS ((total * 100)::bigint) STORED",
      }),
      col("created_at", "timestamp with time zone", "timestamptz", {
        default: "now()",
      }),
    ],
    primary_key: ["id"],
    foreign_keys: [
      {
        name: "orders_customer_id_fkey",
        columns: ["customer_id"],
        ref_schema: "public",
        ref_table: "customers",
        ref_columns: ["id"],
      },
    ],
    indexes: [
      pkIndex("orders", "id"),
      {
        name: "orders_customer_id_idx",
        unique: false,
        primary: false,
        definition:
          "CREATE INDEX orders_customer_id_idx ON public.orders USING btree (customer_id)",
      },
      {
        name: "orders_status_created_at_idx",
        unique: false,
        primary: false,
        definition:
          "CREATE INDEX orders_status_created_at_idx ON public.orders USING btree (status, created_at DESC)",
      },
    ],
    constraints: [
      {
        name: "orders_total_check",
        kind: "check",
        definition: "CHECK (total >= (0)::numeric)",
      },
    ],
    editable: true,
  },
  {
    schema: "public",
    name: "order_items",
    kind: "table",
    estimated_rows: 30_118,
    size_bytes: 2_891_776,
    columns: [
      col("id", "bigint", "int8"),
      col("order_id", "bigint", "int8"),
      col("product_id", "integer", "int4"),
      col("quantity", "integer", "int4", { default: "1" }),
      col("unit_price", "numeric(10,2)", "numeric"),
    ],
    primary_key: ["id"],
    foreign_keys: [
      {
        name: "order_items_order_id_fkey",
        columns: ["order_id"],
        ref_schema: "public",
        ref_table: "orders",
        ref_columns: ["id"],
      },
      {
        name: "order_items_product_id_fkey",
        columns: ["product_id"],
        ref_schema: "public",
        ref_table: "products",
        ref_columns: ["id"],
      },
    ],
    indexes: [pkIndex("order_items", "id")],
    constraints: [
      {
        name: "order_items_quantity_check",
        kind: "check",
        definition: "CHECK (quantity > 0)",
      },
    ],
    editable: true,
  },
  {
    schema: "public",
    name: "products",
    kind: "table",
    estimated_rows: 24,
    size_bytes: 98_304,
    columns: [
      col("id", "integer", "int4"),
      col("sku", "text", "text"),
      col("name", "text", "text"),
      col("price", "numeric(10,2)", "numeric"),
      col("active", "boolean", "bool", { default: "true" }),
      col("tags", "text[]", "_text", { nullable: true }),
      col("description", "text", "text", { nullable: true }),
      col("thumbnail", "bytea", "bytea", { nullable: true }),
    ],
    primary_key: ["id"],
    foreign_keys: [],
    indexes: [pkIndex("products", "id")],
    constraints: [],
    editable: true,
  },
  {
    schema: "public",
    name: "order_totals",
    kind: "view",
    estimated_rows: -1,
    size_bytes: 0,
    columns: [
      col("customer_id", "bigint", "int8", { nullable: true }),
      col("orders", "bigint", "int8", { nullable: true }),
      col("revenue", "numeric", "numeric", { nullable: true }),
    ],
    primary_key: [],
    foreign_keys: [],
    indexes: [],
    constraints: [],
    editable: false,
  },
  {
    schema: "audit",
    name: "events",
    kind: "table",
    estimated_rows: -1,
    size_bytes: 16_384,
    columns: [
      col("at", "timestamp with time zone", "timestamptz"),
      col("actor", "text", "text"),
      col("action", "text", "text"),
      col("payload", "jsonb", "jsonb", { nullable: true }),
    ],
    primary_key: [],
    foreign_keys: [],
    indexes: [],
    constraints: [],
    editable: false,
  },
];

export const fixtureSchema: DataSchema = {
  tables,
  read_at: "2026-10-21T09:14:03Z",
};

// --- Rows -----------------------------------------------------------------

/** Deterministic pseudo-random numbers (mulberry32), so screenshots repeat. */
function random(seed: number) {
  let a = seed;
  return () => {
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const pick = <T>(rand: () => number, xs: readonly T[]): T =>
  xs[Math.floor(rand() * xs.length)] as T;

/** Postgres' text output of a timestamptz, `minutes` before a fixed now. */
function ts(minutes: number) {
  const d = new Date(Date.UTC(2026, 9, 20, 14, 2) - minutes * 60_000);
  return `${d.toISOString().slice(0, 19).replace("T", " ")}+00`;
}

const firstNames = [
  "Ada",
  "Linus",
  "Grace",
  "Ken",
  "Barbara",
  "Edsger",
  "Frances",
  "Dennis",
  "Radia",
  "Niklaus",
];
const lastNames = [
  "Lovelace",
  "Torvalds",
  "Hopper",
  "Thompson",
  "Liskov",
  "Dijkstra",
  "Allen",
  "Ritchie",
  "Perlman",
  "Wirth",
];

function seed(): Record<string, DataRow[]> {
  const rand = random(16);
  const customers: DataRow[] = Array.from({ length: 60 }, (_, i) => {
    const first = pick(rand, firstNames);
    const last = pick(rand, lastNames);
    return [
      String(i + 1),
      `${first}.${last}${i}@example.com`.toLowerCase(),
      `${first} ${last}`,
      pick(rand, ["free", "free", "pro", "team"]),
      { masked: true },
      ts(60 * 24 * 90 - i * 1300),
    ];
  });
  const products: DataRow[] = Array.from({ length: 24 }, (_, i) => {
    const long = i % 5 === 0;
    const description = long
      ? {
          truncated:
            `Hand-made in Bergen. ${"Every batch is tested for strength and finish. ".repeat(160)}`.slice(
              0,
              8192,
            ),
          bytes: 24_576,
        }
      : i % 3 === 0
        ? null
        : `A sturdy ${pick(rand, ["oak", "ash", "steel", "linen"])} thing.`;
    return [
      i + 1,
      `SKU-${String(4100 + i)}`,
      `${pick(rand, ["Desk", "Lamp", "Shelf", "Chair", "Mug", "Rug"])} ${String.fromCharCode(65 + (i % 26))}`,
      (9.5 + Math.round(rand() * 40000) / 100).toFixed(2),
      i % 7 !== 3,
      i % 4 === 0
        ? null
        : `{${pick(rand, ["home", "office"])},${pick(rand, ["new", "sale", "classic"])}}`,
      description,
      i % 6 === 0
        ? null
        : `\\x89504e470d0a1a0a0000000d49484452${(i * 7919).toString(16).padStart(8, "0")}`,
    ];
  });
  const orders: DataRow[] = Array.from({ length: 260 }, (_, i) => {
    const total = (5 + Math.round(rand() * 50000) / 100).toFixed(2);
    const status = pick(rand, [
      "paid",
      "paid",
      "paid",
      "pending",
      "refunded",
      "shipped",
    ]);
    return [
      String(1001 + i),
      String(1 + Math.floor(rand() * 60)),
      total,
      status,
      i % 9 === 0 ? "Leave at the back door.\nRing twice." : null,
      JSON.stringify({
        channel: pick(rand, ["web", "ios", "pos"]),
        coupon: i % 11 === 0 ? "AUTUMN" : null,
      }),
      String(Math.round(Number(total) * 100)),
      ts((260 - i) * 37),
    ];
  });
  const items: DataRow[] = Array.from({ length: 600 }, (_, i) => [
    String(i + 1),
    String(1001 + Math.floor(i / 2.3)),
    1 + Math.floor(rand() * 24),
    1 + Math.floor(rand() * 4),
    (9.5 + Math.round(rand() * 20000) / 100).toFixed(2),
  ]);
  const totals = new Map<string, { n: number; sum: number }>();
  for (const o of orders) {
    const t = totals.get(String(o[1])) ?? { n: 0, sum: 0 };
    totals.set(String(o[1]), { n: t.n + 1, sum: t.sum + Number(o[2]) });
  }
  const events: DataRow[] = Array.from({ length: 40 }, (_, i) => [
    ts(i * 11),
    pick(rand, ["api", "worker", "cron"]),
    pick(rand, ["order.created", "order.paid", "customer.signed_up"]),
    i % 4 === 0 ? null : JSON.stringify({ id: 1001 + i }),
  ]);
  return {
    "public.customers": customers,
    "public.products": products,
    "public.orders": orders,
    "public.order_items": items,
    "public.order_totals": [...totals].map(([id, t]) => [
      id,
      String(t.n),
      t.sum.toFixed(2),
    ]),
    "audit.events": events,
  };
}

const data = seed();

// --- Filtering and sorting --------------------------------------------------

const asNumber = (cell: DataCell) =>
  typeof cell === "number"
    ? cell
    : typeof cell === "string" && cell !== ""
      ? Number(cell)
      : Number.NaN;

function compare(a: DataCell, b: DataCell) {
  if (a === b) return 0;
  if (a === null) return 1;
  if (b === null) return -1;
  const x = asNumber(a);
  const y = asNumber(b);
  if (!Number.isNaN(x) && !Number.isNaN(y)) return x - y;
  return String(a).localeCompare(String(b));
}

function likeRegex(pattern: string, flags: string) {
  const body = pattern
    .replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
    .replaceAll("%", ".*")
    .replaceAll("_", ".");
  return new RegExp(`^${body}$`, flags);
}

function matches(cell: DataCell, f: DataFilter) {
  const text = cell === null || typeof cell === "object" ? null : String(cell);
  const value = f.value ?? "";
  switch (f.op) {
    case "is_null":
      return cell === null;
    case "not_null":
      return cell !== null;
    case "eq":
      return (
        text === value ||
        (typeof cell === "boolean" && (value === "true") === cell)
      );
    case "ne":
      return text !== null && text !== value;
    case "lt":
      return text !== null && compare(cell, value) < 0;
    case "lte":
      return text !== null && compare(cell, value) <= 0;
    case "gt":
      return text !== null && compare(cell, value) > 0;
    case "gte":
      return text !== null && compare(cell, value) >= 0;
    case "like":
      return text !== null && likeRegex(value, "").test(text);
    case "ilike":
      return text !== null && likeRegex(value, "i").test(text);
    case "in":
      return text !== null && (f.values ?? []).includes(text);
  }
}

const sleep = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => {
      clearTimeout(timer);
      reject(new DOMException("Aborted", "AbortError"));
    });
  });

const findTable = (name: string) =>
  tables.find(
    (t) =>
      `${t.schema}.${t.name}` === name ||
      (t.schema === "public" && t.name === name),
  );

const columnsOf = (t: DataTable) =>
  (t.columns ?? []).map((c) => ({ name: c.name, type_name: c.type_name }));

/** Filtered and sorted rows of a table, as the rows endpoint would see them. */
function select(t: DataTable, filters: DataFilter[], order = "") {
  const names = (t.columns ?? []).map((c) => c.name);
  let rows = (data[`${t.schema}.${t.name}`] ?? []).filter((row) =>
    filters.every((f) => matches(row[names.indexOf(f.column)] ?? null, f)),
  );
  if (order) {
    const desc = order.startsWith("-");
    const i = names.indexOf(order.replace(/^-/, ""));
    rows = rows.toSorted((a, b) => {
      const c = compare(a[i] ?? null, b[i] ?? null);
      return desc ? -c : c;
    });
  }
  return rows;
}

// --- Access scenarios -------------------------------------------------------

const admin: DataPermission = {
  level: "write",
  sensitive: "visible",
  approve: true,
  manage: true,
  confirm_commit: false,
  why: [
    {
      source: "admin",
      level: "write",
      sensitive: "visible",
      detail: "admins can do everything",
    },
  ],
};

const deployRead: DataPermission = {
  level: "read",
  sensitive: "masked",
  approve: false,
  manage: false,
  confirm_commit: false,
  why: [
    {
      source: "policy",
      policy: "built_in",
      subject: "role:deploy",
      level: "read",
      sensitive: "masked",
      detail: "built-in default: role:deploy may read non-production",
    },
  ],
};

const baseInfo: DataInfo = {
  engine: "postgres",
  detected_by: "image",
  status: "ready",
  server_version: "16.4",
  set_up_at: "2026-10-19T08:30:00Z",
  set_up_by: "saile@example.com",
  used_by: ["api", "worker"],
  access: admin,
};

/** Panel states the demo can show, by name. */
export const scenarios = {
  admin: baseInfo,
  production: { ...baseInfo, access: { ...admin, confirm_commit: true } },
  deploy: { ...baseInfo, access: deployRead },
  "schema only": {
    ...baseInfo,
    access: {
      ...deployRead,
      level: "schema",
      why: [
        {
          source: "policy",
          policy: "project",
          subject: "role:deploy",
          level: "schema",
          sensitive: "masked",
          detail: "project shop: role:deploy may see the schema of production",
        },
      ],
    },
  },
  "no access": {
    ...baseInfo,
    access: {
      ...deployRead,
      level: "none",
      why: [
        {
          source: "policy",
          policy: "built_in",
          subject: "role:read",
          level: "none",
          sensitive: "masked",
          detail: "built-in default: role:read has no data access",
        },
      ],
    },
  },
  "setup needed (admin)": {
    ...baseInfo,
    status: "setup_needed",
    reason: "Dock's roles don't exist in this database yet.",
    set_up_at: undefined,
    set_up_by: undefined,
  },
  "setup needed": {
    ...baseInfo,
    status: "setup_needed",
    reason: "Dock's roles don't exist in this database yet.",
    set_up_at: undefined,
    set_up_by: undefined,
    access: deployRead,
  },
  unreachable: {
    ...baseInfo,
    status: "unreachable",
    reason:
      'Login as postgres failed: password authentication failed. POSTGRES_PASSWORD only applies when the volume is first created; set "database_url_variable" to the variable with the current password.',
  },
  "not running": {
    ...baseInfo,
    status: "not_running",
    server_version: undefined,
  },
  unsupported: {
    ...baseInfo,
    status: "unsupported",
    server_version: "12.17",
    reason: "Postgres 12.17 is older than 14.",
  },
} satisfies Record<string, DataInfo>;

export type Scenario = keyof typeof scenarios;

// --- History and saved queries ---------------------------------------------

const history: DataHistoryEntry[] = [
  {
    audit_id: 9120,
    id: 512,
    created_at: "2026-10-21T09:02:11Z",
    sql: "select status, count(*) from orders group by 1 order by 2 desc",
    rows: 4,
    duration_ms: 12,
    outcome: "ok",
  },
  {
    audit_id: 9098,
    id: 507,
    created_at: "2026-10-21T08:47:40Z",
    sql: "select * from order_itemz where order_id = 1041",
    rows: 0,
    duration_ms: 3,
    outcome: "error",
  },
  {
    audit_id: 9051,
    id: 498,
    created_at: "2026-10-20T16:20:05Z",
    sql: "select c.email, sum(o.total)\nfrom customers c join orders o on o.customer_id = c.id\ngroup by 1 order by 2 desc limit 20",
    rows: 20,
    duration_ms: 48,
    outcome: "ok",
  },
  {
    audit_id: 9012,
    id: 480,
    created_at: "2026-10-20T11:05:52Z",
    sql: "select pg_sleep(30)",
    rows: 0,
    duration_ms: 10_000,
    outcome: "timeout",
  },
];

const saved: DataSavedQuery[] = [
  {
    id: 1,
    name: "Refunds this week",
    sql: "select * from orders\nwhere status = 'refunded'\n  and created_at > now() - interval '7 days'",
    created_at: "2026-10-14T10:00:00Z",
    updated_at: "2026-10-18T10:00:00Z",
  },
  {
    id: 2,
    name: "Top customers",
    sql: "select customer_id, orders, revenue from order_totals order by revenue desc limit 50",
    created_at: "2026-10-02T10:00:00Z",
    updated_at: "2026-10-02T10:00:00Z",
  },
];

// --- SQL rendering ------------------------------------------------------------

const literal = (v: string | null | undefined) =>
  v === null || v === undefined ? "NULL" : `'${v.replaceAll("'", "''")}'`;

const where = (
  key: Record<string, string> = {},
  old: Record<string, string | null> = {},
) =>
  [
    ...Object.entries(key).map(([k, v]) => `${k} = ${literal(v)}`),
    ...Object.entries(old).map(([k, v]) =>
      v === null ? `${k} IS NULL` : `${k} = ${literal(v)}`,
    ),
  ].join(" AND ");

/** The transaction a changeset runs, as the daemon renders it. */
export function renderChangesSql(changes: DataChange[]) {
  const lines = changes.map((c) => {
    switch (c.op) {
      case "update":
        return `UPDATE ${c.table} SET ${Object.entries(c.new ?? {})
          .map(([k, v]) => `${k} = ${literal(v)}`)
          .join(", ")} WHERE ${where(c.key, c.old)};`;
      case "delete":
        return `DELETE FROM ${c.table} WHERE ${where(c.key)};`;
      default: // insert
        return `INSERT INTO ${c.table} (${Object.keys(c.new ?? {}).join(", ")}) VALUES (${Object.values(
          c.new ?? {},
        )
          .map(literal)
          .join(", ")});`;
    }
  });
  return ["BEGIN;", ...lines, "COMMIT;"].join("\n");
}

// --- The source ---------------------------------------------------------------

/**
 * An in-memory DataSource over the fixtures, with small delays so loading
 * states show. `scenario` picks the panel's status and the caller's access.
 */
export function fixtureSource(scenario: Scenario): DataSource {
  let info: DataInfo = scenarios[scenario];
  let nextSaved = 3;
  return {
    async info() {
      await sleep(80);
      return info;
    },
    async setup() {
      await sleep(600);
      info = {
        ...info,
        status: "ready",
        reason: undefined,
        set_up_at: new Date().toISOString(),
      };
      return info;
    },
    async schema() {
      await sleep(120);
      return fixtureSchema;
    },
    async rows(_ref, req, signal) {
      await sleep(150, signal);
      const t = findTable(req.table);
      if (!t)
        throw new Error(`Table ${req.table} not found. Reload the schema.`);
      const rows = select(t, req.filters ?? [], req.order);
      const offset = Number(req.after || 0);
      const limit = req.limit || 100;
      const page: DataPage = {
        columns: columnsOf(t),
        rows: rows.slice(offset, offset + limit),
        next: offset + limit < rows.length ? String(offset + limit) : undefined,
        estimated_rows: req.filters?.length ? rows.length : t.estimated_rows,
      };
      return page;
    },
    async references(_ref, req, signal) {
      await sleep(120, signal);
      const out: DataReferences[] = [];
      for (const t of tables) {
        for (const fk of t.foreign_keys ?? []) {
          if (`${fk.ref_schema}.${fk.ref_table}` !== req.table) continue;
          const column = fk.columns?.[0] ?? "";
          const value = req.key[fk.ref_columns?.[0] ?? ""] ?? "";
          const filters: DataFilter[] = [{ column, op: "eq", value }];
          const rows = select(t, filters);
          const limit = req.limit || 10;
          out.push({
            constraint: fk.name,
            schema: t.schema,
            table: t.name,
            columns: fk.columns,
            filters,
            page: {
              columns: columnsOf(t),
              rows: rows.slice(0, limit),
              next: rows.length > limit ? String(limit) : undefined,
              estimated_rows: rows.length,
            },
          });
        }
      }
      return out;
    },
    async *query(_ref, req, signal) {
      const started = performance.now();
      await sleep(200, signal);
      const sql = req.sql.trim();
      // The daemon records each query as an audit event: history reads it.
      const entry: DataHistoryEntry = {
        audit_id: (history[0]?.audit_id ?? 9000) + 1,
        id: (history[0]?.id ?? 500) + 1,
        created_at: new Date().toISOString(),
        sql,
        rows: 0,
        duration_ms: 0,
        outcome: "ok",
      };
      history.unshift(entry);
      const from = /\bfrom\s+([\w.]+)/i.exec(sql);
      if (!from?.[1]) {
        yield {
          type: "header",
          columns: [{ name: "?column?", type_name: "int4" }],
        };
        yield { type: "rows", rows: [[1]] };
        yield {
          type: "end",
          row_count: 1,
          command: "SELECT 1",
          duration_ms: 2,
        };
        return;
      }
      const t = findTable(from[1]);
      if (!t) {
        entry.outcome = "error";
        yield {
          type: "error",
          error: {
            message: `relation "${from[1]}" does not exist`,
            sqlstate: "42P01",
            position: from.index + from[0].length - from[1].length + 1,
          },
        } satisfies DataQueryFrame;
        return;
      }
      const limit = Number(/\blimit\s+(\d+)/i.exec(sql)?.[1] ?? 10_000);
      const rows = select(t, []).slice(0, Math.min(limit, req.limit || 10_000));
      yield { type: "header", columns: columnsOf(t) };
      for (let i = 0; i < rows.length; i += 500) {
        await sleep(40, signal);
        yield { type: "rows", rows: rows.slice(i, i + 500) };
      }
      entry.rows = rows.length;
      entry.duration_ms = Math.round(performance.now() - started);
      yield {
        type: "end",
        row_count: rows.length,
        command: `SELECT ${rows.length}`,
        duration_ms: entry.duration_ms,
      };
    },
    async history(_ref, before) {
      await sleep(60);
      return history
        .filter((h) => before === undefined || h.audit_id < before)
        .slice(0, 50);
    },
    async savedQueries() {
      await sleep(60);
      return saved;
    },
    async saveQuery(_ref, body, id) {
      await sleep(100);
      const now = new Date().toISOString();
      const existing = saved.find((q) => q.id === id);
      if (existing) {
        Object.assign(existing, body, { updated_at: now });
        return existing;
      }
      const query = {
        id: nextSaved++,
        ...body,
        created_at: now,
        updated_at: now,
      };
      saved.unshift(query);
      return query;
    },
    async deleteSavedQuery(_ref, id) {
      await sleep(60);
      const i = saved.findIndex((q) => q.id === id);
      if (i >= 0) saved.splice(i, 1);
    },
    async changeset(_ref, req) {
      await sleep(250);
      return {
        id: 0,
        status: "preview",
        author: "saile@example.com",
        author_kind: "user",
        changes: req.changes,
        sql: renderChangesSql(req.changes ?? []),
        confirm_commit: info.access.confirm_commit,
        created_at: new Date().toISOString(),
      };
    },
  };
}
