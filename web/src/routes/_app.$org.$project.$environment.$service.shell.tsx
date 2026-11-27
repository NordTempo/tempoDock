import { createFileRoute } from "@tanstack/react-router";
import { servicePath } from "../data/queries";
import { ShellPanel } from "../features/shell/ShellPanel";

// A service's Shell tab (ADR 0108): a terminal in its running container.
// xterm.js comes with this route's chunk only.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/shell",
)({
  component: ShellTab,
});

function ShellTab() {
  return <ShellPanel serviceRef={servicePath(Route.useParams())} />;
}
