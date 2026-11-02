import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { AnalyticsTab } from "../features/analytics/AnalyticsTab";
import { Dashboard } from "../features/analytics/Dashboard";
import {
  analyticsSearch,
  applyChange,
  parseFilter,
  sitesQuery,
} from "../features/analytics/data";

// A service's Analytics tab (ADR 0037): its site's dashboard.
// /shop/production/web/analytics?site=shop.example.com&range=7d&f=country:is:NO
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/analytics",
)({
  validateSearch: analyticsSearch,
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(sitesQuery(params.project)),
  component: Tab,
});

function Tab() {
  const ref = servicePath(Route.useParams());
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const filters = (search.f ?? []).flatMap((f) => parseFilter(f) ?? []);
  const range = search.range ?? "30d";
  return (
    <AnalyticsTab
      serviceRef={ref}
      view="/$org/$project/$environment/$service/analytics"
      site={search.site}
      range={range}
      filters={filters}
      onChange={(change) =>
        navigate({ search: (s) => applyChange(s, change), replace: true })
      }
    >
      {(site, onPick) => (
        <Dashboard
          project={ref.project}
          site={site}
          range={range}
          filters={filters}
          onPick={onPick}
        />
      )}
    </AnalyticsTab>
  );
}
