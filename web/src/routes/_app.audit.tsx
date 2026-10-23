import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { AuditPage, validateAuditSearch } from "../features/audit/AuditPage";

// The workspace's Activity: the audit log as the caller may see it. Admins
// also get it in the platform console (/console/audit).
export const Route = createFileRoute("/_app/audit")({
  validateSearch: validateAuditSearch,
  loaderDeps: ({ search }) => ({
    project: search.project,
    action: search.action,
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
