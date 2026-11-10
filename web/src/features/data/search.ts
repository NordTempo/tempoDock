import type { DataPanelView } from "./DataPanel";
import type { DataFilter, DataInsightView } from "./types";

// The panel's view in a route's search params, shared by Dock's Data tab
// and tempoBase's connection page:
// ?tab=sql&table=public.orders&order=-total&filter=[...], ?tab=health&insight=queries

/** The panel's view in the URL; the Tables tab is the default. */
export type DataSearch = {
  tab?: "sql" | "schema" | "changes" | "health";
  table?: string;
  order?: string;
  filter?: DataFilter[];
  insight?: DataInsightView;
};

const tabs = ["sql", "schema", "changes", "health"] as const;
const insights = ["queries", "sessions", "tables", "indexes"] as const;
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
  };
}

/** The panel's view from the search. */
export const searchView = (search: DataSearch): DataPanelView => ({
  tab: search.tab ?? "tables",
  table: search.table,
  order: search.order,
  filters: search.filter,
  insight: search.insight,
});

/** The search for a view, leaving defaults out of the URL. */
export const viewSearch = (view: DataPanelView): DataSearch => ({
  tab: view.tab === "tables" ? undefined : view.tab,
  table: view.table,
  order: view.order,
  filter: view.filters?.length ? view.filters : undefined,
  insight: view.tab === "health" ? view.insight : undefined,
});
