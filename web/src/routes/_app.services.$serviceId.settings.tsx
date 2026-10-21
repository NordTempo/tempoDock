import { createFileRoute } from "@tanstack/react-router";
import { SettingsPanel } from "../features/settings/SettingsPanel";

export const Route = createFileRoute("/_app/services/$serviceId/settings")({
  component: SettingsTab,
});

function SettingsTab() {
  const { serviceId } = Route.useParams();
  return <SettingsPanel serviceId={serviceId} />;
}
