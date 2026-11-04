import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { runQueries } from "../features/runs/data";
import { RunPanel } from "../features/runs/RunPanel";

// One-off commands in the service's active release (ADR 0048): the
// command box, the latest runs and the selected run's output.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/run",
)({
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(runQueries.list(servicePath(params))),
  component: RunTab,
});

function RunTab() {
  return <RunPanel serviceRef={servicePath(Route.useParams())} />;
}
