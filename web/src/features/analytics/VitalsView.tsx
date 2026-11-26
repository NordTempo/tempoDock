import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { ErrorBlock } from "../../ui/ErrorBlock";
import { Panel } from "../../ui/kit";
import { LineChart } from "../stats/LineChart";
import {
  type Filter,
  type SiteVitals,
  type StatsRange,
  siteVitalsQuery,
  type Vital,
  type VitalStat,
  vitalDimensions,
  vitals,
} from "./data";
import { Labeled } from "./icons";
import { chartMarkers, count, shown } from "./parts";

// A site's Web Vitals (ADR 0037, phase A2; Saile's pick: B). All five at
// p75 in a row of tiles; the chosen one per day against its thresholds
// and the deploys, then pages, devices, browsers and countries for it.
// Device is a switch of its own: the numbers differ most by it.

// The full name is what the tiles show; the abbreviation and what the
// vital measures are the tooltip.
const names: Record<Vital, [short: string, long: string, means: string]> = {
  lcp: ["LCP", "Largest Contentful Paint", "when the main content shows"],
  inp: [
    "INP",
    "Interaction to Next Paint",
    "how fast the page answers a click or key",
  ],
  cls: [
    "CLS",
    "Cumulative Layout Shift",
    "how much the layout jumps while loading",
  ],
  fcp: ["FCP", "First Contentful Paint", "when anything first shows"],
  ttfb: ["TTFB", "Time to First Byte", "how fast the server answers"],
};

/** A vital's value as people read it: "180ms", "2.9s", "0.04". */
function value(metric: Vital, v: number) {
  if (v < 0) return "–";
  if (metric === "cls") return v.toFixed(2);
  return v < 1000 ? `${Math.round(v)}ms` : `${(v / 1000).toFixed(1)}s`;
}

/**
 * A chart's values in one unit for the whole axis: seconds once its top
 * passes one, so "800ms" and "1.2s" don't stand on the same axis.
 */
function axisValue(metric: Vital, v: number, top: number) {
  if (metric === "cls" || top < 1000) return value(metric, v);
  return `${(v / 1000).toFixed(1)}s`;
}

type Rating = "good" | "fair" | "poor";
const glyph: Record<Rating, [string, string]> = {
  good: ["●", "text-ok"],
  fair: ["▲", "text-warn"],
  poor: ["■", "text-fail"],
};

/** Where a p75 falls against the vital's thresholds. */
function rate(data: SiteVitals, metric: Vital, v: number): Rating | undefined {
  const t = data.thresholds[metric];
  if (v < 0 || !t) return undefined;
  return v <= t.good ? "good" : v > t.poor ? "poor" : "fair";
}

function Rated({
  data,
  metric,
  v,
}: {
  data: SiteVitals;
  metric: Vital;
  v: number;
}) {
  const r = rate(data, metric, v);
  return (
    <span className="whitespace-nowrap">
      {r && <span className={glyph[r][1]}>{glyph[r][0]} </span>}
      {value(metric, v)}
    </span>
  );
}

const devices = [
  ["All devices", undefined],
  ["Desktop", "desktop"],
  ["Mobile", "mobile"],
  ["Tablet", "tablet"],
] as const;

