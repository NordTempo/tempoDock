import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useMemo } from "react";
import { queries, servicePath, useService } from "../data/queries";
import type { ServiceRef, ServiceTraffic } from "../data/types";
import { RangeSwitch, Stat } from "../features/stats/parts";
import { BarChart } from "../features/traffic/BarChart";
import {
  barLabel,
  barMs,
  toBars,
  trafficRangeLabel,
  trafficRangeMs,
  trafficRanges,
  validateTrafficRange,
} from "../features/traffic/series";
import { useOrg } from "../shell/location";
import { bytes, size } from "../ui/format";
import { Panel, SectionHead } from "../ui/kit";

// What a service's domains served (ADR 0033): totals, requests over time
// and the most requested endpoints. Visitors are Dock Analytics' (the
// Analytics tab, ADR 0040).
// /shop/production/web/traffic?range=7d
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/requests/",
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
          to="/$org/$project/$environment/$service/settings/domains"
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
        <Chart traffic={traffic} to={dataUpdatedAt} />
      </section>
      <section aria-labelledby="endpoints">
        <SectionHead id="endpoints">
          Top {traffic.paths.length || 20} endpoints
        </SectionHead>
        <Endpoints traffic={traffic} serviceRef={ref} />
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
            <span className={t.status_4xx ? "text-warn" : undefined}>
              {count(t.status_4xx)}
            </span>
            <span className="text-sm font-normal text-mut"> 4xx · </span>
            <span className={t.status_5xx ? "text-fail" : undefined}>
              {count(t.status_5xx)}
            </span>
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

function Chart({ traffic, to }: { traffic: ServiceTraffic; to: number }) {
  const range = traffic.range;
  const from = to - trafficRangeMs[range];
  const bars = useMemo(
    () => toBars(traffic.points, barMs[range]),
    [traffic.points, range],
  );
  return (
    <BarChart
      title="Requests"
      per={barLabel[range]}
      bars={bars}
      from={from - (from % barMs[range])}
      to={to}
      bucketMs={barMs[range]}
    />
  );
}

/**
 * The most requested endpoints: their share, 5xx count and p95 (from
 * Caddy's access log; 0 is unknown, rows before the timings were kept),
 * and the way to their traces (ADR 0075).
 */
function Endpoints({
  traffic,
  serviceRef: ref,
}: {
  traffic: ServiceTraffic;
  serviceRef: ServiceRef;
}) {
  const org = useOrg();
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
            <th className="text-right">5xx</th>
            <th className="text-right">p95</th>
            <th className="w-1/5" />
            <th>
              <span className="sr-only">Traces</span>
            </th>
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
              <td
                className={`text-right font-mono ${p.errors ? "text-fail" : "text-faint"}`}
              >
                {count(p.errors)}
              </td>
              <td className="text-right font-mono">
                {p.p95_ms ? (
                  `${Math.round(p.p95_ms)} ms`
                ) : (
                  <span className="text-faint" title="Not measured yet">
                    ·
                  </span>
                )}
              </td>
              <td className="align-middle">
                <div
                  className="h-1.5 rounded-xs bg-info/55"
                  style={{ width: `${(p.requests / most) * 100}%` }}
                />
              </td>
              <td className="text-right">
                <Link
                  to="/$org/$project/$environment/$service/requests/traces"
                  params={{ org, ...ref }}
                  search={{ route: p.path }}
                  className="btn sm"
                >
                  Traces
                </Link>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Panel>
  );
}
