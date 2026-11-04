import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { AnalyticsTab } from "../features/analytics/AnalyticsTab";
import { applyChange, parseFilter } from "../features/analytics/data";
import {
  type AnalyticsSearch,
  analyticsSearch,
  sitesQuery,
} from "../features/analytics/loaders";
import { ReplaysView } from "../features/analytics/ReplaysView";

// A site's session replays (ADR 0037): …/web/analytics/replays?site=…&errors=true&device=mobile.
type Search = AnalyticsSearch & { errors?: boolean; device?: string };

export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/analytics_/replays/",
)({
  validateSearch: (s: Record<string, unknown>): Search => ({
    ...analyticsSearch(s),
    ...(s.errors === true && { errors: true }),
    ...(["desktop", "mobile", "tablet"].includes(s.device as string) && {
      device: s.device as string,
    }),
  }),
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(sitesQuery(params.project)),
  component: Replays,
});

function Replays() {
  const ref = servicePath(Route.useParams());
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const filters = (search.f ?? []).flatMap((f) => parseFilter(f) ?? []);
  const range = search.range ?? "30d";
  return (
    <AnalyticsTab
      serviceRef={ref}
      view="/$org/$project/$environment/$service/analytics/replays"
      site={search.site}
      range={range}
      filters={filters}
      onChange={(change) =>
        navigate({ search: (s) => applyChange(s, change), replace: true })
      }
    >
      {(site) => (
        <ReplaysView
          project={ref.project}
          site={site}
          range={range}
          filters={filters}
          errorsOnly={!!search.errors}
          device={search.device}
          onErrorsOnly={(on) =>
            navigate({
              search: (s) => ({ ...s, errors: on || undefined }),
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
