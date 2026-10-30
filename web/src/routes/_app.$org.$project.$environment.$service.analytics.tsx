import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { AnalyticsTab } from "../features/analytics/AnalyticsTab";
import {
  filterParam,
  parseFilter,
  type StatsRange,
  sitesQuery,
  statsRanges,
} from "../features/analytics/data";

// A service's Analytics tab (ADR 0037): its site's dashboard.
// /shop/production/web/analytics?site=shop.example.com&range=7d&f=country:is:NO
type Search = { site?: string; range?: StatsRange; f?: string[] };

export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/analytics",
)({
  validateSearch: (s: Record<string, unknown>): Search => ({
    ...(typeof s.site === "string" && { site: s.site }),
    ...(statsRanges.includes(s.range as StatsRange) && {
      range: s.range as StatsRange,
    }),
    ...(Array.isArray(s.f) && {
      f: s.f.filter(
        (f): f is string => typeof f === "string" && !!parseFilter(f),
      ),
    }),
  }),
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(sitesQuery(params.project)),
  component: Tab,
});

function Tab() {
  const ref = servicePath(Route.useParams());
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  return (
    <AnalyticsTab
      serviceRef={ref}
      site={search.site}
      range={search.range ?? "30d"}
      filters={(search.f ?? []).flatMap((f) => parseFilter(f) ?? [])}
      onChange={(change) =>
        navigate({
          search: (s) => {
            const next = { ...s };
            if ("site" in change) next.site = change.site;
            if (change.range)
              next.range = change.range === "30d" ? undefined : change.range;
            if (change.filters)
              next.f = change.filters.length
                ? change.filters.map(filterParam)
                : undefined;
            return next;
          },
          replace: true,
        })
      }
    />
  );
}
