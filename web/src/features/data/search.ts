import type { DataPanelView } from "./DataPanel";
import type { DataFilter, DataInsightView } from "./types";

// The panel's view in a route's search params, shared by Dock's Data tab
// and tempoBase's connection page:
// ?tab=sql&table=public.orders&order=-total&filter=[...], ?tab=health&insight=queries,
// and for Redis ?db=0&match=user:*&type=hash&key=user:1

/** The panel's view in the URL; the Tables (or Keys) tab is the default. */
export type DataSearch = {
  tab?: "keys" | "sql" | "schema" | "changes" | "health";
  table?: string;
  order?: string;
  filter?: DataFilter[];
  insight?: DataInsightView;
  db?: number;
  match?: string;
  type?: KeyType;
  key?: string;
  hex?: boolean;
};

/** A Redis key type the browser filters by. */
export type KeyType = "string" | "hash" | "list" | "set" | "zset" | "stream";

const tabs = ["keys", "sql", "schema", "changes", "health"] as const;
const keyTypes = ["string", "hash", "list", "set", "zset", "stream"] as const;
const insights = ["queries", "sessions", "tables", "indexes", "keys"] as const;
const ops = new Set<string>([
  "eq",
  "ne",
  "lt",
  "lte",
  "gt",
  "gte",
  "like",
  "ilike",
  "in",
  "is_null",
  "not_null",
]);

const text = (v: unknown) => (typeof v === "string" && v ? v : undefined);

const isFilter = (f: unknown): f is DataFilter =>
  typeof f === "object" &&
  f !== null &&
  "column" in f &&
  typeof f.column === "string" &&
  "op" in f &&
  typeof f.op === "string" &&
  ops.has(f.op);

/** A route's validateSearch: keeps what the panel understands. */
export function validateDataSearch(
  search: Record<string, unknown>,
): DataSearch {
  const filter = Array.isArray(search.filter)
    ? search.filter.filter(isFilter)
    : [];
  return {
    tab: tabs.find((t) => t === search.tab),
    table: text(search.table),
    order: text(search.order),
    filter: filter.length ? filter : undefined,
    insight: insights.find((v) => v === search.insight),
    db:
      typeof search.db === "number" &&
      Number.isInteger(search.db) &&
      search.db > 0
        ? search.db
        : undefined,
    match: text(search.match),
    type: keyTypes.find((t) => t === search.type),
    key: text(search.key),
    hex: search.hex === true || undefined,
  };
}

/** The panel's view from the search. */
export const searchView = (search: DataSearch): DataPanelView => ({
  tab: search.tab ?? "tables",
  table: search.table,
  order: search.order,
  filters: search.filter,
  insight: search.insight,
  db: search.db,
  match: search.match,
  keyType: search.type,
  key: search.key,
  keyHex: search.hex,
});

/** The search for a view, leaving defaults out of the URL. */
export const viewSearch = (view: DataPanelView): DataSearch => ({
  // Tables and Keys are each engine's first tab: the default.
  tab: view.tab === "tables" || view.tab === "keys" ? undefined : view.tab,
  table: view.table,
  order: view.order,
  filter: view.filters?.length ? view.filters : undefined,
  insight: view.tab === "health" ? view.insight : undefined,
  db: view.db || undefined,
  match: view.match,
  type: view.keyType,
  key: view.key,
  hex: view.keyHex || undefined,
});
