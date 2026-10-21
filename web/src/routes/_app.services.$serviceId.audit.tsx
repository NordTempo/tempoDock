import { createFileRoute } from "@tanstack/react-router";
import { queries, useAuditLog } from "../data/queries";
import { AuditTable } from "../features/audit/AuditTable";
import { Heading } from "../ui/kit";

export const Route = createFileRoute("/_app/services/$serviceId/audit")({
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(
      queries.audit({ serviceId: params.serviceId }),
    ),
  component: AuditTab,
});

function AuditTab() {
  const { serviceId } = Route.useParams();
  const events = useAuditLog({ serviceId });
  return (
    <div className="px-5">
      <Heading>Audit</Heading>
      <AuditTable events={events} />
    </div>
  );
}
