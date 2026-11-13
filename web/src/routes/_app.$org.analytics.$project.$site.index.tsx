import { createFileRoute } from "@tanstack/react-router";
import { Dashboard } from "../features/analytics/Dashboard";
import { useSiteView } from "../features/analytics/SitePage";

// A site's dashboard (ADR 0037): its Overview view.
export const Route = createFileRoute("/_app/$org/analytics/$project/$site/")({
  component: Overview,
});

function Overview() {
  const { project, site, range, filters, onPick } = useSiteView();
  return (
    <Dashboard
      project={project}
      site={site}
      range={range}
      filters={filters}
      onPick={onPick}
    />
  );
}