export function VitalsView({
  project,
  site,
  range,
  filters,
  metric,
  device,
  onMetric,
  onDevice,
}: {
  project: string;
  site: string;
  range: StatsRange;
  filters: Filter[];
  metric: Vital;
  device?: string;
  onMetric: (m: Vital) => void;
  onDevice: (d: string | undefined) => void;
}) {
  const usable = filters.filter(
    (f) =>
      (vitalDimensions as readonly string[]).includes(f.dimension) &&
      f.dimension !== "device",
  );
  const skipped = filters.filter(
    (f) => !(vitalDimensions as readonly string[]).includes(f.dimension),
  );
  const query = device
    ? [...usable, { dimension: "device", op: "is" as const, value: device }]
    : usable;
  const { data, error, refetch } = useQuery(
    siteVitalsQuery(project, site, range, query, metric),
  );
  if (error) return <ErrorBlock error={error} onRetry={() => refetch()} />;
  if (!data) return <p className="text-mut">Loading…</p>;
  const t = data.thresholds[metric];
  const step = data.bucket === "hour" ? 3_600_000 : 86_400_000;
  const series = (data.series ?? []).map((p) => ({ t: p.t, v: p.p75 }));
  const top = Math.max(t?.poor ?? 0, ...series.map((p) => p.v)) * 1.15;
  return (
    <div className="flex flex-col gap-4">
      {skipped.length > 0 && (
        <p className="text-xs text-mut">
          Page loads' vitals don't record{" "}
          {skipped.map((f) => f.dimension.replace("_", " ")).join(", ")}; those
          filters are left out here.
        </p>
      )}
      {!data.collecting && (
        <p className="text-warn-text">
          This site doesn't collect Web Vitals. Turn on "Errors and Web Vitals"
          in its settings.
        </p>
      )}
      {!data.store_online && (
        <p className="text-warn-text">{data.store_error}</p>
      )}
      <div className="flex flex-wrap items-center gap-3 text-xs">
        <span className="seg">
          {devices.map(([label, d]) => (
            <button
              key={label}
              type="button"
              aria-pressed={device === d}
              onClick={() => onDevice(d)}
            >
              {label}
            </button>
          ))}
        </span>
        {/* The legend; the value shown is the 75th percentile (p75),
            said as "3 in 4 loads", with the term in its tooltip; what
            each vital measures is the tiles' and the link's. */}
        <span className="flex flex-wrap items-center gap-x-3 text-faint">
          <span>
            <span className="text-ok">●</span> good
          </span>
          <span>
            <span className="text-warn">▲</span> needs improvement
          </span>
          <span>
            <span className="text-fail">■</span> poor
          </span>
          <span title="The 75th percentile (p75): a quarter of loads were slower">
            3 in 4 of {count(data.all.loads)} page loads this fast or faster
          </span>
          <a
            href="https://web.dev/articles/vitals"
            target="_blank"
            rel="noreferrer"
            className="text-info"
          >
            What they measure ↗
          </a>
        </span>
      </div>
      <div
        role="tablist"
        aria-label="Vital"
        className="grid grid-cols-[repeat(auto-fit,minmax(150px,1fr))] gap-x-6 gap-y-3 tabular-nums"
      >
        {vitals.map((m) => {
          const good = data.thresholds[m]?.good;
          return (
            <button
              key={m}
              type="button"
              role="tab"
              aria-selected={m === metric}
              title={`${names[m][0]}: ${names[m][2]}`}
              onClick={() => onMetric(m)}
              className={`border-b-2 pb-1 text-left ${m === metric ? "border-fg" : "border-transparent"}`}
            >
              <span
                className={`block text-xs ${m === metric ? "text-fg" : "text-sec"}`}
              >
                {names[m][1]}
              </span>
              <span className="text-lg font-semibold">
                <Rated data={data} metric={m} v={data.all[m].p75} />
              </span>
              {good !== undefined && (
                <span className="block text-2xs text-mut">
                  good at {value(m, good)} or less
                </span>
              )}
            </button>
          );
        })}
      </div>
      <LineChart
        title={`${names[metric][1]} per ${data.bucket}, 3 in 4 loads this fast`}
        points={series}
        from={data.from - (data.from % step)}
        to={data.to}
        resolution={step / 1000}
        max={top || 1}
        format={(v) => axisValue(metric, v, top)}
        markers={chartMarkers(data.markers)}
        guides={
          t
            ? [
                {
                  v: t.good,
                  label: `${value(metric, t.good)} good`,
                  tone: "ok",
                },
                {
                  v: t.poor,
                  label: `${value(metric, t.poor)} poor`,
                  tone: "fail",
                },
              ]
            : undefined
        }
        empty="No page loads measured in this range."
        roomy
      />
      <div className="grid gap-x-6 gap-y-6 lg:grid-cols-2">
        <Breakdown
          data={data}
          metric={metric}
          title="Pages"
          tabs={[["Pages", "page"]]}
          sortable
        />
        <Breakdown
          data={data}
          metric={metric}
          title="Devices"
          tabs={[["Devices", "device"]]}
        />
        <Breakdown
          data={data}
          metric={metric}
          title="Browsers"
          tabs={[
            ["Browsers", "browser"],
            ["Countries", "country"],
          ]}
        />
      </div>
    </div>
  );
}

