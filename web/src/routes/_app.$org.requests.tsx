import { createFileRoute } from "@tanstack/react-router";
import { requestQueries } from "../features/requests/data";
import { RequestsPage } from "../features/requests/RequestsPage";

// Access requests (ADR 0044): what tokens asked to do in locked
// environments, for the organization's admins to approve or reject.
// ?id= selects one.
export const Route = createFileRoute("/_app/$org/requests")({
  validateSearch: (s: Record<string, unknown>): { id?: number } => {
    const id = Number(s.id);
    return Number.isInteger(id) && id > 0 ? { id } : {};
  },
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(requestQueries.list()),
  component: Requests,
});

function Requests() {
  return <RequestsPage selected={Route.useSearch().id} />;
}
