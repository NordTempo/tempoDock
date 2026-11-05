import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { SettingsPanel } from "../features/settings/SettingsPanel";

// A service's Build & run tab (ADR 0059): source, builder, commands,
// health check, resources, replicas and server.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/build",
)({
  component: BuildTab,
});

function BuildTab() {
  return <SettingsPanel serviceRef={servicePath(Route.useParams())} />;
}
