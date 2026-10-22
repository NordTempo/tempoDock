import { createFileRoute } from "@tanstack/react-router";
import { useMemo } from "react";
import { DataSourceContext } from "../../../src/features/data/api";
import { DataPanel } from "../../../src/features/data/DataPanel";
import {
  searchView,
  validateDataSearch,
  viewSearch,
} from "../../../src/features/data/search";
import { keys, queries } from "../data/queries";
import { connectionSource } from "../data/source";

// One connection's data panel. Like Dock's Data tab, everything it shows
// is in the URL: /c/shop?table=public.orders&order=-total&filter=[...].
// Its tables are in the sidebar (shell/ConnectionTables.tsx).
export const Route = createFileRoute("/_app/c/$connection/")({
  validateSearch: validateDataSearch,
  loader: ({ context, params }) => {
    // Proposals arrive from agents while nobody looks: the changeset list
    // refetches when the window regains focus (no event stream, no polling).
    context.queryClient.setQueryDefaults(
      [...keys.data(params.connection), "changesets"],
      { refetchOnWindowFocus: true },
    );
    return context.queryClient.ensureQueryData(
      queries.connection(params.connection),
    );
  },
  component: ConnectionPanel,
});

function ConnectionPanel() {
  const { connection } = Route.useParams();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  // A stable source: the panel's hooks key callbacks on it.
  const source = useMemo(() => connectionSource(connection), [connection]);
  return (
    <DataSourceContext value={source}>
      <DataPanel
        key={connection}
        className="h-screen"
        tableList={false}
        view={searchView(search)}
        onView={(next) => navigate({ search: viewSearch(next) })}
      />
    </DataSourceContext>
  );
}
