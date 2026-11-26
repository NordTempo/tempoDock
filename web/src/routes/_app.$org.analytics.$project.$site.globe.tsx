import { createFileRoute } from "@tanstack/react-router";
import { GlobeView } from "../features/analytics/GlobeView";
import { useSiteView } from "../features/analytics/SitePage";

// A site's visitors by country on a globe, its own view beside Overview.
export const Route = createFileRoute(
  "/_app/$org/analytics/$project/$site/globe",
)({
  component: GlobeRoute,
});

function GlobeRoute() {
  const { project, site, range, filters, onPick } = useSiteView();
  return (
    <GlobeView
      project={project}
      site={site.name}
      range={range}
      filters={filters}
      onPick={onPick}
    />
  );
}
