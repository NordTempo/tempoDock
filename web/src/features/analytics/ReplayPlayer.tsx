import "@rrweb/replay/dist/style.css";
import { Replayer } from "@rrweb/replay";
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState } from "react";
import { useOrg } from "../../shell/location";
import { day } from "../../ui/format";
import { Button } from "../../ui/kit";
import {
  loadRecording,
  parseFilter,
  type RecordedEvent,
  type ReplayDetail,
  replayQuery,
  type StatsRange,
  siteReplaysQuery,
} from "./data";
import { replayFilters } from "./ReplaysView";

// The session replay player (ADR 0037, phase A3, mock A): rrweb's
// replayer scaled into the page, a scrubber with the visit's page views
// and errors on it, and a column with the timeline following playback.
// Loading the recording is audited; the column says so.

type Item = NonNullable<ReplayDetail["timeline"]>[number];

type Tab = "all" | "error" | "event";

/** m:ss of a duration in milliseconds. */
const mmss = (ms: number) => {
  const s = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
};

const regions = new Intl.DisplayNames(["en"], { type: "region" });

export function ReplayPlayer({
  project,
  site,
  replay: id,
  at,
  atTime,
  list = {},
}: {
  project: string;
  site: string;
  replay: string;
  /** Where to start, in seconds (a shared link). */
  at?: number;
  /** Or a moment to start a few seconds before (Unix ms): an error's. */
  atTime?: number;
  /** The list this replay was opened from, for Previous and Next. */
  list?: {
    range?: StatsRange;
    f?: string[];
    errors?: boolean;
    device?: string;
  };
}) {
  const org = useOrg();
  const { data: replay, error } = useQuery(replayQuery(project, site, id));
  const [events, setEvents] = useState<RecordedEvent[]>();
  const [loadError, setLoadError] = useState<string>();
  useEffect(() => {
    if (!replay) return;
    const ctl = new AbortController();
    loadRecording(project, site, replay, ctl.signal).then(setEvents, (e) => {
      if (!ctl.signal.aborted) setLoadError(String(e.message ?? e));
    });
    return () => ctl.abort();
  }, [project, site, replay]);

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <Link
          to="/$org/analytics/$project/$site/replays"
          params={{ org, project, site }}
          search={list}
          className="text-mut"
        >
          Replays
        </Link>
        <span className="text-faint">›</span>
        <span className="font-semibold">
          Session <span className="font-mono">{id.slice(0, 6)}…</span>
        </span>
        {replay && replay.errors > 0 && (
          <span className="text-fail text-xs">
            {replay.errors} {replay.errors === 1 ? "error" : "errors"}
          </span>
        )}
        {replay && (
          <Walk project={project} site={site} current={replay.id} list={list} />
        )}
      </div>
      {(error || loadError) && (
        <p className="text-fail">{error?.message ?? loadError}</p>
      )}
      {replay && events ? (
        <Player replay={replay} events={events} at={at} atTime={atTime} />
      ) : (
        !(error || loadError) && <p className="text-mut">Loading…</p>
      )}
    </div>
  );
}

/**
 * Previous and Next through the list the replay was opened from, newest
 * first: its query is the list's, so mostly already loaded.
 */
function Walk({
  project,
  site,
  current,
  list,
}: {
  project: string;
  site: string;
  current: string;
  list: { range?: StatsRange; f?: string[]; errors?: boolean; device?: string };
}) {
  const org = useOrg();
  const filters = (list.f ?? []).flatMap((f) => parseFilter(f) ?? []);
  const { data } = useQuery(
    siteReplaysQuery(
      project,
      site,
      list.range ?? "30d",
      replayFilters(filters, list.device),
      !!list.errors,
    ),
  );
  const ids = (data?.replays ?? []).map((r) => r.id);
  const at = ids.indexOf(current);
  const link = (id: string | undefined, label: string) =>
    id ? (
      <Link
        to="/$org/analytics/$project/$site/replays/$replay"
        params={{ org, project, site, replay: id }}
        search={list}
      >
        {label}
      </Link>
    ) : (
      <span className="text-faint">{label}</span>
    );
  if (at < 0) return null;
  return (
    <span className="ml-auto flex gap-3 text-xs">
      {link(ids[at - 1], "‹ Previous")}
      {link(ids[at + 1], "Next ›")}
    </span>
  );
}

