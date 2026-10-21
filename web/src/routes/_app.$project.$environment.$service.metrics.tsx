import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useMemo } from "react";
import { queries, servicePath, useService } from "../data/queries";
import type { Service, ServiceRef, StatsRange } from "../data/types";
import { CpuUsage, MemoryUsage } from "../features/service/Usage";
import { LineChart } from "../features/stats/LineChart";
import { useLiveService } from "../features/stats/live";
import { LiveNote, RangeSwitch, Stat } from "../features/stats/parts";
import {
  type Point,
  rangeLabel,
  rangeMs,
  validateRange,
} from "../features/stats/series";
import { cpus, pct, size } from "../ui/format";
import { SectionHead } from "../ui/kit";

const MiB = 1024 * 1024;

// A service's CPU and memory: live while open, and history across
// releases. /shop/production/web/metrics?range=24h
export const Route = createFileRoute(
  "/_app/$project/$environment/$service/metrics",
)({
  validateSearch: validateRange,
  loaderDeps: ({ search }) => ({ range: search.range ?? "1h" }),
  loader: ({ context: { queryClient }, params, deps }) =>
    queryClient.ensureQueryData(
      queries.serviceStats(servicePath(params), deps.range),
    ),
  component: MetricsTab,
});

function MetricsTab() {
  const ref = servicePath(Route.useParams());
  const range = Route.useSearch().range ?? "1h";
  const navigate = Route.useNavigate();
  const { service } = useService(ref);
  const live = useLiveService(ref);

  return (
    <>
      <section aria-label="Now" className="flex flex-col gap-2">
        <Now service={service} />
        <LiveNote state={live} />
      </section>
      <section aria-labelledby="history">
        <SectionHead
          id="history"
          aside={`${rangeLabel[range]}, across releases`}
          right={
            <RangeSwitch
              value={range}
              onChange={(r) =>
                navigate({
                  search: r === "1h" ? {} : { range: r },
                  replace: true,
                })
              }
            />
          }
        >
          History
        </SectionHead>
        <History serviceRef={ref} range={range} service={service} />
      </section>
    </>
  );
}

function Now({ service: s }: { service: Service }) {
  const u = s.usage;
  if (!u) {
    return (
      <p className="text-mut">
        No running container, so no live numbers. Limits:{" "}
        <MemoryUsage service={s} />, <CpuUsage service={s} />.
      </p>
    );
  }
  return (
    <div className="flex flex-wrap gap-x-10 gap-y-3">
      <Stat
        label="CPU"
        value={pct(
          s.effective_cpus ? u.cpu_percent / s.effective_cpus : u.cpu_percent,
        )}
        of={
          s.effective_cpus
            ? `of ${cpus(s.effective_cpus)}${s.cpus_from_default ? " (default)" : ""}`
            : "of a core"
        }
      />
      <Stat
        label="Memory"
        value={size(u.memory_bytes)}
        of={
          s.effective_memory_mb
            ? `/ ${size(s.effective_memory_mb * MiB)}${s.memory_from_default ? " (default)" : ""}`
            : undefined
        }
      />
      <Stat label="Disk read" value={`${size(u.io_read_bytes_per_sec)}/s`} />
      <Stat label="Disk write" value={`${size(u.io_write_bytes_per_sec)}/s`} />
    </div>
  );
}

function History({
  serviceRef: ref,
  range,
  service: s,
}: {
  serviceRef: ServiceRef;
  range: StatsRange;
  service: Service;
}) {
  const { data: stats, dataUpdatedAt } = useSuspenseQuery({
    ...queries.serviceStats(ref, range),
    // A new point lands every minute; the query pauses with the tab.
    refetchInterval: 60_000,
  });
  // CPU as a share of the limit, so 100% means the limit.
  const share = s.effective_cpus || 1;
  const series = useMemo(() => {
    const of = (pick: (p: (typeof stats.points)[number]) => number): Point[] =>
      stats.points.map((p) => ({ t: Date.parse(p.time), v: pick(p) }));
    return {
      cpu: of((p) => p.cpu_percent / share),
      memory: of((p) => p.memory_bytes),
    };
  }, [stats, share]);
  const to = dataUpdatedAt;
  const common = {
    from: to - rangeMs[range],
    to,
    resolution: stats.resolution_seconds,
  };
  const memoryLimit = s.effective_memory_mb * MiB;
  return (
    <div className="mt-2 grid gap-x-8 gap-y-5 lg:grid-cols-2">
      <LineChart
        {...common}
        title={
          s.effective_cpus
            ? `CPU, share of ${cpus(s.effective_cpus)}`
            : "CPU, share of a core"
        }
        points={series.cpu}
        max={100}
        maxNote={s.effective_cpus ? "limit" : undefined}
        format={pct}
      />
      <LineChart
        {...common}
        title="Memory"
        points={series.memory}
        max={memoryLimit || Math.max(1, ...series.memory.map((p) => p.v)) * 1.2}
        maxNote={memoryLimit ? "limit" : undefined}
        format={size}
      />
    </div>
  );
}
