import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { ShellPanel } from "../features/shell/ShellPanel";

// A service's Shell tab (ADR 0111): a terminal in its running container.
// xterm.js comes with this route's chunk only.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/shell",
)({
  component: ShellTab,
});

function ShellTab() {
  const params = Route.useParams();
  const ref = servicePath(params);
  // Another service is another panel: the old one's socket closes with it.
  return (
    <ShellPanel
      key={`${params.org}/${ref.project}/${ref.environment}/${ref.service}`}
      serviceRef={ref}
    />
  );
}
