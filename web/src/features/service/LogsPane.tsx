import { memo, useEffect, useMemo, useRef } from "react";
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

export type LogFilter = {
  /** A release's output; null follows the active release. */
  release: number | null;
  /** Build output instead of container output. */
  build: boolean;
  /** stderr lines and lines that say error, fatal or panic. */
  errors: boolean;
  /** Text, or /regex/. */
  query: string;
  /** Keep the newest line in view. */
  follow: boolean;
  /** Lines of backlog to start with; 0 for all (`dock logs -n`). */
  tail: number;
  /** Only lines newer than this: 10m, 1h, 24h (`dock logs --since`). */
  since: string;
};

export const noFilter: LogFilter = {
  release: null,
  build: false,
  errors: false,
  query: "",
  follow: true,
  tail: 500,
  since: "",
};

const tails = [100, 500, 2000, 0];
const sinces = ["", "10m", "1h", "6h", "24h"];

/** Rendered lines are capped; the stream keeps more for filtering. */
const VISIBLE = 500;

// Error words, or an HTTP 5xx status between spaces ("POST /api 502 31ms").
const errorWords = /\b(error|fatal|panic|exception)\b|\s5\d\d\s/i;

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

/**
 * Live log tail with release tabs (the deploy in flight, the serving
 * release, the build), a picker for earlier releases whose output was
 * kept, a text or /regex/ filter, errors only and follow.
 */
export function LogsPane({
  serviceRef: ref,
  filter,
  onFilter,
  height,
}: {
  serviceRef: ServiceRef;
  filter: LogFilter;
  onFilter: (next: LogFilter) => void;
  /** Tailwind height class for the log box. */
  height: string;
}) {
  const { service, deploying } = useService(ref);
  const releases = useReleases(ref);
  const input = useRef<HTMLInputElement>(null);
  const box = useRef<HTMLDivElement>(null);

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

  useShortcuts([
    {
      key: "f",
      label: "follow",
      run: () => onFilter({ ...filter, follow: !filter.follow }),
    },
    { key: "/", run: () => input.current?.focus() },
  ]);

  const shown = useMemo(() => {
    const matches = filter.query ? matcher(filter.query) : null;
    const out: LogLine[] = [];
    for (const line of stream.lines) {
      if (
        filter.errors &&
        line.stream !== "stderr" &&
        !errorWords.test(line.text)
      )
        continue;
      if (matches && !matches(line.text)) continue;
      out.push(line);
      if (out.length === VISIBLE) break;
    }
    // The stream is newest first; read top to bottom like a terminal.
    return out.reverse();
  }, [stream.lines, filter.errors, filter.query]);

  // Following keeps the newest line in view.
  const newest = shown.at(-1)?.key;
  useEffect(() => {
    if (filter.follow && newest && box.current) {
      box.current.scrollTop = box.current.scrollHeight;
    }
  }, [filter.follow, newest]);

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

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-x-3.5 gap-y-2">
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
        <label className="flex items-center gap-1.5 text-xs text-sec">
          <input
            type="checkbox"
            className="accent-fg"
            checked={filter.errors}
            onChange={(e) => onFilter({ ...filter, errors: e.target.checked })}
          />
          Errors only
        </label>
        {(live || filter.build) && (
          <label className="flex items-center gap-1.5 text-xs text-sec">
            <input
              type="checkbox"
              className="accent-fg"
              checked={filter.follow}
              onChange={(e) =>
                onFilter({ ...filter, follow: e.target.checked })
              }
            />
            Follow <kbd>F</kbd>
          </label>
        )}
      </div>
      <div
        ref={box}
        role="log"
        // Focusable, so keyboard users can scroll it with the arrow keys.
        // biome-ignore lint/a11y/noNoninteractiveTabindex: a scrollable region
        tabIndex={0}
        aria-label={`${filter.build ? "build" : `r${release}`} logs`}
        className={`overflow-auto rounded-panel border border-line bg-side py-2.5 font-mono text-xs leading-[1.7] ${height}`}
      >
        {!live && !filter.build && (
          <div className="px-3.5 text-faint">
            Release {release}, kept history
          </div>
        )}
        {stream.error &&
          (service.stopped_at && release === active && !filter.build ? (
            <div className="px-3.5 text-mut">
              Stopped: no container runs. Start it to see its output.
            </div>
          ) : deploying && release === deploying.number && !filter.build ? (
            <div className="px-3.5 text-mut">
              Waiting for the container to start.
            </div>
          ) : (
            <div className="px-3.5 text-fail">{stream.error}</div>
          ))}
        {shown.length === 0 && !stream.error && (
          <div className="px-3.5 text-mut">
            {release === null
              ? "Not deployed yet."
              : stream.live
                ? "Waiting for output."
                : filter.query || filter.errors
                  ? "No lines match."
                  : "No output."}
          </div>
        )}
        {shown.map((line) => (
          <LogRow key={line.key} line={line} />
        ))}
        {stream.live && shown.length > 0 && filter.follow && (
          <div className="px-3.5 text-faint">
            <span className="text-ok">●</span> following
          </div>
        )}
      </div>
    </div>
  );
}

/**
 * One log line, with WARN and ERROR words colored. A line the Errors only
 * filter keeps (stderr, or error words) gets a red rule and tint, so
 * errors stand out without the filter.
 */
export const LogRow = memo(function LogRow({ line }: { line: LogLine }) {
  const error = line.stream === "stderr" || errorWords.test(line.text);
  const parts = line.text.split(/\b(WARN(?:ING)?|ERROR|FATAL|PANIC)\b/);
  return (
    <div
      // Long lines wrap (under their time) instead of running off the edge.
      className={`border-l-2 pr-3 pl-[calc(0.75rem+9ch)] -indent-[9ch] break-words whitespace-pre-wrap ${error ? "border-fail bg-fail-bg" : "border-transparent"}`}
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
