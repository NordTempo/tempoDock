import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { SettingsPanel } from "../features/settings/SettingsPanel";

export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/settings",
)({
  component: SettingsTab,
});

function SettingsTab() {
  return <SettingsPanel serviceRef={servicePath(Route.useParams())} />;
}
