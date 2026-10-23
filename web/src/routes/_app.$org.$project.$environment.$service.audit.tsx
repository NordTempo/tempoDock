import { createFileRoute } from "@tanstack/react-router";
import { queries, servicePath, useAuditLog } from "../data/queries";
import { AuditTable } from "../features/audit/AuditTable";

// The service's own events: releases, settings, variables, volumes, domains.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/audit",
)({
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(queries.audit(servicePath(params))),
  component: AuditTab,
});

function AuditTab() {
  return <AuditTable events={useAuditLog(servicePath(Route.useParams()))} />;
}
