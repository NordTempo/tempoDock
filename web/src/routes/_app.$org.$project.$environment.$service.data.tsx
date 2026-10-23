import { createFileRoute } from "@tanstack/react-router";
import { useMemo } from "react";
import { ConnectHint } from "../features/connect/ConnectHint";
import { DataSourceContext } from "../features/data/api";
import { DataPanel } from "../features/data/DataPanel";
import {
  searchView,
  validateDataSearch,
  viewSearch,
} from "../features/data/search";
import { serviceSource } from "../features/data/source";

// The Data tab of a database service (ADR 0016). Its component, the grid
// and the panel are one lazy chunk; CodeMirror is another, loaded by the
// SQL tab. Everything the panel shows is in the URL:
// /shop/staging/postgres/data?table=public.orders&order=-total&filter=[...]
// A line above the panel shows `dock connect` for the service.

export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/data",
)({
  validateSearch: validateDataSearch,
  component: DataTab,
});

function DataTab() {
  const { project, environment, service } = Route.useParams();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  // A stable source: the panel's hooks key callbacks on it.
  const source = useMemo(
    () => serviceSource({ project, environment, service }),
    [project, environment, service],
  );
  return (
    <div className="flex flex-col gap-3">
      <ConnectHint serviceRef={{ project, environment, service }} />
      <DataSourceContext value={source}>
        <DataPanel
          view={searchView(search)}
          onView={(next) => navigate({ search: viewSearch(next) })}
          // The default height, less the connect line.
          className="h-[calc(100vh-276px)] min-h-[480px]"
        />
      </DataSourceContext>
    </div>
  );
}
