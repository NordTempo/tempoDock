import { createFileRoute } from "@tanstack/react-router";
import { useService } from "../data/queries";
import { mb } from "../ui/format";
import { Bar, Heading } from "../ui/kit";

// Placeholder until stored metrics (M4). Shows the live numbers we do have.
export const Route = createFileRoute("/_app/services/$serviceId/metrics")({
  component: MetricsTab,
});

function MetricsTab() {
  const { serviceId } = Route.useParams();
  const { cpuPercent, memory, settings } = useService(serviceId);
  return (
    <div className="px-5">
      <Heading note="now">Metrics</Heading>
      <dl className="grid max-w-[520px] grid-cols-[180px_1fr] gap-y-1.5 font-mono text-xs">
        <dt className="font-sans text-sm text-dim">CPU</dt>
        <dd>
          {cpuPercent}% of {settings.cpuLimit.toFixed(1)}
          <Bar fraction={cpuPercent / 100} />
        </dd>
        <dt className="font-sans text-sm text-dim">Memory</dt>
        <dd>
          {mb(memory.usedMb)} of {mb(settings.memoryLimitMb)}
          <Bar fraction={memory.usedMb / settings.memoryLimitMb} />
        </dd>
        <dt className="font-sans text-sm text-dim">Memory peak, 24h</dt>
        <dd>{mb(memory.peak24hMb)}</dd>
      </dl>
      <p className="pt-3 text-dim">History arrives with stored metrics.</p>
    </div>
  );
}
