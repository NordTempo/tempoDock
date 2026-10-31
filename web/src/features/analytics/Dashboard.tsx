import { useQuery } from "@tanstack/react-query";
import { lazy, Suspense, useMemo, useState } from "react";
import { clock, day } from "../../ui/format";
import { Button, Panel } from "../../ui/kit";
import { LineChart } from "../stats/LineChart";
import { RangeSwitch } from "../stats/parts";
import { niceMax } from "../traffic/series";
import {
  type AnalyticsRow,
  type Filter,
  type Site,
  type SiteStats,
  type StatsRange,
  siteStatsQuery,
  statsRanges,
  useBreakdown,
  useSiteLive,
} from "./data";

// A site's dashboard (ADR 0037, mock C): the numbers against the period
// before, the chart with deploys and domain moves, the breakdowns, and a
// live column. Clicking a value filters by it.

const SiteSettingsDialog = lazy(() => import("./SiteSettingsDialog"));

const count = (n: number) => n.toLocaleString("en-US");

const regions = new Intl.DisplayNames(["en"], { type: "region" });
/** A country's name from its ISO code ("NO": Norway). */
const country = (code: string) => {
  try {
    return regions.of(code) ?? code;
  } catch {
    return code;
  }
};

/** How a dimension's value reads; services and releases come labelled. */
const shown = (dimension: string, row: { value: string; label?: string }) =>
  row.label ||
  (row.value === ""
    ? dimension === "referrer" || dimension === "channel"
      ? "Direct"
      : "(none)"
    : dimension === "country"
      ? country(row.value)
      : row.value);

export function Dashboard({
  project,
  sites,
  site,
  range,
  filters,
  onSite,
  onRenamed,
  onRange,
  onFilters,
}: {
  project: string;
  /** The sites to choose from: those the service serves. */
  sites: Site[];
  site: Site;
  range: StatsRange;
  filters: Filter[];
  onSite: (name: string) => void;
  /** The site was renamed: keep showing it, filters and all. */
  onRenamed: (name: string) => void;
  onRange: (range: StatsRange) => void;
  onFilters: (filters: Filter[]) => void;
}) {
  const { data: stats, error } = useQuery(
    siteStatsQuery(project, site.name, range, filters),
  );
  const [settings, setSettings] = useState(false);
  // Labels of filtered values (services, releases), from the rows clicked;
  // a filter from the URL finds its label in the dashboard's rows.
  const [labels, setLabels] = useState<Record<string, string>>({});
  const addFilter = (dimension: string, row: AnalyticsRow) => {
    if (row.label)
      setLabels((l) => ({
        ...l,
        [`${dimension}:${row.value}`]: row.label ?? "",
      }));
    onFilters([
      ...filters.filter((f) => f.dimension !== dimension),
      { dimension, op: "is", value: row.value },
    ]);
  };
  const hostnames = site.hostnames ?? [];
  const services = new Set(hostnames.map((h) => h.service).filter(Boolean));

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        {sites.length > 1 ? (
          <select
            aria-label="Site"
            value={site.name}
            onChange={(e) => onSite(e.target.value)}
          >
            {sites.map((s) => (
              <option key={s.name} value={s.name}>
                {s.name}
              </option>
            ))}
          </select>
        ) : (
          <span className="font-semibold">{site.name}</span>
        )}
        <span className="text-xs text-faint">
          site · {hostnames.length}{" "}
          {hostnames.length === 1 ? "hostname" : "hostnames"} · {services.size}{" "}
          {services.size === 1 ? "service" : "services"}
        </span>
        <Button onClick={() => setSettings(true)}>Settings</Button>
        <span className="ml-auto">
          <RangeSwitch value={range} options={statsRanges} onChange={onRange} />
        </span>
      </div>
      {filters.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          {filters.map((f) => (
            <button
              key={`${f.dimension}:${f.op}:${f.value}`}
              type="button"
              className="rounded-ctl bg-chip px-2 py-0.5 text-xs"
              title="Remove this filter"
              onClick={() => onFilters(filters.filter((g) => g !== f))}
            >
              <span className="text-mut">{f.dimension.replace("_", " ")}</span>{" "}
              {f.op.replace("_", " ")}{" "}
              {shown(f.dimension, {
                value: f.value,
                label:
                  labels[`${f.dimension}:${f.value}`] ??
                  stats?.breakdowns[f.dimension]?.find(
                    (r) => r.value === f.value,
                  )?.label,
              })}{" "}
              <span className="text-faint">✕</span>
            </button>
          ))}
          <button
            type="button"
            className="text-xs text-mut"
            onClick={() => onFilters([])}
          >
            Clear
          </button>
        </div>
      )}
      {error && <p className="text-fail">{error.message}</p>}
      {stats && !stats.store_online && (
        <p className="text-warn-text">{stats.store_error}</p>
      )}
      {stats?.notice && <p className="text-mut">{stats.notice}</p>}
      <div className="grid gap-x-8 gap-y-6 xl:grid-cols-[minmax(0,1fr)_300px]">
        <div className="flex min-w-0 flex-col gap-6">
          {stats ? (
            <Main
              project={project}
              site={site.name}
              stats={stats}
              range={range}
              filters={filters}
              onPick={addFilter}
            />
          ) : (
            !error && <p className="text-mut">Loading…</p>
          )}
        </div>
        <aside className="flex flex-col gap-6">
          <Live project={project} site={site.name} />
          {stats && <Markers stats={stats} />}
        </aside>
      </div>
      <p className="text-xs text-faint">
        Locations:{" "}
        <a href="https://db-ip.com" target="_blank" rel="noreferrer">
          IP Geolocation by DB-IP
        </a>{" "}
        (CC BY 4.0). Cookieless: a visitor is counted once per day.
      </p>
      {settings && (
        <Suspense fallback={null}>
          <SiteSettingsDialog
            project={project}
            site={site}
            onRenamed={onRenamed}
            onClose={() => setSettings(false)}
          />
        </Suspense>
      )}
    </div>
  );
}

