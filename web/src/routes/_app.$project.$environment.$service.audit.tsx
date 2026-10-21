import { createFileRoute } from "@tanstack/react-router";
import { queries, servicePath, useAuditLog } from "../data/queries";
import { AuditTable } from "../features/audit/AuditTable";
import { Heading } from "../ui/kit";

// The service's own events: releases, settings, variables, volumes, domains.
export const Route = createFileRoute(
  "/_app/$project/$environment/$service/audit",
)({
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(queries.audit(servicePath(params))),
  component: AuditTab,
});

function AuditTab() {
  const events = useAuditLog(servicePath(Route.useParams()));
  return (
    <div className="px-5">
      <Heading>Audit</Heading>
      <AuditTable events={events} />
    </div>
  );
}
