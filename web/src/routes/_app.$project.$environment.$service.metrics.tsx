import { createFileRoute } from "@tanstack/react-router";
import { servicePath, useService } from "../data/queries";
import { formatSetting } from "../features/settings/settings";
import { Heading } from "../ui/kit";

// The daemon doesn't collect usage yet; show the limits that do exist.
export const Route = createFileRoute(
  "/_app/$project/$environment/$service/metrics",
)({
  component: MetricsTab,
});

function MetricsTab() {
  const { service } = useService(servicePath(Route.useParams()));
  return (
    <div className="px-5">
      <Heading>Metrics</Heading>
      <p className="text-dim">CPU and memory usage are not collected yet.</p>
      <dl className="grid max-w-[520px] grid-cols-[180px_1fr] gap-y-1.5 pt-3 font-mono text-xs">
        <dt className="font-sans text-sm text-dim">CPU limit</dt>
        <dd>{formatSetting("cpus", service.cpus)}</dd>
        <dt className="font-sans text-sm text-dim">Memory limit</dt>
        <dd>{formatSetting("memory_mb", service.memory_mb)}</dd>
      </dl>
    </div>
  );
}