function Player({
  replay,
  events,
  at,
  atTime,
}: {
  replay: ReplayDetail;
  events: RecordedEvent[];
  at?: number;
  atTime?: number;
}) {
  const host = useRef<HTMLDivElement>(null);
  const frame = useRef<HTMLDivElement>(null);
  const player = useRef<Replayer>(null);
  const [playing, setPlaying] = useState(false);
  const [now, setNow] = useState(0);
  const [speed, setSpeed] = useState(1);
  const [skipIdle, setSkipIdle] = useState(true);
  const [size, setSize] = useState({ width: 1280, height: 800 });
  const [scale, setScale] = useState(1);
  const [tab, setTab] = useState<Tab>("all");
  const start = events[0]?.timestamp ?? 0;
  const total = (events[events.length - 1]?.timestamp ?? start) - start;

  useEffect(() => {
    if (!host.current || events.length < 2) return;
    const r = new Replayer(events, {
      root: host.current,
      skipInactive: true,
      showWarning: false,
      mouseTail: false,
      triggerFocus: false,
    });
    r.on("resize", (d) => {
      const { width, height } = d as { width: number; height: number };
      setSize({ width, height });
    });
    r.on("finish", () => setPlaying(false));
    player.current = r;
    const t = Math.min(
      atTime ? Math.max(0, atTime - start - 3000) : (at ?? 0) * 1000,
      total,
    );
    r.pause(t);
    setNow(t);
    return () => {
      r.destroy();
      player.current = null;
    };
  }, [events, at, atTime, start, total]);

  // Fit the recorded viewport into the frame.
  useEffect(() => {
    const el = frame.current;
    if (!el) return;
    const fit = () => setScale(Math.min(1, el.clientWidth / size.width));
    fit();
    const ro = new ResizeObserver(fit);
    ro.observe(el);
    return () => ro.disconnect();
  }, [size]);

  // The clock moves only while playing.
  useEffect(() => {
    if (!playing) return;
    const t = setInterval(
      () => setNow(player.current?.getCurrentTime() ?? 0),
      250,
    );
    return () => clearInterval(t);
  }, [playing]);

  const seek = (ms: number, play = playing) => {
    const r = player.current;
    if (!r) return;
    const t = Math.min(Math.max(ms, 0), total);
    if (play) r.play(t);
    else r.pause(t);
    setNow(t);
  };
  const toggle = () => {
    const r = player.current;
    if (!r) return;
    if (playing) {
      r.pause();
      setNow(r.getCurrentTime());
    } else r.play(now >= total ? 0 : now);
    setPlaying(!playing);
  };

  const items = replay.timeline ?? [];
  const at0 = (it: Item) => Date.parse(it.at) - start;
  const current = useMemo(() => {
    let i = -1;
    for (const [j, it] of items.entries())
      if (Date.parse(it.at) - start <= now) i = j;
    return i;
  }, [items, now, start]);
  const page = items
    .slice(0, current + 1)
    .filter((i) => i.kind === "pageview").length;
  const pages = items.filter((i) => i.kind === "pageview").length;
  const url =
    [...items.slice(0, current + 1)]
      .reverse()
      .find((i) => i.kind === "pageview")?.path ?? replay.entry_path;
  const shownItems = items.filter((i) => tab === "all" || i.kind === tab);

  return (
    <div className="grid gap-x-6 gap-y-4 xl:grid-cols-[minmax(0,1fr)_320px]">
      <div className="flex min-w-0 flex-col gap-2">
        <div className="flex items-center gap-2 rounded-t-panel border border-line bg-panel px-3 py-1.5 text-xs">
          <span className="truncate font-mono text-sec">{url}</span>
          <span className="ml-auto text-faint">
            {size.width}×{size.height}
            {scale < 1 && ` · scaled ${Math.round(scale * 100)}%`}
          </span>
        </div>
        <div
          ref={frame}
          className="relative overflow-hidden border-x border-line bg-page"
          style={{ height: size.height * scale }}
        >
          <div
            ref={host}
            className="absolute top-0 left-0 origin-top-left"
            style={{
              transform: `scale(${scale})`,
              width: size.width,
              height: size.height,
            }}
          />
        </div>
        <Scrubber
          total={total}
          now={now}
          items={items}
          offset={at0}
          onSeek={(t) => seek(t)}
        />
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <Button onClick={toggle}>{playing ? "❚❚ Pause" : "▶ Play"}</Button>
          <span className="font-mono">
            {mmss(now)} / {mmss(total)}
          </span>
          <span className="flex gap-1">
            {[1, 2, 4].map((s) => (
              <button
                key={s}
                type="button"
                className={`rounded-ctl px-1.5 ${speed === s ? "bg-sel text-fg" : "text-mut"}`}
                onClick={() => {
                  setSpeed(s);
                  player.current?.setConfig({ speed: s });
                }}
              >
                {s}×
              </button>
            ))}
          </span>
          <label className="flex items-center gap-1 text-mut">
            <input
              type="checkbox"
              checked={skipIdle}
              onChange={(e) => {
                setSkipIdle(e.target.checked);
                player.current?.setConfig({ skipInactive: e.target.checked });
              }}
            />
            Skip idle
          </label>
          <span className="text-faint">
            Page {Math.max(page, 1)} of {Math.max(pages, 1)} · Inputs masked
          </span>
          <button
            type="button"
            className="ml-auto text-mut"
            onClick={() => {
              const u = new URL(window.location.href);
              u.searchParams.set("t", String(Math.floor(now / 1000)));
              void navigator.clipboard.writeText(u.toString());
            }}
          >
            Copy link at {mmss(now)}
          </button>
        </div>
      </div>
      <aside className="flex flex-col gap-5">
        <div className="flex flex-col gap-2">
          <div className="flex items-center gap-2">
            <span className="font-semibold">Timeline</span>
            <span className="ml-auto flex gap-1 text-xs">
              {(["all", "error", "event"] as const).map((t) => (
                <button
                  key={t}
                  type="button"
                  className={`rounded-ctl px-1.5 ${tab === t ? "bg-sel text-fg" : "text-mut"}`}
                  onClick={() => setTab(t)}
                >
                  {t === "all" ? "All" : t === "error" ? "Errors" : "Events"}
                </button>
              ))}
            </span>
          </div>
          <ol className="flex max-h-[520px] flex-col overflow-y-auto text-xs">
            {shownItems.slice(0, 500).map((it) => {
              const t = at0(it);
              const past = t <= now;
              const isCurrent = items[current] === it;
              return (
                <li key={`${it.at}-${it.kind}-${it.path}-${it.name}`}>
                  <button
                    type="button"
                    onClick={() => seek(t)}
                    className={`grid w-full grid-cols-[3.2rem_3.5rem_minmax(0,1fr)] gap-1 rounded-ctl px-1.5 py-1 text-left ${isCurrent ? "bg-sel" : ""} ${past ? "" : "text-mut"}`}
                  >
                    <span className="font-mono text-faint">{mmss(t)}</span>
                    <span
                      className={
                        it.kind === "error"
                          ? "text-fail"
                          : it.kind === "event"
                            ? "text-info"
                            : "text-faint"
                      }
                    >
                      {it.kind === "pageview" ? "page" : it.kind}
                    </span>
                    <span className="truncate" title={it.name || it.path}>
                      {it.kind === "pageview" ? it.path : it.name}
                    </span>
                  </button>
                </li>
              );
            })}
          </ol>
        </div>
        <dl className="grid grid-cols-[6rem_minmax(0,1fr)] gap-x-2 gap-y-1 text-xs">
          <dt className="text-mut">Visitor</dt>
          <dd>
            {[
              replay.country && (regions.of(replay.country) ?? replay.country),
              replay.browser,
              replay.os,
              replay.device,
            ]
              .filter(Boolean)
              .join(" · ") || "-"}
          </dd>
          <dt className="text-mut">Served by</dt>
          <dd>{replay.release || "-"}</dd>
          <dt className="text-mut">Started</dt>
          <dd>
            {day(replay.start)} ·{" "}
            {mmss(Date.parse(replay.end) - Date.parse(replay.start))}
          </dd>
          <dt className="text-mut">Page loads</dt>
          <dd>{replay.segments}</dd>
        </dl>
        <p className="text-faint text-xs">
          Watching a replay is recorded in the audit log.
        </p>
      </aside>
    </div>
  );
}

