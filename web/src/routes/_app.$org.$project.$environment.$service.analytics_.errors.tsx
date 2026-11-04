import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { AnalyticsTab } from "../features/analytics/AnalyticsTab";
import { applyChange, parseFilter } from "../features/analytics/data";
import { ErrorsView } from "../features/analytics/ErrorsView";
import {
  type AnalyticsSearch,
  analyticsSearch,
  sitesQuery,
} from "../features/analytics/loaders";

// A site's errors (ADR 0037): …/web/analytics/errors?site=…&range=7d&g=<group>,
// g the group opened under its row.
type Search = AnalyticsSearch & { g?: string };

export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/analytics_/errors",
)({
  validateSearch: (s: Record<string, unknown>): Search => ({
    ...analyticsSearch(s),
    ...(typeof s.g === "string" && /^[0-9a-f]{16}$/.test(s.g) && { g: s.g }),
  }),
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(sitesQuery(params.project)),
  component: Errors,
});

function Errors() {
  const ref = servicePath(Route.useParams());
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const filters = (search.f ?? []).flatMap((f) => parseFilter(f) ?? []);
  const range = search.range ?? "30d";
  return (
    <AnalyticsTab
      serviceRef={ref}
      view="/$org/$project/$environment/$service/analytics/errors"
      site={search.site}
      range={range}
      filters={filters}
      onChange={(change) =>
        navigate({ search: (s) => applyChange(s, change), replace: true })
      }
    >
      {(site) => (
        <ErrorsView
          project={ref.project}
          site={site.name}
          range={range}
          filters={filters}
          open={search.g}
          onOpen={(g) =>
            navigate({ search: (s) => ({ ...s, g }), replace: true })
          }
        />
      )}
    </AnalyticsTab>
  );
}
