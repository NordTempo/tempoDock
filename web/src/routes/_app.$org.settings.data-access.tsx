import { createFileRoute } from "@tanstack/react-router";
import type { PolicyScope } from "../features/data/dockPolicy";
import { PolicyPage } from "../features/data/PolicyPage";

const text = (v: unknown) => (typeof v === "string" && v ? v : undefined);

// The org's data access policy (ADR 0016), in its settings (ADR 0062):
// /acme/settings/data-access for the org's default, ?project=shop for a
// project's rules, &environment=staging for one environment's. The
// platform's default and ceiling are the console's.
export const Route = createFileRoute("/_app/$org/settings/data-access")({
  validateSearch: (search: Record<string, unknown>): PolicyScope => {
    const project = text(search.project);
    return {
      project,
      environment: project ? text(search.environment) : undefined,
    };
  },
  component: DataPolicy,
});

function DataPolicy() {
  const navigate = Route.useNavigate();
  return (
    <PolicyPage
      scope={Route.useSearch()}
      onScope={(search) => navigate({ search })}
    />
  );
}
