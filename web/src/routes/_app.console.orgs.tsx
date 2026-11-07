import { createFileRoute } from "@tanstack/react-router";
import { consoleOrgsQuery } from "../features/orgs/data";
import { OrgsPage } from "../features/orgs/OrgsPage";

// Every organization with its people, size and use, and the operator's
// actions on the one picked (ADR 0062).
export const Route = createFileRoute("/_app/console/orgs")({
  validateSearch: (s: Record<string, unknown>): { org?: string } => ({
    org: typeof s.org === "string" ? s.org : undefined,
  }),
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(consoleOrgsQuery(true)),
  component: function Orgs() {
    return <OrgsPage selected={Route.useSearch().org} />;
  },
});
