import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { AuditPage } from "../features/audit/AuditPage";
import { validateAuditSearch } from "../features/audit/search";

// The organization's audit log as the caller may see it. Admins
// also get it in the platform console (/console/audit).
export const Route = createFileRoute("/_app/$org/audit")({
  validateSearch: validateAuditSearch,
  loaderDeps: ({ search }) => ({
    project: search.project,
    environment: search.environment,
    action: search.action,
    limit: search.limit,
  }),
  loader: ({ context, deps }) =>
    context.queryClient.ensureQueryData(queries.audit(deps)),
  component: Audit,
});

function Audit() {
  const navigate = Route.useNavigate();
  return (
    <AuditPage
      search={Route.useSearch()}
      onSearch={(search) => navigate({ search, replace: true })}
    />
  );
}
