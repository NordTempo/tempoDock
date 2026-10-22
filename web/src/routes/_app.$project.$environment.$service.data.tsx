import { createFileRoute } from "@tanstack/react-router";
import { useMemo } from "react";
import { DataSourceContext } from "../features/data/api";
import { DataPanel, type DataPanelView } from "../features/data/DataPanel";
import { apiSource } from "../features/data/source";
import type { DataFilter } from "../features/data/types";

// The Data tab of a database service (ADR 0016). Its component, the grid
// and the panel are one lazy chunk; CodeMirror is another, loaded by the
// SQL tab. Everything the panel shows is in the URL:
// /shop/staging/postgres/data?table=public.orders&order=-total&filter=[...]

/** The panel's view in the URL; the Tables tab is the default. */
type DataSearch = {
  tab?: "sql" | "schema" | "changes";
  table?: string;
  order?: string;
  filter?: DataFilter[];
};

const tabs = ["sql", "schema", "changes"] as const;
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

export const Route = createFileRoute(
  "/_app/$project/$environment/$service/data",
)({
  validateSearch: (search: Record<string, unknown>): DataSearch => {
    const filter = Array.isArray(search.filter)
      ? search.filter.filter(isFilter)
      : [];
    return {
      tab: tabs.find((t) => t === search.tab),
      table: text(search.table),
      order: text(search.order),
      filter: filter.length ? filter : undefined,
    };
  },
  component: DataTab,
});

function DataTab() {
  const { project, environment, service } = Route.useParams();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  // A stable ref: the panel's hooks key callbacks on it.
  const ref = useMemo(
    () => ({ project, environment, service }),
    [project, environment, service],
  );
  const view: DataPanelView = {
    tab: search.tab ?? "tables",
    table: search.table,
    order: search.order,
    filters: search.filter,
  };
  return (
    <DataSourceContext value={apiSource}>
      <DataPanel
        serviceRef={ref}
        view={view}
        onView={(next) =>
          navigate({
            search: {
              tab: next.tab === "tables" ? undefined : next.tab,
              table: next.table,
              order: next.order,
              filter: next.filters?.length ? next.filters : undefined,
            },
          })
        }
      />
    </DataSourceContext>
  );
}
