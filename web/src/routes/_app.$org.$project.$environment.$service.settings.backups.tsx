import { createFileRoute } from "@tanstack/react-router";
import { queries, servicePath } from "../data/queries";
import { BackupsPanel } from "../features/backups/BackupsPanel";
import { snapshotsQuery } from "../features/backups/data";

// Settings' Backups view, for stateful services (ADR 0061, ADR 0076): its
// backups and volume snapshots in one list of restore points, and the
// restores that ran. #backup-<id> marks a run.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/settings/backups",
)({
  loader: ({ context: { queryClient }, params }) => {
    const ref = servicePath(params);
    return Promise.all([
      queryClient.ensureQueryData(queries.backups(ref)),
      queryClient.ensureQueryData(snapshotsQuery(ref)),
    ]);
  },
  component: Backups,
});

function Backups() {
  return <BackupsPanel serviceRef={servicePath(Route.useParams())} />;
}
