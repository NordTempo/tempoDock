import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { AnalyticsTab } from "../features/analytics/AnalyticsTab";
import { applyChange, parseFilter } from "../features/analytics/data";
import {
  type AnalyticsSearch,
  analyticsSearch,
  sitesQuery,
  type Vital,
  vitals,
} from "../features/analytics/loaders";
import { VitalsView } from "../features/analytics/VitalsView";

// A site's Web Vitals (ADR 0037): …/web/analytics/vitals?site=…&m=inp&device=mobile,
// m the vital charted (LCP if none), device the devices shown.
type Search = AnalyticsSearch & { m?: Vital; device?: string };

export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/analytics_/vitals",
)({
  validateSearch: (s: Record<string, unknown>): Search => ({
    ...analyticsSearch(s),
    ...(vitals.includes(s.m as Vital) && { m: s.m as Vital }),
    ...(["desktop", "mobile", "tablet"].includes(s.device as string) && {
      device: s.device as string,
    }),
  }),
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(sitesQuery(params.project)),
  component: Vitals,
});

function Vitals() {
  const ref = servicePath(Route.useParams());
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const filters = (search.f ?? []).flatMap((f) => parseFilter(f) ?? []);
  const range = search.range ?? "30d";
  return (
    <AnalyticsTab
      serviceRef={ref}
      view="/$org/$project/$environment/$service/analytics/vitals"
      site={search.site}
      range={range}
      filters={filters}
      onChange={(change) =>
        navigate({ search: (s) => applyChange(s, change), replace: true })
      }
    >
      {(site) => (
        <VitalsView
          project={ref.project}
          site={site.name}
          range={range}
          filters={filters}
          metric={search.m ?? "lcp"}
          device={search.device}
          onMetric={(m) =>
            navigate({
              search: (s) => ({ ...s, m: m === "lcp" ? undefined : m }),
              replace: true,
            })
          }
          onDevice={(device) =>
            navigate({ search: (s) => ({ ...s, device }), replace: true })
          }
        />
      )}
    </AnalyticsTab>
  );
}
