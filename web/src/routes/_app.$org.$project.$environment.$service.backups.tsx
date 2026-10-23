import { createFileRoute } from "@tanstack/react-router";
import { queries, servicePath } from "../data/queries";
import { BackupsPanel } from "../features/backups/BackupsPanel";

// A stateful service's backups: how, where and when, and the latest runs.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/backups",
)({
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(queries.backups(servicePath(params))),
  component: BackupsTab,
});

function BackupsTab() {
  return <BackupsPanel serviceRef={servicePath(Route.useParams())} />;
}
