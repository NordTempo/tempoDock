import { createFileRoute } from "@tanstack/react-router";
import { servicePath, useCanDeploy } from "../data/queries";
import { AnalyticsTab } from "../features/analytics/AnalyticsTab";
import { applyChange, parseFilter } from "../features/analytics/data";
import { FunnelsView } from "../features/analytics/FunnelsView";
import {
  type AnalyticsSearch,
  analyticsSearch,
  sitesQuery,
} from "../features/analytics/loaders";

// A site's funnels (ADR 0037): …/web/analytics/funnels?site=…&funnel=Checkout,
// funnel the one shown (the first if none).
type Search = AnalyticsSearch & { funnel?: string };

export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/analytics_/funnels",
)({
  validateSearch: (s: Record<string, unknown>): Search => ({
    ...analyticsSearch(s),
    ...(typeof s.funnel === "string" && s.funnel && { funnel: s.funnel }),
  }),
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(sitesQuery(params.project)),
  component: Funnels,
});

function Funnels() {
  const ref = servicePath(Route.useParams());
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const filters = (search.f ?? []).flatMap((f) => parseFilter(f) ?? []);
  const range = search.range ?? "30d";
  const canEdit = useCanDeploy(ref);
  return (
    <AnalyticsTab
      serviceRef={ref}
      view="/$org/$project/$environment/$service/analytics/funnels"
      site={search.site}
      range={range}
      filters={filters}
      onChange={(change) =>
        navigate({ search: (s) => applyChange(s, change), replace: true })
      }
    >
      {(site) => (
        <FunnelsView
          project={ref.project}
          site={site.name}
          range={range}
          filters={filters}
          selected={search.funnel}
          onSelect={(funnel) =>
            navigate({ search: (s) => ({ ...s, funnel }), replace: true })
          }
          canEdit={canEdit}
        />
      )}
    </AnalyticsTab>
  );
}
