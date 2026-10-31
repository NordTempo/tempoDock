import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useMemo } from "react";
import { queries, servicePath, useService } from "../data/queries";
import type { ServiceTraffic } from "../data/types";
import { LineChart } from "../features/stats/LineChart";
import { RangeSwitch, Stat } from "../features/stats/parts";
import { BarChart } from "../features/traffic/BarChart";
import {
  barLabel,
  barMs,
  niceMax,
  toBars,
  trafficRangeLabel,
  trafficRangeMs,
  trafficRanges,
  validateTrafficRange,
} from "../features/traffic/series";
import { useOrg } from "../shell/location";
import { bytes, size } from "../ui/format";
import { Panel, SectionHead } from "../ui/kit";

// What a service's domains served (ADR 0033): totals, requests and their
// average time over time, and the most requested endpoints. Visitors are
// Dock Analytics' (the Analytics tab).
// /shop/production/web/traffic?range=7d
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/traffic",
)({
  validateSearch: validateTrafficRange,
  loaderDeps: ({ search }) => ({ range: search.range ?? "24h" }),
  loader: ({ context: { queryClient }, params, deps }) =>
    queryClient.ensureQueryData(
      queries.serviceTraffic(servicePath(params), deps.range),
    ),
  component: TrafficTab,
});

const count = (n: number) => n.toLocaleString("en-US");

function TrafficTab() {
  const org = useOrg();
  const ref = servicePath(Route.useParams());
  const range = Route.useSearch().range ?? "24h";
  const navigate = Route.useNavigate();
  const { domains } = useService(ref);
  // Traffic events append each new minute to the 24h view (data/live.ts).
  const { data: traffic, dataUpdatedAt } = useSuspenseQuery(
    queries.serviceTraffic(ref, range),
  );
  const hosts = domains.filter((d) => !d.redirect_to).map((d) => d.hostname);

  if (!hosts.length && !traffic.totals.requests) {
    return (
      <p className="text-mut">
        No domain yet. Requests are counted for a service's domains; add one on{" "}
        <Link
          to="/$org/$project/$environment/$service/domains"
          params={{ org, ...ref }}
        >
          Domains
        </Link>
        .
      </p>
    );
  }
  return (
    <>
      <section aria-labelledby="traffic" className="flex flex-col gap-5">
        <SectionHead
          id="traffic"
          aside={hosts.join(", ")}
          right={
            <RangeSwitch
              value={range}
              options={trafficRanges}
              onChange={(r) =>
                navigate({
                  search: r === "24h" ? {} : { range: r },
                  replace: true,
                })
              }
            />
          }
        >
          {trafficRangeLabel[range]}
        </SectionHead>
        <Totals traffic={traffic} />
        <Charts traffic={traffic} to={dataUpdatedAt} />
      </section>
      <section aria-labelledby="endpoints">
        <SectionHead
          id="endpoints"
          aside={`top ${traffic.paths.length || 20}, IDs as :id`}
        >
          Endpoints
        </SectionHead>
        <Endpoints traffic={traffic} />
      </section>
    </>
  );
}

function Totals({ traffic: { totals: t } }: { traffic: ServiceTraffic }) {
  return (
    <div className="flex flex-wrap gap-x-10 gap-y-3 tabular-nums">
      <Stat label="Requests" value={count(t.requests)} />
      <Stat
        label="Errors"
        value={
          <>
            <span className="text-warn">{count(t.status_4xx)}</span>
            <span className="text-sm font-normal text-mut"> 4xx </span>
            <span className="text-fail">{count(t.status_5xx)}</span>
            <span className="text-sm font-normal text-mut"> 5xx</span>
          </>
        }
      />
      <Stat label="Avg time" value={Math.round(t.avg_duration_ms)} of="ms" />
      <Stat
        label="Sent"
        value={t.bytes < 1 << 20 ? size(t.bytes) : bytes(t.bytes)}
      />
    </div>
  );
}

function Charts({ traffic, to }: { traffic: ServiceTraffic; to: number }) {
  const range = traffic.range;
  const from = to - trafficRangeMs[range];
  const bars = useMemo(
    () => toBars(traffic.points, barMs[range]),
    [traffic.points, range],
  );
  // Minutes and hours without requests have no point, so the line breaks
  // there instead of dropping to 0 ms.
  const avg = useMemo(
    () =>
      traffic.points.map((p) => ({
        t: Date.parse(p.time),
        v: p.avg_duration_ms,
      })),
    [traffic.points],
  );
  const per = traffic.resolution_seconds === 60 ? "minute" : "hour";
  return (
    <div className="grid gap-x-8 gap-y-5 lg:grid-cols-2">
      <BarChart
        title="Requests"
        per={barLabel[range]}
        bars={bars}
        from={from - (from % barMs[range])}
        to={to}
        bucketMs={barMs[range]}
      />
      <LineChart
        title={`Avg time per ${per}`}
        points={avg}
        from={from}
        to={to}
        resolution={traffic.resolution_seconds}
        max={niceMax(Math.max(1, ...avg.map((p) => p.v)))}
        format={(v) => `${Math.round(v)} ms`}
      />
    </div>
  );
}

function Endpoints({ traffic }: { traffic: ServiceTraffic }) {
  const { paths, totals } = traffic;
  if (!paths.length) {
    return <p className="text-mut">No requests in this range.</p>;
  }
  const most = paths[0]?.requests || 1;
  const share = (n: number) =>
    n * 100 < totals.requests
      ? "<1%"
      : `${Math.round((n * 100) / Math.max(1, totals.requests))}%`;
  return (
    <Panel>
      <table className="tabular-nums">
        <thead>
          <tr>
            <th>Method</th>
            <th>Path</th>
            <th className="text-right">Requests</th>
            <th className="text-right">Share</th>
            <th className="w-1/3" />
          </tr>
        </thead>
        <tbody>
          {paths.map((p) => (
            <tr key={`${p.method} ${p.path}`}>
              <td className="font-mono text-mut">{p.method}</td>
              <td className="max-w-md truncate font-mono" title={p.path}>
                {p.path}
              </td>
              <td className="text-right">{count(p.requests)}</td>
              <td className="text-right text-mut">{share(p.requests)}</td>
              <td className="align-middle">
                <div
                  className="h-1.5 rounded-xs bg-info/55"
                  style={{ width: `${(p.requests / most) * 100}%` }}
                />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Panel>
  );
}
