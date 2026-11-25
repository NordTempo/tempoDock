import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { Dashboard } from "../features/analytics/Dashboard";
import {
  type AnalyticsChange,
  analyticsSearch,
  applyChange,
  parseFilter,
  siteRange,
  sitesQuery,
} from "../features/analytics/data";
import { ServiceSite } from "../features/analytics/ServiceSite";

// Requests' Visitors view: the dashboard of the site the service's
// domains count for (ADR 0037), in the service's shell. The site's
// other views (Vitals, Funnels, Replays) stay in the Analytics area.
// /shop/production/web/requests/visitors?range=7d&f=country:is:NO
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/requests/visitors",
)({
  validateSearch: analyticsSearch,
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(sitesQuery(params.project)),
  component: Visitors,
});

function Visitors() {
  const ref = servicePath(Route.useParams());
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const range = search.range ?? siteRange.get();
  const filters = (search.f ?? []).flatMap((f) => parseFilter(f) ?? []);
  const onChange = (change: AnalyticsChange) => {
    if (change.range) siteRange.set(change.range);
    navigate({ search: (s) => applyChange(s, change), replace: true });
  };
  return (
    <ServiceSite
      serviceRef={ref}
      site={search.site}
      range={range}
      filters={filters}
      onChange={onChange}
    >
      {(site) => (
        <Dashboard
          project={ref.project}
          site={site}
          range={range}
          filters={filters}
          onPick={(dimension, row) =>
            onChange({
              filters: [
                ...filters.filter((f) => f.dimension !== dimension),
                { dimension, op: "is", value: row.value },
              ],
            })
          }
        />
      )}
    </ServiceSite>
  );
}
