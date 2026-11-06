import { createFileRoute } from "@tanstack/react-router";
import { queries, servicePath } from "../data/queries";
import { BackupsPanel } from "../features/backups/BackupsPanel";
import { snapshotsQuery } from "../features/backups/data";

// A stateful service's restore points (ADR 0061): its backups and volume
// snapshots in one list, and the restores that ran.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/backups",
)({
  loader: ({ context: { queryClient }, params }) => {
    const ref = servicePath(params);
    return Promise.all([
      queryClient.ensureQueryData(queries.backups(ref)),
      queryClient.ensureQueryData(snapshotsQuery(ref)),
    ]);
  },
  component: BackupsTab,
});

function BackupsTab() {
  const ref = servicePath(Route.useParams());
  return <BackupsPanel serviceRef={ref} />;
}
