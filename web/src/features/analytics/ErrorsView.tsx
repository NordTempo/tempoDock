import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Fragment, useMemo, useState } from "react";
import { useOrg } from "../../shell/location";
import { ErrorBlock } from "../../ui/ErrorBlock";
import { ago, clock, day } from "../../ui/format";
import { Panel } from "../../ui/kit";
import { LineChart } from "../stats/LineChart";
import { niceMax } from "../traffic/series";
import {
  type ErrorGroup,
  errorDimensions,
  type Filter,
  type SiteErrors,
  type StatsRange,
  siteErrorQuery,
  siteErrorsQuery,
} from "./data";
import {
  type Change,
  charted,
  chartMarkers,
  count,
  delta,
  Kpi,
  points,
  shown,
} from "./parts";

// A site's errors (ADR 0037, phase A2; Saile's pick: B's chart and table
// with C's rows that open in place). All errors over time at the top, then
// the groups with a sparkline each; a group opens under its row with its
// chart, stack and latest occurrences, linking to their replays.

type Count = { t: number; count: number };

/** A series with a zero in every bucket of the range nothing happened in. */
function filled(
  series: readonly Count[],
  from: number,
  to: number,
  step: number,
) {
  const at = new Map(series.map((p) => [p.t, p.count]));
  const out: { t: number; v: number }[] = [];
  for (let t = from - (from % step); t < to; t += step)
    out.push({ t, v: at.get(t) ?? 0 });
  return out;
}

