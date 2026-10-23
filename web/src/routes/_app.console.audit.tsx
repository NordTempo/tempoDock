import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { AuditPage, validateAuditSearch } from "../features/audit/AuditPage";

// Every project's audit log, in the platform console. The same page as
// the workspace's Activity (/audit), under the console's sidebar.
export const Route = createFileRoute("/_app/console/audit")({
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
