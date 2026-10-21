import { createFileRoute } from "@tanstack/react-router";
import { useReleases, useService } from "../data/queries";
import { applyNote } from "../features/service/releases";
import { VariablesEditor } from "../features/variables/VariablesEditor";

export const Route = createFileRoute("/_app/services/$serviceId/variables")({
  component: VariablesTab,
});

function VariablesTab() {
  const { serviceId } = Route.useParams();
  const service = useService(serviceId);
  const releases = useReleases(serviceId);
  return (
    <VariablesEditor
      target={{ environmentId: service.environmentId, serviceId }}
      environmentName={service.environmentName}
      note={applyNote(service, releases)}
    />
  );
}
