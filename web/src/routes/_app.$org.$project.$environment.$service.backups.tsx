import { createFileRoute } from "@tanstack/react-router";
import { queries, servicePath } from "../data/queries";
import { BackupsPanel } from "../features/backups/BackupsPanel";
import { snapshotsQuery } from "../features/backups/data";
import { SnapshotsPanel } from "../features/backups/SnapshotsPanel";

// A stateful service's backups: how, where and when, and the latest runs;
// then its volume snapshots.
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
  return (
    <>
      <BackupsPanel serviceRef={ref} />
      <SnapshotsPanel serviceRef={ref} />
    </>
  );
}
