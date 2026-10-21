import { createFileRoute } from "@tanstack/react-router";
import { servicePath, useReleases, useService } from "../data/queries";
import { ReleasesTable } from "../features/service/ReleasesTable";

// The release history the API keeps on hand (newest 20).
export const Route = createFileRoute(
  "/_app/$project/$environment/$service/releases",
)({
  component: ReleasesTab,
});

function ReleasesTab() {
  const ref = servicePath(Route.useParams());
  const { deploying } = useService(ref);
  const releases = useReleases(ref);
  return (
    <ReleasesTable
      serviceRef={ref}
      releases={releases}
      deploying={!!deploying}
    />
  );
}
