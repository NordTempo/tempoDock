import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { DomainsPanel } from "../features/service/DomainsPanel";

export const Route = createFileRoute("/_app/services/$serviceId/domains")({
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(queries.domains(params.serviceId)),
  component: DomainsTab,
});

function DomainsTab() {
  const { serviceId } = Route.useParams();
  return <DomainsPanel serviceId={serviceId} />;
}
