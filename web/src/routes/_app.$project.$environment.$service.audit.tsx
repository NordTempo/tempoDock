import { createFileRoute } from "@tanstack/react-router";
import { envPath, queries, useAuditLog } from "../data/queries";
import { AuditTable } from "../features/audit/AuditTable";
import { Heading } from "../ui/kit";

// The API filters audit events by environment; the service's own events are
// the ones whose target is the service.
export const Route = createFileRoute(
  "/_app/$project/$environment/$service/audit",
)({
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(queries.audit(envPath(params))),
  component: AuditTab,
});

function AuditTab() {
  const params = Route.useParams();
  const target = `${params.project}/${params.environment}/${params.service}`;
  const events = useAuditLog(envPath(params)).filter(
    (e) => e.target === target,
  );
  return (
    <div className="px-5">
      <Heading>Audit</Heading>
      <AuditTable events={events} />
    </div>
  );
}
