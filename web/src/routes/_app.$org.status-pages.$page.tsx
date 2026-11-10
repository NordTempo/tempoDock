import { createFileRoute } from "@tanstack/react-router";
import { statusQueries } from "../features/status/data";
import { StatusPageEditor } from "../features/status/StatusPageEditor";

// One status page (ADR 0073): its components, their status and history,
// and its settings.
export const Route = createFileRoute("/_app/$org/status-pages/$page")({
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(statusQueries.page(params.page)),
  component: Editor,
});

function Editor() {
  return <StatusPageEditor slug={Route.useParams().page} />;
}
