import { createFileRoute } from "@tanstack/react-router";
import { PolicyPage } from "../features/data/PolicyPage";
import type { PolicyScope } from "../features/data/policy";

const text = (v: unknown) => (typeof v === "string" && v ? v : undefined);

// The data access policy (ADR 0016): /data-policy for the default,
// ?project=shop for a project's rules, &environment=staging for one
// environment's. Everyone may read it; admins change it.
export const Route = createFileRoute("/_app/data-policy")({
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
