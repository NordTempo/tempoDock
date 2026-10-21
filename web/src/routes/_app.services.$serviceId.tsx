import { createFileRoute, Outlet } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { ServiceHeader } from "../features/service/ServiceHeader";

// Service layout: header, tabs and meta stay put while tabs swap below.
export const Route = createFileRoute("/_app/services/$serviceId")({
  loader: async ({ context: { queryClient }, params }) => {
    const service = await queryClient.ensureQueryData(
      queries.service(params.serviceId),
    );
    await Promise.all([
      queryClient.ensureQueryData(queries.releases(service.id)),
      queryClient.ensureQueryData(queries.pending(service.environmentId)),
    ]);
  },
  component: ServiceLayout,
});

function ServiceLayout() {
  const { serviceId } = Route.useParams();
  return (
    <>
      <ServiceHeader serviceId={serviceId} />
      <Outlet />
    </>
  );
}
