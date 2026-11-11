import { createFileRoute } from "@tanstack/react-router";
import { servicePath, useCanDeploy } from "../data/queries";
import { Dashboard } from "../features/analytics/Dashboard";
import {
  type AnalyticsChange,
  analyticsSearch,
  applyChange,
  parseFilter,
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
  const canEdit = useCanDeploy(ref);
  const range = search.range ?? "30d";
  const filters = (search.f ?? []).flatMap((f) => parseFilter(f) ?? []);
  const onChange = (change: AnalyticsChange) =>
    navigate({ search: (s) => applyChange(s, change), replace: true });
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
          canEdit={canEdit}
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
