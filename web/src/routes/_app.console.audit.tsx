import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { AuditPage } from "../features/audit/AuditPage";
import { validateAuditSearch } from "../features/audit/search";

// The organization's audit log, or the platform's, in the platform
// console. The same page as the workspace's Activity (/audit), under the
// console's sidebar.
export const Route = createFileRoute("/_app/console/audit")({
  validateSearch: validateAuditSearch,
  loaderDeps: ({ search }) => ({
    platform: search.platform,
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
      withPlatform
    />
  );
}