type Metric = "visitors" | "pageviews";

function Main({
  project,
  site,
  stats,
  range,
  filters,
  onPick,
}: {
  project: string;
  site: string;
  stats: SiteStats;
  range: StatsRange;
  filters: Filter[];
  onPick: (dimension: string, row: AnalyticsRow) => void;
}) {
  const [metric, setMetric] = useState<Metric>("visitors");
  const o = stats.overview;
  const p = stats.previous;
  const panel = { project, site, stats, range, filters, onPick };
  return (
    <>
      <dl className="grid grid-cols-[repeat(auto-fit,minmax(130px,1fr))] gap-x-6 gap-y-3 tabular-nums">
        <Kpi
          label="Visitors"
          value={count(o.visitors)}
          change={delta(o.visitors, p.visitors)}
          on={metric === "visitors"}
          onClick={() => setMetric("visitors")}
        />
        <Kpi
          label="Page views"
          value={count(o.pageviews)}
          change={delta(o.pageviews, p.pageviews)}
          on={metric === "pageviews"}
          onClick={() => setMetric("pageviews")}
        />
        <Kpi
          label="Sessions"
          value={count(o.sessions)}
          change={delta(o.sessions, p.sessions)}
        />
        <Kpi
          label="Bounce rate"
          value={`${Math.round(o.bounce_rate * 100)}%`}
          change={
            o.sessions && p.sessions
              ? points(o.bounce_rate, p.bounce_rate)
              : undefined
          }
        />
        <Kpi
          label="Session length"
          value={duration(o.duration_ms)}
          change={delta(o.duration_ms, p.duration_ms)}
        />
        <Kpi
          label="Events"
          value={count(o.events)}
          change={delta(o.events, p.events)}
        />
      </dl>
      <Chart stats={stats} metric={metric} />
      <div className="grid gap-x-6 gap-y-6 lg:grid-cols-2">
        <Breakdown
          {...panel}
          title="Pages"
          tabs={[
            ["Top", "page"],
            ["Entry", "entry_page"],
            ["Exit", "exit_page"],
          ]}
        />
        <Breakdown
          {...panel}
          title="Sources"
          tabs={[
            ["Referrers", "referrer"],
            ["Channels", "channel"],
            ["Campaigns", "utm_campaign"],
          ]}
        />
        <Breakdown
          {...panel}
          title="Locations"
          tabs={[
            ["Countries", "country"],
            ["Regions", "region"],
            ["Cities", "city"],
          ]}
        />
        <Breakdown
          {...panel}
          title="Devices"
          tabs={[
            ["Browsers", "browser"],
            ["OS", "os"],
            ["Devices", "device"],
          ]}
        />
        <Breakdown
          {...panel}
          title="Events"
          tabs={[
            ["Custom", "event"],
            ["Outbound links", "outbound"],
          ]}
        />
        <Breakdown
          {...panel}
          title="Served by"
          tabs={[
            ["Services", "service"],
            ["Releases", "release"],
            ["Hostnames", "hostname"],
          ]}
        />
      </div>
    </>
  );
}

