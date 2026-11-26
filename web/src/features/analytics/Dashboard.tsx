import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { ErrorBlock } from "../../ui/ErrorBlock";
import { clock } from "../../ui/format";
import { LineChart } from "../stats/LineChart";
import { niceMax } from "../traffic/series";
import { BreakdownTable } from "./BreakdownTable";
import {
  type AnalyticsRow,
  type Filter,
  type Site,
  type SiteStats,
  type StatsRange,
  siteStatsQuery,
  useBreakdown,
  useSiteLive,
} from "./data";
import { GoalsPanel } from "./GoalsPanel";
import {
  charted,
  chartMarkers,
  count,
  country,
  delta,
  duration,
  Kpi,
  points,
} from "./parts";

// A site's dashboard (ADR 0037, mock C): the numbers against the period
// before, the chart with deploys and domain moves, the breakdowns, and a
// live column. Clicking a value filters by it. The globe is a view of its
// own (GlobeView).

export function Dashboard({
  project,
  site,
  range,
  filters,
  onPick,
}: {
  project: string;
  site: Site;
  range: StatsRange;
  filters: Filter[];
  /** A value was clicked: filter by it (row carries its label). */
  onPick: (dimension: string, row: AnalyticsRow) => void;
}) {
  const {
    data: stats,
    error,
    refetch,
  } = useQuery(siteStatsQuery(project, site.name, range, filters));
  return (
    <div className="flex flex-col gap-4">
      {error && <ErrorBlock error={error} onRetry={() => refetch()} />}
      {stats && !stats.store_online && (
        <p className="text-warn-text">{stats.store_error}</p>
      )}
      {stats?.notice && <p className="text-mut">{stats.notice}</p>}
      <div className="grid gap-x-8 gap-y-6 min-[1400px]:grid-cols-[minmax(0,1fr)_300px]">
        <div className="flex min-w-0 flex-col gap-6">
          {stats ? (
            <Main
              project={project}
              site={site.name}
              stats={stats}
              range={range}
              filters={filters}
              onPick={onPick}
            />
          ) : (
            !error && <p className="text-mut">Loading…</p>
          )}
        </div>
        <aside className="flex flex-col gap-6">
          <Live project={project} site={site.name} />
        </aside>
      </div>
      <p className="text-xs text-faint">
        Locations:{" "}
        <a href="https://db-ip.com" target="_blank" rel="noreferrer">
          IP Geolocation by DB-IP
        </a>{" "}
        (CC BY 4.0). Cookieless: a visitor is counted once per day.
      </p>
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
  const changes = {
    visitors: delta(o.visitors, p.visitors, range),
    pageviews: delta(o.pageviews, p.pageviews, range),
    sessions: delta(o.sessions, p.sessions, range),
    bounce:
      o.sessions && p.sessions
        ? points(o.bounce_rate, p.bounce_rate, range)
        : undefined,
    duration: delta(o.duration_ms, p.duration_ms, range, duration),
    events: delta(o.events, p.events, range),
  };
  return (
    <>
      {/* Six tiles in one row on wide windows; two rows of three below 1400px. */}
      <div className="grid grid-cols-3 gap-x-4 gap-y-3 tabular-nums min-[1400px]:grid-cols-6">
        <Kpi
          label="Visitors"
          value={count(o.visitors)}
          change={changes.visitors}
          on={metric === "visitors"}
          onClick={() => setMetric("visitors")}
        />
        <Kpi
          label="Page views"
          value={count(o.pageviews)}
          change={changes.pageviews}
          on={metric === "pageviews"}
          onClick={() => setMetric("pageviews")}
        />
        <Kpi
          label="Sessions"
          value={count(o.sessions)}
          change={changes.sessions}
        />
        <Kpi
          label="Bounce rate"
          value={`${Math.round(o.bounce_rate * 100)}%`}
          change={changes.bounce}
        />
        <Kpi
          label="Session length"
          value={duration(o.duration_ms)}
          change={changes.duration}
        />
        <Kpi label="Events" value={count(o.events)} change={changes.events} />
      </div>
      {/* Said once, and only when a tile shows a change. */}
      {Object.values(changes).some(Boolean) && (
        <p className="-mt-2 text-xs text-faint">
          Changes compare with the previous {range}.
        </p>
      )}
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
        {/* Next to Pages: a service's domains told apart (ADR 0103). */}
        <Breakdown
          {...panel}
          title="Domains"
          tabs={[
            ["Domains", "hostname"],
            ["Releases", "release"],
            ["Services", "service"],
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
        <GoalsPanel
          project={project}
          site={site}
          range={range}
          filters={filters}
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
      </div>
    </>
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
  const markers = useMemo(() => chartMarkers(stats.markers), [stats.markers]);
  return (
    <LineChart
      title={`${metric === "visitors" ? "Visitors" : "Page views"} per ${stats.bucket}`}
      points={points}
      from={start}
      to={stats.to}
      resolution={step / 1000}
      max={niceMax(Math.max(4, ...points.map((p) => p.v)))}
      format={charted}
      markers={markers}
      empty="Nothing counted in this range."
      roomy
    />
  );
}

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
  const [label, dimension] = tabs[tab] ?? ["Top", "page"];
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
  return (
    <section aria-label={title} className="flex min-w-0 flex-col gap-1.5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h3 className="text-sm font-semibold">{title}</h3>
        {/* The app's one segmented control, as everywhere else. */}
        {tabs.length > 1 && (
          <span className="seg">
            {tabs.map(([l], i) => (
              <button
                key={l}
                type="button"
                aria-pressed={i === tab}
                onClick={() => setTab(i)}
              >
                {l}
              </button>
            ))}
          </span>
        )}
      </div>
      <BreakdownTable
        dimension={dimension}
        label={label}
        rows={loaded ?? fetched.data}
        error={fetched.error}
        onPick={onPick}
      />
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
      <h3 id="live" className="flex flex-wrap items-baseline gap-x-2">
        <span className="text-2xl leading-none font-semibold text-ok tabular-nums">
          {live ? count(live.visitors) : "–"}
        </span>
        <span className="text-sm">
          live {live?.visitors === 1 ? "visitor" : "visitors"}
        </span>
        <span className="text-xs text-faint">
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
