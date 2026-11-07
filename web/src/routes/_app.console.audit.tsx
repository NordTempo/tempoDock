import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { AuditPage } from "../features/audit/AuditPage";
import { validateAuditSearch } from "../features/audit/search";

// The platform's activity (ADR 0062): servers, accounts and logins,
// platform settings, updates and backups, and operators' actions on orgs.
// An org's own activity is in the org; an operator sees it by joining.
export const Route = createFileRoute("/_app/console/audit")({
  validateSearch: validateAuditSearch,
  loaderDeps: ({ search }) => ({
    platform: true as const,
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
      search={{
        ...Route.useSearch(),
        platform: true,
        project: undefined,
        environment: undefined,
      }}
      onSearch={(search) =>
        navigate({ search: { ...search, platform: undefined }, replace: true })
      }
    />
  );
}
