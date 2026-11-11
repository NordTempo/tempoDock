import { createFileRoute } from "@tanstack/react-router";
import { statusQueries } from "../features/status/data";
import { IncidentEditor } from "../features/status/IncidentEditor";

// One incident on a status page (ADR 0073): posting updates, the
// timeline and what its components say now. It reads its page, which
// carries the page's incidents.
export const Route = createFileRoute(
  "/_app/$org/status-pages/$page_/incidents/$incident",
)({
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(statusQueries.page(params.page)),
  component: Incident,
});

function Incident() {
  const { page, incident } = Route.useParams();
  return <IncidentEditor slug={page} id={incident} />;
}