export function ErrorsView({
  project,
  site,
  range,
  filters,
  open,
  onOpen,
}: {
  project: string;
  site: string;
  range: StatsRange;
  filters: Filter[];
  /** The group opened under its row, if one is. */
  open?: string;
  onOpen: (group: string | undefined) => void;
}) {
  // Errors record a page and its visitor, not the session's source: those
  // filters don't apply here.
  const usable = filters.filter((f) =>
    (errorDimensions as readonly string[]).includes(f.dimension),
  );
  const skipped = filters.filter((f) => !usable.includes(f));
  const { data, error, refetch } = useQuery(
    siteErrorsQuery(project, site, range, usable),
  );
  const [text, setText] = useState("");
  const [onlyNew, setOnlyNew] = useState(false);
  if (error) return <ErrorBlock error={error} onRetry={() => refetch()} />;
  if (!data) return <p className="text-mut">Loading…</p>;
  const step = data.bucket === "hour" ? 3_600_000 : 86_400_000;
  const needle = text.trim().toLowerCase();
  const groups = (data.groups ?? []).filter(
    (g) =>
      (!onlyNew || Date.parse(g.first_seen) >= data.from) &&
      (!needle ||
        `${g.type} ${g.message} ${g.frame}`.toLowerCase().includes(needle)),
  );
  return (
    <div className="flex flex-col gap-4">
      {skipped.length > 0 && (
        <p className="text-xs text-mut">
          Errors don't record{" "}
          {skipped.map((f) => f.dimension.replace("_", " ")).join(", ")}; those
          filters are left out here.
        </p>
      )}
      {!data.collecting && (
        <p className="text-warn-text">
          This site doesn't collect errors. Turn on "Errors and Web Vitals" in
          its settings.
        </p>
      )}
      {!data.store_online && (
        <p className="text-warn-text">{data.store_error}</p>
      )}
      <Summary data={data} range={range} />
      <LineChart
        title={`Errors per ${data.bucket}`}
        points={filled(data.series ?? [], data.from, data.to, step)}
        from={data.from - (data.from % step)}
        to={data.to}
        resolution={step / 1000}
        max={niceMax(Math.max(4, ...(data.series ?? []).map((p) => p.count)))}
        format={charted}
        markers={chartMarkers(data.markers)}
        empty="No errors in this range."
        roomy
      />
      <div className="flex flex-wrap items-center gap-3 text-xs">
        <input
          type="search"
          aria-label="Filter messages and files"
          placeholder="Filter messages and files"
          value={text}
          onChange={(e) => setText(e.target.value)}
          className="field w-64 py-0.5 text-xs"
        />
        <span className="seg">
          {(["All", "New"] as const).map((label) => (
            <button
              key={label}
              type="button"
              aria-pressed={(label === "New") === onlyNew}
              onClick={() => setOnlyNew(label === "New")}
            >
              {label}
            </button>
          ))}
        </span>
      </div>
      <Panel>
        <table className="tabular-nums">
          <thead>
            <tr>
              <th>Error</th>
              <th className="w-28">
                {data.bucket === "hour" ? "Per hour" : "Per day"}
              </th>
              <th className="w-16 text-right">Events</th>
              <th className="w-16 text-right">Sessions</th>
              <th className="w-16 text-right">Visitors</th>
              <th className="w-24">First seen</th>
              <th className="w-20">Last seen</th>
            </tr>
          </thead>
          <tbody>
            {groups.map((g) => (
              <Fragment key={g.group}>
                <Row
                  group={g}
                  data={data}
                  step={step}
                  open={open === g.group}
                  onToggle={() =>
                    onOpen(open === g.group ? undefined : g.group)
                  }
                />
                {open === g.group && (
                  <tr>
                    <td colSpan={7} className="bg-hover">
                      <Detail
                        project={project}
                        site={site}
                        group={g.group}
                        range={range}
                        filters={usable}
                        markers={data.markers ?? []}
                      />
                    </td>
                  </tr>
                )}
              </Fragment>
            ))}
            {groups.length === 0 && (
              <tr>
                <td colSpan={7} className="text-mut">
                  {data.groups?.length
                    ? "No errors match."
                    : "No errors in this range."}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </Panel>
    </div>
  );
}

/** The range's totals against the period before; more is worse. */
function Summary({ data, range }: { data: SiteErrors; range: StatsRange }) {
  const s = data.summary;
  const p = data.previous;
  const worse = (c?: Change) => c && { ...c, up: !c.up };
  return (
    <div className="grid grid-cols-[repeat(auto-fit,minmax(150px,1fr))] gap-x-6 gap-y-3 tabular-nums">
      <Kpi
        label="Errors"
        value={count(s.errors)}
        change={worse(delta(s.errors, p.errors, range))}
      />
      <Kpi
        label="Sessions with errors"
        value={`${(s.session_share * 100).toFixed(1)}%`}
        change={
          p.session_share
            ? points(-s.session_share, -p.session_share, range)
            : undefined
        }
      />
      <Kpi
        label="Visitors affected"
        value={count(s.visitors)}
        change={worse(delta(s.visitors, p.visitors, range))}
      />
      <div className="border-b-2 border-transparent pb-1">
        <span className="block text-xs text-mut">Groups</span>
        <span className="block text-lg font-semibold">{count(s.groups)}</span>
        <span className="block text-xs text-warn-text">
          {s.new_groups > 0 ? `${s.new_groups} new` : "\u00a0"}
        </span>
      </div>
    </div>
  );
}

/** One group's row: what it is, its sparkline and counts. */
function Row({
  group: g,
  data,
  step,
  open,
  onToggle,
}: {
  group: ErrorGroup;
  data: SiteErrors;
  step: number;
  open: boolean;
  onToggle: () => void;
}) {
  const isNew = Date.parse(g.first_seen) >= data.from;
  return (
    <tr
      className={`cursor-pointer hover:bg-hover ${open ? "bg-hover" : ""}`}
      onClick={onToggle}
    >
      <td className="max-w-0">
        <button
          type="button"
          aria-expanded={open}
          className="block w-full text-left"
          // The row's click opens it; the button makes it reachable by keyboard.
        >
          <span className="flex items-baseline gap-2">
            <span className="text-faint">{open ? "▾" : "▸"}</span>
            <span className="font-semibold">{g.type}</span>
            <span className="truncate" title={g.message}>
              {g.message}
            </span>
            {isNew && (
              <span className="shrink-0 text-xs text-warn-text">
                new{g.first_release ? ` in ${g.first_release}` : ""}
              </span>
            )}
          </span>
          <span className="flex min-h-[24px] items-center gap-3 pl-4 text-xs">
            <span className="min-w-0 truncate font-mono text-faint">
              {g.frame || "no stack"}
            </span>
            <span className="shrink-0 text-info">
              {open ? "Hide details" : "Details and stack"}
            </span>
          </span>
        </button>
      </td>
      <td>
        <Spark
          series={g.series ?? []}
          from={data.from}
          to={data.to}
          step={step}
        />
      </td>
      <td className="text-right">{count(g.count)}</td>
      <td className="text-right text-mut">{count(g.sessions)}</td>
      <td className="text-right text-mut">{count(g.visitors)}</td>
      <td className="text-xs text-mut">
        {day(g.first_seen)}
        {g.first_release && (
          <span className="block text-faint">{g.first_release}</span>
        )}
      </td>
      <td className="text-xs text-mut">{ago(g.last_seen)}</td>
    </tr>
  );
}

/** A tiny bar per bucket of the range. */
function Spark({
  series,
  from,
  to,
  step,
}: {
  series: readonly Count[];
  from: number;
  to: number;
  step: number;
}) {
  const bars = filled(series, from, to, step);
  const most = Math.max(1, ...bars.map((b) => b.v));
  const w = 100 / Math.max(1, bars.length);
  return (
    <svg
      viewBox="0 0 100 20"
      preserveAspectRatio="none"
      className="h-5 w-24 text-fail"
      aria-hidden
    >
      {bars.map((b, i) =>
        b.v > 0 ? (
          <rect
            key={b.t}
            x={i * w}
            y={20 - (b.v / most) * 20}
            width={Math.max(w - 0.5, 0.5)}
            height={(b.v / most) * 20}
            fill="currentColor"
            opacity={0.8}
          />
        ) : null,
      )}
    </svg>
  );
}

/** A group opened under its row. */
function Detail({
  project,
  site,
  group,
  range,
  filters,
  markers,
}: {
  project: string;
  site: string;
  group: string;
  range: StatsRange;
  filters: Filter[];
  markers: NonNullable<SiteErrors["markers"]>;
}) {
  const { data, error, refetch } = useQuery(
    siteErrorQuery(project, site, group, range, filters),
  );
  const org = useOrg();
  const markerPoints = useMemo(() => chartMarkers(markers), [markers]);
  if (error) return <ErrorBlock error={error} onRetry={() => refetch()} />;
  if (!data) return <p className="text-mut">Loading…</p>;
  const step = data.bucket === "hour" ? 3_600_000 : 86_400_000;
  const top = (rows: typeof data.pages, dim: string) => {
    const r = rows?.[0];
    return r && data.count
      ? `${shown(dim, r)} ${Math.round((r.count / data.count) * 100)}%`
      : undefined;
  };
  const facts = [
    `first seen ${day(data.first_seen)}${data.first_release ? ` in ${data.first_release}` : ""}`,
    `${count(data.sessions)} ${data.sessions === 1 ? "session" : "sessions"}`,
    top(data.browsers, "browser"),
    top(data.os, "os"),
    top(data.pages, "page"),
  ].filter(Boolean);
  return (
    <div className="flex flex-col gap-3 py-2">
      <p className="text-xs text-mut">{facts.join(" · ")}</p>
      <LineChart
        title="This error"
        points={filled(data.series ?? [], data.from, data.to, step)}
        from={data.from - (data.from % step)}
        to={data.to}
        resolution={step / 1000}
        max={niceMax(Math.max(1, ...(data.series ?? []).map((p) => p.count)))}
        format={charted}
        markers={markerPoints}
      />
      {data.stack && (
        <pre className="max-h-56 overflow-auto rounded-ctl border border-line bg-page p-2 font-mono text-xs whitespace-pre-wrap text-sec">
          {data.stack}
        </pre>
      )}
      <div className="flex flex-col gap-1">
        <h4 className="text-xs font-semibold">Latest, newest first</h4>
        <table className="text-xs tabular-nums">
          <thead>
            <tr>
              <th className="w-24">Time</th>
              <th>Page</th>
              <th>Browser</th>
              <th>Release</th>
              <th className="w-24" />
            </tr>
          </thead>
          <tbody>
            {(data.recent ?? []).slice(0, 10).map((o) => (
              <tr key={`${o.at}-${o.path}-${o.session}`}>
                <td className="text-mut">
                  {day(o.at)} {clock(o.at)}
                </td>
                <td className="max-w-0 truncate">{o.path}</td>
                <td className="text-mut">
                  {[
                    o.browser &&
                      `${o.browser} ${o.browser_version ?? ""}`.trim(),
                    o.os,
                  ]
                    .filter(Boolean)
                    .join(" · ") || "-"}
                </td>
                <td className="text-mut">{o.release || "-"}</td>
                <td className="text-right">
                  {o.replay ? (
                    <Link
                      to="/$org/analytics/$project/$site/replays/$replay"
                      params={{ org, project, site, replay: o.replay }}
                      search={{ at: Date.parse(o.at) }}
                    >
                      replay ›
                    </Link>
                  ) : (
                    <span className="text-faint">not recorded</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