/** The progress bar, with a tick per page view and error, seekable. */
function Scrubber({
  total,
  now,
  items,
  offset,
  onSeek,
}: {
  total: number;
  now: number;
  items: Item[];
  offset: (it: Item) => number;
  onSeek: (ms: number) => void;
}) {
  const pct = (ms: number) =>
    `${total > 0 ? (Math.min(Math.max(ms, 0), total) / total) * 100 : 0}%`;
  return (
    <div className="relative h-6">
      <div className="absolute top-2.5 right-0 left-0 h-1 rounded-full bg-line" />
      <div
        className="absolute top-2.5 left-0 h-1 rounded-full bg-sec"
        style={{ width: pct(now) }}
      />
      {items
        .filter((i) => i.kind !== "event")
        .map((it) => (
          <span
            key={`${it.at}-${it.kind}-${it.path}`}
            title={it.kind === "error" ? it.name : it.path}
            className={`absolute top-1 h-4 w-0.5 ${it.kind === "error" ? "bg-fail" : "bg-faint"}`}
            style={{ left: pct(offset(it)) }}
          />
        ))}
      <input
        type="range"
        aria-label="Position"
        min={0}
        max={Math.max(total, 1)}
        value={now}
        onChange={(e) => onSeek(Number(e.target.value))}
        className="absolute inset-0 w-full cursor-pointer opacity-0"
      />
    </div>
  );
}