/** "+12%" against the period before, with a tone; nothing without one. */
function delta(now: number, before: number) {
  if (!before) return undefined;
  const pct = Math.round(((now - before) / before) * 100);
  return { text: `${pct >= 0 ? "+" : "−"}${Math.abs(pct)}%`, up: pct >= 0 };
}

/** A rate's change in percentage points; a lower bounce rate is good. */
function points(now: number, before: number) {
  const pts = Math.round((now - before) * 100);
  return {
    text: `${pts > 0 ? "+" : pts < 0 ? "−" : "±"}${Math.abs(pts)} pts`,
    up: pts <= 0,
  };
}

/** 2m 48s. */
function duration(ms: number) {
  const s = Math.round(ms / 1000);
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}

function Kpi({
  label,
  value,
  change,
  on,
  onClick,
}: {
  label: string;
  value: string;
  change?: { text: string; up: boolean };
  /** The metric the chart shows; set only for those it can show. */
  on?: boolean;
  onClick?: () => void;
}) {
  const content = (
    <>
      <dt className={`text-xs ${on ? "text-fg" : "text-mut"}`}>{label}</dt>
      <dd className="text-lg font-semibold">
        {value}
        {change && (
          <small
            className={`ml-1.5 text-xs font-normal ${change.up ? "text-ok" : "text-fail"}`}
          >
            {change.text}
          </small>
        )}
      </dd>
    </>
  );
  return onClick ? (
    <button
      type="button"
      aria-pressed={on}
      onClick={onClick}
      className={`border-b-2 pb-1 text-left ${on ? "border-fg" : "border-transparent"}`}
    >
      {content}
    </button>
  ) : (
    <div className="border-b-2 border-transparent pb-1">{content}</div>
  );
}

/**
 * The chosen metric per hour or day, zero where nothing happened. The axis
 * starts at the first bucket's start, so the partial first hour or day
 * shows; a range with nothing in it shows the empty text, not a flat line.
 */
function Chart({ stats, metric }: { stats: SiteStats; metric: Metric }) {
  const step = stats.bucket === "hour" ? 3_600_000 : 86_400_000;
  const start = stats.from - (stats.from % step);
  const points = useMemo(() => {
    const series = stats.series ?? [];
    if (!series.length) return [];
    const at = new Map(series.map((p) => [p.t, p[metric]]));
    const out = [];
    for (let t = start; t < stats.to; t += step) {
      out.push({ t, v: at.get(t) ?? 0 });
    }
    return out;
  }, [stats, metric, step, start]);
  const markers = useMemo(
    () =>
      (stats.markers ?? []).map((m) => ({
        t: Date.parse(m.at),
        label: `${m.kind}: ${m.message}`,
      })),
    [stats.markers],
  );
  return (
    <LineChart
      title={`${metric === "visitors" ? "Visitors" : "Page views"} per ${stats.bucket}`}
      points={points}
      from={start}
      to={stats.to}
      resolution={step / 1000}
      max={niceMax(Math.max(1, ...points.map((p) => p.v)))}
      format={(v) => count(Math.round(v))}
      markers={markers}
      empty="Nothing counted in this range."
    />
  );
}

/** What a breakdown's count column counts. */
const counted: Record<string, string> = {
  page: "Views",
  entry_page: "Sessions",
  exit_page: "Sessions",
};

/**
 * A panel of one dimension's top values, with tabs for related ones. The
 * dashboard's own dimensions come with it; the others load when picked.
 */