/** One breakdown for the chosen vital: loads, p75 and its split. */
function Breakdown({
  data,
  metric,
  title,
  tabs,
  sortable,
}: {
  data: SiteVitals;
  metric: Vital;
  title: string;
  tabs: [label: string, dimension: string][];
  /** Offers worst first besides most loads first. */
  sortable?: boolean;
}) {
  const [tab, setTab] = useState(0);
  const [worst, setWorst] = useState(true);
  const dimension = tabs[tab]?.[1] ?? "page";
  const rows = [...(data.breakdowns[dimension] ?? [])].filter(
    (r) => r[metric].samples > 0,
  );
  if (sortable && worst) rows.sort((a, b) => b[metric].p75 - a[metric].p75);
  return (
    <section aria-label={title} className="flex min-w-0 flex-col gap-1.5">
      <div className="flex flex-wrap items-baseline gap-x-3">
        {tabs.length > 1 ? (
          tabs.map(([label], i) => (
            <button
              key={label}
              type="button"
              aria-pressed={i === tab}
              className={`text-sm ${i === tab ? "font-semibold text-fg" : "text-mut"}`}
              onClick={() => setTab(i)}
            >
              {label}
            </button>
          ))
        ) : (
          <h3 className="text-sm font-semibold">{title}</h3>
        )}
        {sortable && (
          <span className="seg">
            {(["Worst", "Most loads"] as const).map((label) => (
              <button
                key={label}
                type="button"
                aria-pressed={(label === "Worst") === worst}
                onClick={() => setWorst(label === "Worst")}
              >
                {label}
              </button>
            ))}
          </span>
        )}
      </div>
      <Panel>
        <table className="tabular-nums">
          <thead>
            <tr>
              <th className="sr-only">{tabs[tab]?.[0]}</th>
              <th className="w-16 text-right">Loads</th>
              <th
                className="w-20 text-right"
                title="3 in 4 loads were this fast or faster: the 75th percentile (p75)"
              >
                3 in 4
              </th>
              <th className="w-28">Split</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.value}>
                <td className="max-w-0 truncate" title={r.value}>
                  <Labeled dimension={dimension} value={r.value}>
                    {shown(dimension, r)}
                  </Labeled>
                </td>
                <td className="text-right text-mut">{count(r.loads)}</td>
                <td className="text-right">
                  <Rated data={data} metric={metric} v={r[metric].p75} />
                </td>
                <td>
                  <Split stat={r[metric]} />
                </td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr>
                <td colSpan={4} className="text-mut">
                  None in this range.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Panel>
    </section>
  );
}

/** Good, needs improvement and poor shares of a vital's page loads. */
function Split({ stat }: { stat: VitalStat }) {
  if (!stat.samples) return null;
  const good = (stat.good / stat.samples) * 100;
  const poor = (stat.poor / stat.samples) * 100;
  // The bar, and the good share in words: not colour alone.
  return (
    <span
      className="flex items-center gap-2"
      title={`${Math.round(good)}% good · ${Math.round(100 - good - poor)}% needs improvement · ${Math.round(poor)}% poor`}
    >
      <span className="flex h-1.5 w-24 overflow-hidden rounded-full bg-line">
        <span className="bg-ok" style={{ width: `${good}%` }} />
        <span className="bg-warn" style={{ width: `${100 - good - poor}%` }} />
        <span className="bg-fail" style={{ width: `${poor}%` }} />
      </span>
      <span className="text-2xs text-mut">{Math.round(good)}% good</span>
    </span>
  );
}
