import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useMemo } from "react";
import { queries, servicePath, useService } from "../data/queries";
import type { Service, ServiceRef, StatsRange } from "../data/types";
import { AppMetricsPanel } from "../features/appmetrics/AppMetricsPanel";
import { appMetricsQuery } from "../features/appmetrics/data";
import { LineChart } from "../features/stats/LineChart";
import { useLiveService } from "../features/stats/live";
import { LiveNote, RangeSwitch, Stat } from "../features/stats/parts";
import {
  type Point,
  rangeMs,
  ranges,
  validateRange,
} from "../features/stats/series";
import { uptimeChecks } from "../features/uptime/data";
import { useOrg } from "../shell/location";
import { cpus, pct, plural, size } from "../ui/format";
import { SectionHead } from "../ui/kit";

const MiB = 1024 * 1024;

// A service's Metrics tab (ADR 0076): CPU and memory, live while open,
// and history across releases; its app metrics' pinned charts (ADR
// 0075); then, where it has a domain or checks, its uptime as one result
// line (ADR 0055). The checks and the health check are set up in
// Settings › Health and uptime, next to each other (ADR 0099).
// /shop/production/web/metrics?range=24h, #uptime for the line.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/metrics",
)({
  validateSearch: validateRange,
  loaderDeps: ({ search }) => ({ range: search.range ?? "1h" }),
  loader: ({ context: { queryClient }, params, deps }) => {
    // App metrics load beside the rest, but don't hold the tab up.
    void queryClient.prefetchQuery(
      appMetricsQuery(servicePath(params), deps.range),
    );
    return Promise.all([
      queryClient.ensureQueryData(
        queries.serviceStats(servicePath(params), deps.range),
      ),
      queryClient.ensureQueryData(uptimeChecks(servicePath(params))),
    ]);
  },
  component: MetricsTab,
});

function MetricsTab() {
  const ref = servicePath(Route.useParams());
  const range = Route.useSearch().range ?? "1h";
  const navigate = Route.useNavigate();
  const view = useService(ref);
  const { service } = view;
  const live = useLiveService(ref);
  // Uptime checks reach a service through its domains (or a URL someone
  // added); a worker has none.
  const checked = view.domains.length > 0 || !!view.uptime;

  return (
    <>
      <section aria-label="Now" className="flex flex-col gap-2">
        <Now service={service} />
        {/* Live next to live numbers (or when updates stop); without a
            sample, why there is none. */}
        {service.usage ||
        (service.status.state === "running" && live !== "live") ? (
          <LiveNote state={live} />
        ) : (
          <span className="text-xs text-mut">
            {service.status.state === "running"
              ? "Waiting for the first sample."
              : "No running container, so no live numbers."}
          </span>
        )}
      </section>
      <section aria-labelledby="history">
        <SectionHead
          id="history"
          right={
            <RangeSwitch
              value={range}
              options={ranges}
              onChange={(r) =>
                navigate({
                  search: r === "1h" ? {} : { range: r },
                  replace: true,
                })
              }
            />
          }
        >
          History across releases
        </SectionHead>
        <History serviceRef={ref} range={range} service={service} />
      </section>
      <AppMetricsPanel serviceRef={ref} range={range} />
      {checked && <UptimeLine serviceRef={ref} />}
    </>
  );
}

/**
 * Uptime as a result: up or down, the last day's share and how many
 * checks, with the way to where they are set up (Settings › Health and
 * uptime). Without checks, the way to add one.
 */
function UptimeLine({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const org = useOrg();
  const { uptime } = useService(ref);
  const checks = useSuspenseQuery(uptimeChecks(ref)).data;
  const word = { up: "up", down: "down", unknown: "first check pending" };
  const tone = { up: "text-ok", down: "text-fail", unknown: "text-mut" };
  return (
    <section id="uptime" aria-labelledby="uptime-head">
      <SectionHead id="uptime-head">Uptime</SectionHead>
      <p className="flex flex-wrap items-center gap-x-1.5 border-y border-line py-2.5">
        {uptime && checks.length > 0 ? (
          <>
            <span className={tone[uptime.state]}>{word[uptime.state]}</span>
            {uptime.uptime_24h !== undefined && (
              <span className="tabular-nums">
                · {Math.min(uptime.uptime_24h, 99.99).toFixed(2)}% in 24h
              </span>
            )}
            <span className="text-mut">· {plural(checks.length, "check")}</span>
          </>
        ) : (
          <span className="text-mut">No uptime checks yet.</span>
        )}
        <Link
          to="/$org/$project/$environment/$service/settings"
          params={{ org, ...ref }}
          hash="health"
          className="ml-auto inline-flex min-h-[24px] items-center underline decoration-faint underline-offset-2 hover:decoration-current"
        >
          {checks.length > 0 ? "Set up in" : "Add one in"} Settings › Health and
          uptime
        </Link>
      </p>
    </section>
  );
}

/**
 * The latest sample against the limits; without one, the limits with a
 * dash for each number, so the row keeps its place.
 */
function Now({ service: s }: { service: Service }) {
  const u = s.usage;
  const none = <span className="text-faint">-</span>;
  return (
    <div className="flex flex-wrap gap-x-10 gap-y-3">
      <Stat
        label="CPU"
        value={
          u
            ? pct(
                s.effective_cpus
                  ? u.cpu_percent / s.effective_cpus
                  : u.cpu_percent,
              )
            : none
        }
        of={
          s.effective_cpus
            ? `of ${cpus(s.effective_cpus)}${s.cpus_from_default ? " default" : ""}`
            : "of a core"
        }
      />
      <Stat
        label="Memory"
        value={u ? size(u.memory_bytes) : none}
        of={
          s.effective_memory_mb
            ? `/ ${size(s.effective_memory_mb * MiB)}${s.memory_from_default ? " default" : ""}`
            : undefined
        }
      />
      <Stat
        label="Disk read"
        value={u ? `${size(u.io_read_bytes_per_sec)}/s` : none}
      />
      <Stat
        label="Disk write"
        value={u ? `${size(u.io_write_bytes_per_sec)}/s` : none}
      />
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
  // Usage events append each new minute point (data/live.ts).
  const { data: stats, dataUpdatedAt } = useSuspenseQuery(
    queries.serviceStats(ref, range),
  );
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
  // No points yet: one line, not two empty charts above the uptime checks.
  if (stats.points.length === 0)
    return (
      <p className="text-mut">
        No history yet: a point is recorded every minute while it runs.
      </p>
    );
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