function Breakdown({
  title,
  tabs,
  project,
  site,
  stats,
  range,
  filters,
  onPick,
}: {
  title: string;
  tabs: [label: string, dimension: string][];
  project: string;
  site: string;
  stats: SiteStats;
  range: StatsRange;
  filters: Filter[];
  onPick: (dimension: string, row: AnalyticsRow) => void;
}) {
  const [tab, setTab] = useState(0);
  const dimension = tabs[tab]?.[1] ?? "page";
  const loaded = stats.breakdowns[dimension];
  // Not while the store is unreachable: the dashboard already says so.
  const fetched = useBreakdown(
    project,
    site,
    range,
    filters,
    dimension,
    !loaded && stats.store_online,
  );
  const rows = loaded ?? fetched.data;
  const most = Math.max(1, ...(rows ?? []).map((r) => r.visitors));
  return (
    <section aria-label={title} className="flex min-w-0 flex-col gap-1.5">
      <div className="flex flex-wrap items-baseline gap-x-3">
        <h3 className="text-sm font-semibold">{title}</h3>
        {tabs.length > 1 &&
          tabs.map(([label], i) => (
            <button
              key={label}
              type="button"
              aria-pressed={i === tab}
              className={`text-xs ${i === tab ? "text-fg" : "text-mut"}`}
              onClick={() => setTab(i)}
            >
              {label}
            </button>
          ))}
      </div>
      <Panel>
        <table className="tabular-nums">
          <thead>
            <tr>
              <th className="sr-only">{tabs[tab]?.[0]}</th>
              <th className="w-20 text-right">Visitors</th>
              <th className="w-20 text-right">
                {counted[dimension] ?? "Events"}
              </th>
            </tr>
          </thead>
          <tbody>
            {rows?.map((r) => (
              <tr key={r.value}>
                <td className="relative max-w-0 truncate">
                  <span
                    className="absolute inset-y-1 left-0 rounded-xs bg-info/8"
                    style={{ width: `${(r.visitors / most) * 100}%` }}
                  />
                  <button
                    type="button"
                    className="relative truncate text-left"
                    title={`Filter by ${shown(dimension, r)}`}
                    onClick={() => onPick(dimension, r)}
                  >
                    {shown(dimension, r)}
                  </button>
                </td>
                <td className="text-right">{count(r.visitors)}</td>
                <td className="text-right text-mut">{count(r.count)}</td>
              </tr>
            ))}
            {rows?.length === 0 && (
              <tr>
                <td colSpan={3} className="text-mut">
                  None in this range.
                </td>
              </tr>
            )}
            {!rows && (
              <tr>
                <td colSpan={3} className="text-mut">
                  {fetched.error ? fetched.error.message : "Loading…"}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Panel>
    </section>
  );
}

const kindLabel = {
  pageview: "page",
  event: "event",
  outbound: "link",
} as const;

/** Live visitors and the latest events, over the site's live stream. */
function Live({ project, site }: { project: string; site: string }) {
  const { live, lost, paused } = useSiteLive(project, site);
  return (
    <section aria-labelledby="live" className="flex flex-col gap-2">
      <h3 id="live" className="flex items-baseline gap-2 text-sm font-semibold">
        {live ? count(live.visitors) : "–"} live{" "}
        {live?.visitors === 1 ? "visitor" : "visitors"}
        <span className="text-xs font-normal text-faint">
          {lost ? (
            <span className="text-fail">{lost}</span>
          ) : paused ? (
            "paused while hidden"
          ) : (
            "last 5 min"
          )}
        </span>
      </h3>
      <ol className="flex flex-col text-xs">
        {live?.recent?.map((v, i) => (
          <li
            // Events have no ID; the list is replaced whole on each view.
            key={i}
            className="grid grid-cols-[52px_40px_minmax(0,1fr)] gap-x-2 border-b border-soft py-1"
          >
            <span className="text-faint tabular-nums">{clock(v.at)}</span>
            <span className="text-mut">{kindLabel[v.kind]}</span>
            <span className="truncate" title={v.name || v.path}>
              {v.kind === "pageview" ? v.path : v.name}
              {(v.city || v.country) && (
                <span className="text-faint">
                  {" "}
                  · {v.city || country(v.country ?? "")}
                </span>
              )}
            </span>
          </li>
        ))}
        {live && !live.recent?.length && (
          <li className="text-mut">Nothing in the last five minutes.</li>
        )}
      </ol>
    </section>
  );
}

/** The chart's markers, newest first: deploys and hostname changes. */
function Markers({ stats }: { stats: SiteStats }) {
  if (!stats.markers?.length) return null;
  const markers = [...stats.markers].sort((a, b) => b.at.localeCompare(a.at));
  return (
    <section aria-labelledby="markers" className="flex flex-col gap-2">
      <h3 id="markers" className="text-sm font-semibold">
        On the chart
      </h3>
      <ol className="flex flex-col text-xs">
        {markers.slice(0, 20).map((m) => (
          <li
            key={`${m.at} ${m.message}`}
            className="grid grid-cols-[52px_minmax(0,1fr)_auto] gap-x-2 border-b border-soft py-1"
          >
            <span className="text-mut">{m.kind}</span>
            <span className="truncate" title={m.message}>
              {m.message}
            </span>
            <span className="text-faint">{day(m.at)}</span>
          </li>
        ))}
      </ol>
    </section>
  );
}
