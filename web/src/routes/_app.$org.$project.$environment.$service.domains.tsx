import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { DomainsPanel } from "../features/service/DomainsPanel";

export const Route = createFileRoute(
  "/_app/$project/$environment/$service/domains",
)({
  component: DomainsTab,
});

function DomainsTab() {
  return <DomainsPanel serviceRef={servicePath(Route.useParams())} />;
}
