import { createFileRoute } from "@tanstack/react-router";
import { ErrorsView } from "../features/analytics/ErrorsView";
import {
  type AnalyticsSearch,
  analyticsSearch,
} from "../features/analytics/loaders";
import { useSiteView } from "../features/analytics/SitePage";

// A site's errors in the Analytics area: the same view as its service's
// Errors tab (ADR 0059), so a site's page answers "do its pages have
// errors" too. …/errors?g=<group>, g the group opened under its row
// (error alerts link here).
type Search = AnalyticsSearch & { g?: string };

export const Route = createFileRoute(
  "/_app/$org/analytics/$project/$site/errors",
)({
  validateSearch: (s: Record<string, unknown>): Search => ({
    ...analyticsSearch(s),
    g: typeof s.g === "string" && /^[0-9a-f]{16}$/.test(s.g) ? s.g : undefined,
  }),
  component: Errors,
});

function Errors() {
  const { project, site, range, filters } = useSiteView();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  return (
    <ErrorsView
      project={project}
      site={site.name}
      range={range}
      filters={filters}
      open={search.g}
      onOpen={(g) => navigate({ search: (s) => ({ ...s, g }), replace: true })}
    />
  );
}
