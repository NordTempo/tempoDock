import { createFileRoute } from "@tanstack/react-router";
import { FunnelsView } from "../features/analytics/FunnelsView";
import {
  type AnalyticsSearch,
  analyticsSearch,
} from "../features/analytics/loaders";
import { useSiteView } from "../features/analytics/SitePage";

// A site's funnels (ADR 0037): …/funnels?funnel=Checkout, funnel the one
// shown (the first if none).
type Search = AnalyticsSearch & { funnel?: string };

export const Route = createFileRoute(
  "/_app/$org/analytics/$project/$site/funnels",
)({
  validateSearch: (s: Record<string, unknown>): Search => ({
    ...analyticsSearch(s),
    funnel: typeof s.funnel === "string" && s.funnel ? s.funnel : undefined,
  }),
  component: Funnels,
});

function Funnels() {
  const { project, site, range, filters, canEdit } = useSiteView();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  return (
    <FunnelsView
      project={project}
      site={site.name}
      range={range}
      filters={filters}
      selected={search.funnel}
      onSelect={(funnel) =>
        navigate({ search: (s) => ({ ...s, funnel }), replace: true })
      }
      canEdit={canEdit}
    />
  );
}
