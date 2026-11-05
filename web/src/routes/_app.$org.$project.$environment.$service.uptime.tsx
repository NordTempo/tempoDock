import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { uptimeChecks } from "../features/uptime/data";
import { UptimePanel } from "../features/uptime/UptimePanel";

// A service's uptime checks (ADR 0055).
// /shop/production/web/uptime
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/uptime",
)({
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(uptimeChecks(servicePath(params))),
  component: UptimeTab,
});

function UptimeTab() {
  return <UptimePanel serviceRef={servicePath(Route.useParams())} />;
}
