import {
  Fragment,
  memo,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  LOG_CAP,
  type LogStream,
  useBuildLog,
  useServiceLogs,
} from "../../data/live";
import { useReleases, useService } from "../../data/queries";
import type { LogLine, ServiceRef } from "../../data/types";
import { useShortcuts } from "../../shell/keys";
import { clockSec, since } from "../../ui/format";
import { CopyButton } from "../../ui/kit";
import { LogChart } from "./LogChart";
import { type LogLevel, logLevel, namedLevel } from "./logLevel";
import { type ParsedLog, parseLog } from "./logParse";

export type LogFilter = {
  /** A release's output; null follows the active release. */
  release: number | null;
  /** Build output instead of container output. */
  build: boolean;
  /** Only the lines logLevel reads as this level; null for all. */
  level: "warn" | "error" | null;
  /** Text, or /regex/. */
  query: string;
  /** Keep the newest line in view. */
  follow: boolean;
  /** Lines of backlog to start with; 0 for all (`dock logs -n`). */
  tail: number;
  /** Only lines newer than this: 10m, 1h, 24h (`dock logs --since`). */
  since: string;
  /**
   * Only the loaded lines in this window (ms, the end exclusive), picked
   * by dragging over the chart; null for all of them.
   */
  range: readonly [from: number, to: number] | null;
};

export const noFilter: LogFilter = {
  release: null,
  build: false,
  level: null,
  query: "",
  follow: true,
  tail: 500,
  since: "",
  range: null,
};

const tails = [100, 500, 2000, 0];
const sinces = ["", "10m", "1h", "6h", "24h"];
const levels = [
  [null, "All"],
  ["warn", "Warn"],
  ["error", "Error"],
] as const;

/** Rendered lines are capped; the stream keeps more for filtering. */
const VISIBLE = 500;

/** Rows open at once; opening another closes the oldest. */
const OPEN_CAP = 20;

/** Fields the time column already shows, left out of the row (not its details). */
const timeKeys = new Set(["time", "ts", "timestamp", "@timestamp"]);

// The column widths, shared by the header and the rows so they line up:
// in the rows' mono font, so ch is the same width in both tables. Phones
// leave the fields out (cells hidden below sm) for the message's room.
const cols = (
  <colgroup>
    <col className="w-[calc(9ch+2.5rem)] font-mono text-xs" />
    <col className="w-[8ch] font-mono text-xs" />
    <col className="w-full sm:w-[45%]" />
    <col />
  </colgroup>
);

/** A line read for the table: its level, its parts and its time in ms. */
type Row = {
  line: LogLine;
  level: LogLevel;
  log: ParsedLog;
  at: number | null;
};

// Each line is read once: lines are immutable and the stream keeps at
// most LOG_CAP, so the map holds no more than the lines still around.
const read = new WeakMap<LogLine, Row>();
function rowOf(line: LogLine): Row {
  let row = read.get(line);
  if (!row) {
    const log = parseLog(line.text);
    // A structured line's own level decides, so the column, the switch
    // and the chart agree (level=50, a nested "level" in JSON).
    const named = log.level === null ? undefined : namedLevel(log.level);
    row = {
      line,
      level: named === undefined ? logLevel(line.text) : named,
      log,
      at: line.time ? Date.parse(line.time) : null,
    };
    read.set(line, row);
  }
  return row;
}

function matcher(query: string): (text: string) => boolean {
  const regex = /^\/(.+)\/([a-z]*)$/.exec(query);
  if (regex?.[1]) {
    try {
      const re = new RegExp(regex[1], regex[2] || "i");
      return (text) => re.test(text);
    } catch {
      return () => false;
    }
  }
  const needle = query.toLowerCase();
  return (text) => text.toLowerCase().includes(needle);
}

/** Where the sticky controls end on screen, so a jump lands under them. */
function stuckBottom(el: HTMLElement | null) {
  if (!el) return 0;
  const top = Number.parseFloat(getComputedStyle(el).top);
  return (Number.isNaN(top) ? 0 : top) + el.offsetHeight;
}

/**
 * The logs tab: release tabs (the deploy in flight, the serving release,
 * the build), a picker for earlier releases whose output was kept, a text
 * or /regex/ filter and a level switch over a volume chart and a table of
 * lines split into time, level, message and fields (a click opens every
 * field). The window scrolls it: following keeps the newest line in view,
 * scrolling up pauses that and scrolling to the end resumes it.
 */
export function LogsPane({
  serviceRef: ref,
  filter,
  onFilter,
  cli,
}: {
  serviceRef: ServiceRef;
  filter: LogFilter;
  onFilter: (next: LogFilter) => void;
  /** The same view from a terminal, shown with a copy button. */
  cli: string;
}) {
  const { service, deploying } = useService(ref);
  const releases = useReleases(ref);
  const input = useRef<HTMLInputElement>(null);
  const controls = useRef<HTMLDivElement>(null);
  const body = useRef<HTMLTableSectionElement>(null);
  const foot = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const [seen, setSeen] = useState<readonly [number, number] | null>(null);

  const active = service.active_release ?? null;
  const release =
    filter.release ??
    (filter.build
      ? (deploying?.number ?? releases[0]?.number ?? null)
      : (deploying?.number ?? active));
  // Earlier releases' containers are gone; their kept output is read once.
  const liveNumber = deploying?.number ?? active;
  const live = release === active || release === deploying?.number;
  const app = useServiceLogs(ref, filter.build ? null : release, {
    tail: filter.tail,
    since: filter.since || undefined,
    follow: live,
  });
  const earlier = releases.filter(
    (r) =>
      r.number !== active &&
      r.number !== deploying?.number &&
      (r.logs_kept || r.number === filter.release),
  );
  const build = useBuildLog(ref, filter.build ? release : null);
  const stream: LogStream = filter.build ? build : app;
  // The build has no chart, so no range; a range ends in the past, so
  // there's nothing to follow while it's set.
  const range = filter.build ? null : filter.range;
  const followable = (live || filter.build) && !range;

  useShortcuts([
    {
      key: "f",
      label: "follow",
      run: () => onFilter({ ...filter, follow: !filter.follow }),
    },
    { key: "/", run: () => input.current?.focus() },
  ]);

  const { shown, counts } = useMemo(() => {
    const matches = filter.query ? matcher(filter.query) : null;
    const counts = { all: 0, warn: 0, error: 0 };
    const out: Row[] = [];
    for (const line of stream.lines) {
      if (matches && !matches(line.text)) continue;
      const row = rowOf(line);
      if (range && (row.at === null || row.at < range[0] || row.at >= range[1]))
        continue;
      counts.all++;
      if (row.level) counts[row.level]++;
      if (filter.level && row.level !== filter.level) continue;
      if (out.length < VISIBLE) out.push(row);
    }
    // The stream is newest first; read top to bottom like a terminal.
    return { shown: out.reverse(), counts };
  }, [stream.lines, filter.level, filter.query, range]);

  // Following keeps the newest line in view.
  const newest = shown.at(-1)?.line.key;
  useEffect(() => {
    if (filter.follow && followable && newest) {
      window.scrollTo({ top: document.documentElement.scrollHeight });
    }
  }, [filter.follow, followable, newest]);
  // A new range reads from its start.
  const [from, to] = range ?? [];
  useEffect(() => {
    if (from !== undefined) window.scrollTo({ top: 0 });
  }, [from, to]);

  // The stream's newest line when following stopped, to count what came
  // since. The whole stream, not the shown lines, so a filter changed
  // while paused counts nothing. Another source drops the mark: its
  // lines aren't the ones counted from.
  const [mark, setMark] = useState<string | undefined>();
  const markRef = useRef(stream.lines[0]?.key);
  markRef.current = stream.lines[0]?.key;
  useEffect(() => {
    setMark(filter.follow ? undefined : markRef.current);
  }, [filter.follow]);
  useEffect(() => setMark(undefined), [release, filter.build]);
  const markAt = mark ? stream.lines.findIndex((l) => l.key === mark) : -1;
  // A mark the stream no longer keeps means more came than it holds.
  const evicted = mark !== undefined && markAt < 0;
  const fresh = evicted ? stream.lines.length : Math.max(markAt, 0);

  // The time range on screen, which the chart outlines.
  const outline = useCallback(() => {
    const rows = body.current?.querySelectorAll<HTMLElement>("tr[data-at]");
    if (!rows?.length) return setSeen(null);
    const top = stuckBottom(controls.current);
    const bottom = window.innerHeight - (foot.current?.offsetHeight ?? 0);
    const first = Math.min(firstBelow(rows, top), rows.length - 1);
    const last = Math.max(first, firstBelow(rows, bottom) - 1);
    const from = Number(rows[first]?.dataset.at);
    const to = Number(rows[last]?.dataset.at);
    setSeen((was) => (was?.[0] === from && was[1] === to ? was : [from, to]));
  }, []);
  // New lines move it too.
  useEffect(outline, [outline, shown]);

  // Scrolling decides following: up pauses it, the end resumes it. Only
  // scrolling does: a page that grows under a paused view stays paused.
  // One measure per frame, on scroll only.
  const latest = useRef({ filter, onFilter, followable });
  latest.current = { filter, onFilter, followable };
  useEffect(() => {
    let frame = 0;
    const measure = () => {
      frame = 0;
      const doc = document.documentElement;
      const atEnd = window.innerHeight + window.scrollY >= doc.scrollHeight - 4;
      const { filter, onFilter, followable } = latest.current;
      if (followable && atEnd !== filter.follow) {
        onFilter({ ...filter, follow: atEnd });
      }
      outline();
    };
    const onScroll = () => {
      frame ||= requestAnimationFrame(measure);
    };
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      window.removeEventListener("scroll", onScroll);
      cancelAnimationFrame(frame);
    };
  }, [outline]);

  const jump = (at: number) => {
    const row = [
      ...(body.current?.querySelectorAll<HTMLElement>("tr[data-at]") ?? []),
    ].find((r) => Number(r.dataset.at) >= at);
    if (!row) return;
    window.scrollTo({
      top:
        row.getBoundingClientRect().top +
        window.scrollY -
        stuckBottom(controls.current),
    });
  };
  const toggle = useCallback(
    (key: string) =>
      setOpen((was) => {
        const next = new Set(was);
        if (!next.delete(key)) next.add(key);
        // The last few opened stay open; older ones close.
        for (const old of next) {
          if (next.size <= OPEN_CAP) break;
          next.delete(old);
        }
        return next;
      }),
    [],
  );

  // The deploy in flight, the serving release, the build; earlier
  // releases with kept output are picked from a list.
  const tabs = [
    ...(deploying
      ? [{ n: deploying.number, label: `r${deploying.number}` }]
      : []),
    ...(active
      ? [
          {
            n: active,
            // Starting until its health check first passes, as the header says.
            label: `r${active} ${service.stopped_at ? "stopped" : service.status.health === "starting" ? "starting" : "serving"}`,
          },
        ]
      : []),
  ];

  const note =
    !live && !filter.build ? (
      <div className="text-faint">Release {release}, kept history</div>
    ) : null;
  const empty = stream.error ? (
    service.stopped_at && release === active && !filter.build ? (
      <div className="text-mut">
        Stopped: no container runs. Start it to see its output.
      </div>
    ) : deploying && release === deploying.number && !filter.build ? (
      <div className="text-mut">Waiting for the container to start.</div>
    ) : (
      <div className="text-fail">{stream.error}</div>
    )
  ) : shown.length === 0 ? (
    <div className="text-mut">
      {release === null
        ? "Not deployed yet."
        : stream.live
          ? "Waiting for output."
          : filter.query || filter.level || range
            ? "No lines match."
            : "No output."}
    </div>
  ) : null;

  return (
    <div className="flex flex-1 flex-col">
      <div
        ref={controls}
        className="z-[5] border-b border-line bg-page px-4 sm:px-7 md:sticky md:top-[calc(var(--shell-top)+var(--service-top,112px))]"
      >
        <div className="flex flex-wrap items-center gap-x-3.5 gap-y-2 py-2.5">
          <fieldset aria-label="Source" className="seg font-mono text-2xs">
            {tabs.map((tab) => (
              <button
                key={tab.n}
                type="button"
                aria-pressed={!filter.build && release === tab.n}
                onClick={() =>
                  onFilter({
                    ...filter,
                    build: false,
                    release: tab.n === active && !deploying ? null : tab.n,
                  })
                }
              >
                {tab.label}
              </button>
            ))}
            <button
              type="button"
              aria-pressed={filter.build}
              onClick={() => onFilter({ ...filter, build: !filter.build })}
            >
              build
            </button>
          </fieldset>
          {earlier.length > 0 && (
            <select
              aria-label="Release"
              title="dock logs -r N: an earlier release's output, kept after its container was removed"
              value={live ? "" : (release ?? "")}
              onChange={(e) =>
                onFilter({
                  ...filter,
                  release: e.target.value ? Number(e.target.value) : null,
                })
              }
              className="field min-h-[24px] py-0.5 font-mono text-2xs"
            >
              <option value="">
                {liveNumber ? `Live (release ${liveNumber})` : "Live"}
              </option>
              {earlier.map((r) => (
                <option key={r.number} value={r.number}>
                  r{r.number} {r.status} {since(r.finished_at ?? r.created_at)}
                </option>
              ))}
            </select>
          )}
          <label className="flex flex-[1_1_200px] items-center gap-1.5">
            <span className="sr-only">Filter</span>
            <input
              ref={input}
              value={filter.query}
              onChange={(e) => onFilter({ ...filter, query: e.target.value })}
              onKeyDown={(e) => {
                if (e.key === "Escape") e.currentTarget.blur();
              }}
              placeholder="Filter: text or /regex/"
              className="field min-w-0 flex-1 py-1 font-mono text-xs"
            />
          </label>
          <fieldset aria-label="Level" className="seg">
            {levels.map(([level, label]) => (
              <button
                key={label}
                type="button"
                aria-pressed={filter.level === level}
                onClick={() => onFilter({ ...filter, level })}
              >
                {label}
                <span
                  className={`ml-1.5 font-mono ${level === "warn" ? "text-warn-text" : level === "error" ? "text-fail" : "text-mut"}`}
                >
                  {counts[level ?? "all"]}
                </span>
              </button>
            ))}
          </fieldset>
          {range && (
            <button
              type="button"
              aria-label={`Lines from ${clockSec(new Date(range[0]).toISOString())} to ${clockSec(new Date(range[1]).toISOString())}: show every line again`}
              title="Show every line again"
              onClick={() => onFilter({ ...filter, range: null, follow: true })}
              className="btn sm font-mono"
            >
              {clockSec(new Date(range[0]).toISOString())} to{" "}
              {clockSec(new Date(range[1]).toISOString())} ×
            </button>
          )}
          {!filter.build && (
            <span className="flex items-center gap-1.5 text-xs text-sec">
              <select
                aria-label="Backlog lines"
                title={`dock logs -n; the view keeps the newest ${LOG_CAP}`}
                value={filter.tail}
                onChange={(e) =>
                  onFilter({ ...filter, tail: Number(e.target.value) })
                }
                className="field min-h-[24px] py-0.5 text-xs"
              >
                {tails.map((n) => (
                  <option key={n} value={n}>
                    {n ? `last ${n}` : "all lines"}
                  </option>
                ))}
              </select>
              <select
                aria-label="Since"
                title="dock logs --since"
                value={filter.since}
                onChange={(e) => onFilter({ ...filter, since: e.target.value })}
                className="field min-h-[24px] py-0.5 text-xs"
              >
                {sinces.map((s) => (
                  <option key={s} value={s}>
                    {s ? `since ${s}` : "any time"}
                  </option>
                ))}
              </select>
            </span>
          )}
          {followable && (
            <button
              type="button"
              aria-pressed={filter.follow}
              onClick={() => onFilter({ ...filter, follow: !filter.follow })}
              className="flex items-center gap-1.5 text-xs text-sec hover:text-fg"
            >
              <span className={filter.follow ? "text-ok" : "text-mut"}>●</span>
              {filter.follow ? "Following" : "Paused"} <kbd>F</kbd>
            </button>
          )}
        </div>
        {!filter.build && (
          <>
            <LogChart
              lines={shown}
              seen={seen}
              onJump={jump}
              onRange={(from, to) =>
                onFilter({ ...filter, range: [from, to], follow: false })
              }
            />
            {shown.length > 0 && (
              <table className="-mx-4 w-[calc(100%+2rem)] table-fixed sm:-mx-7 sm:w-[calc(100%+3.5rem)]">
                {cols}
                <thead>
                  <tr>
                    <th className="border-0 py-1.5 pl-4 sm:pl-7">Time</th>
                    <th className="border-0 py-1.5">Level</th>
                    <th className="border-0 py-1.5">Message</th>
                    <th className="hidden border-0 py-1.5 sm:table-cell">
                      Fields
                    </th>
                  </tr>
                </thead>
              </table>
            )}
          </>
        )}
      </div>
      {(note || empty) && (
        <div className="px-4 pt-3 font-mono text-xs sm:px-7">
          {note}
          {empty}
        </div>
      )}
      {filter.build ? (
        <div className="py-2.5 font-mono text-xs leading-[1.7] sm:px-4">
          {shown.map((row) => (
            <LogRow key={row.line.key} line={row.line} />
          ))}
        </div>
      ) : (
        shown.length > 0 && (
          <div role="log" aria-label={`r${release} logs`}>
            <table className="table-fixed font-mono text-xs">
              {cols}
              <tbody ref={body}>
                {shown.map((row) => (
                  <LogTableRow
                    key={row.line.key}
                    row={row}
                    open={open.has(row.line.key)}
                    onToggle={toggle}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )
      )}
      <div className="flex-1" />
      <div
        ref={foot}
        className="sticky bottom-0 flex items-center gap-3.5 border-t border-line bg-page px-4 py-1.5 text-xs text-mut sm:px-7"
      >
        {followable && !filter.follow && shown.length > 0 && (
          <button
            type="button"
            onClick={() => onFilter({ ...filter, follow: true })}
            className="btn pri sm absolute right-4 -top-12 rounded-full sm:right-7"
          >
            ↓{" "}
            {fresh > 0
              ? `${fresh}${evicted ? "+" : ""} new ${fresh === 1 ? "line" : "lines"}`
              : "Newest"}
          </button>
        )}
        <span>
          {shown.length === VISIBLE
            ? `The newest ${VISIBLE} lines`
            : `${shown.length} ${shown.length === 1 ? "line" : "lines"}`}
        </span>
        <code className="ml-auto min-w-0 truncate font-mono text-2xs">
          {cli}
        </code>
        <CopyButton text={cli} />
      </div>
    </div>
  );
}

/**
 * The index of the first row whose bottom is below y on screen; the
 * number of rows when every row is above it.
 */
function firstBelow(rows: NodeListOf<HTMLElement>, y: number) {
  let lo = 0;
  let hi = rows.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    const r = rows[mid];
    if (r && r.getBoundingClientRect().bottom <= y) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

/**
 * One line in the logs table: time, level, message and fields on one
 * line, cut at the edge; a click (or its time, from the keyboard) opens
 * every field and the whole line under it. Errors get a red rule and a
 * tint, warnings an amber rule.
 */
const LogTableRow = memo(function LogTableRow({
  row: { line, level, log, at },
  open,
  onToggle,
}: {
  row: Row;
  open: boolean;
  onToggle: (key: string) => void;
}) {
  const structured = log.fields.length > 0 || log.level !== null;
  const word = log.level ?? level ?? "";
  const rule =
    level === "error"
      ? "shadow-[inset_2px_0_0_var(--color-fail)]"
      : level === "warn"
        ? "shadow-[inset_2px_0_0_var(--color-warn)]"
        : "";
  const cell = "py-0.5 leading-[1.6]";
  return (
    <>
      <tr
        data-at={at ?? undefined}
        // Selecting text in a line isn't a click on it.
        onClick={() =>
          getSelection()?.isCollapsed !== false && onToggle(line.key)
        }
        className={`cursor-pointer hover:bg-hover ${open ? "bg-sel" : level === "error" ? "bg-fail-bg/50" : ""}`}
      >
        <td className={`${cell} pl-4 sm:pl-7 ${rule}`}>
          <button
            type="button"
            aria-expanded={open}
            onClick={(e) => {
              e.stopPropagation();
              onToggle(line.key);
            }}
            className="text-mut"
          >
            {line.time ? clockSec(line.time) : "·"}
          </button>
        </td>
        <td
          className={`${cell} uppercase ${level === "error" ? "text-fail" : level === "warn" ? "text-warn" : "text-mut"}`}
        >
          {word}
        </td>
        {structured ? (
          <>
            <td className={`${cell} truncate text-fg`}>{log.msg}</td>
            <td
              className={`${cell} hidden truncate pr-4 sm:table-cell sm:pr-7`}
            >
              {log.fields
                .filter(([key]) => !timeKeys.has(key))
                .map(([key, value], i) => (
                  <Fragment key={key + i}>
                    {i > 0 && " "}
                    <span className="text-mut">{key}=</span>
                    <span className="text-sec">
                      {/\s/.test(value) && !/^[[{]/.test(value)
                        ? JSON.stringify(value)
                        : value}
                    </span>
                  </Fragment>
                ))}
            </td>
          </>
        ) : (
          <td
            colSpan={2}
            className={`${cell} truncate whitespace-nowrap pr-4 text-fg sm:pr-7`}
          >
            {line.text}
          </td>
        )}
      </tr>
      {open && (
        <tr>
          <td
            colSpan={4}
            className="bg-panel py-2.5 pr-4 pl-4 sm:pr-7 sm:pl-[calc(17ch+2.5rem)]"
          >
            {structured && (
              <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-0.5">
                {log.msg && (
                  <>
                    <dt className="text-mut">msg</dt>
                    <dd className="break-words whitespace-pre-wrap text-fg">
                      {log.msg}
                    </dd>
                  </>
                )}
                {log.fields.map(([key, value], i) => (
                  <Fragment key={key + i}>
                    <dt className="text-mut">{key}</dt>
                    <dd className="break-words whitespace-pre-wrap text-fg">
                      {value}
                    </dd>
                  </Fragment>
                ))}
              </dl>
            )}
            {/* The line as it came: under the fields for a structured one. */}
            <div
              className={`break-words whitespace-pre-wrap ${structured ? "mt-2 text-mut" : "text-fg"}`}
            >
              {line.text}
            </div>
            <div className="mt-2 flex items-center gap-3.5 font-sans">
              <CopyButton text={line.text} label="Copy line" />
              {line.time && (
                <span className="text-mut">
                  {new Date(line.time).toLocaleString()} · {line.stream}
                </span>
              )}
            </div>
          </td>
        </tr>
      )}
    </>
  );
});

/**
 * One log line as it came, wrapped under its time, with WARN and ERROR
 * words colored: build output, and a failed release's last lines. A
 * line logLevel reads as an error gets a red rule and tint, a warning an
 * amber one. Quiet drops the tint, for a block that is already about a
 * failure.
 */
export const LogRow = memo(function LogRow({
  line,
  quiet,
}: {
  line: LogLine;
  quiet?: boolean;
}) {
  const level = quiet ? null : logLevel(line.text);
  const parts = line.text.split(/\b(WARN(?:ING)?|ERROR|FATAL|PANIC)\b/);
  return (
    <div
      // Long lines wrap (under their time) instead of running off the edge.
      className={`border-l-2 pr-3 pl-[calc(0.75rem+9ch)] -indent-[9ch] break-words whitespace-pre-wrap ${level === "error" ? "border-fail bg-fail-bg" : level === "warn" ? "border-warn bg-warn-bg" : "border-transparent"}`}
    >
      {line.time && <span className="text-faint">{clockSec(line.time)} </span>}
      {parts.map((part, i) =>
        i % 2 === 1 ? (
          <span
            key={i}
            className={part.startsWith("WARN") ? "text-warn" : "text-fail"}
          >
            {part}
          </span>
        ) : (
          part
        ),
      )}
    </div>
  );
});
