import { memo, useEffect, useMemo, useRef } from "react";
import { type LogStream, useBuildLog, useServiceLogs } from "../../data/live";
import { useReleases, useService } from "../../data/queries";
import type { LogLine, ServiceRef } from "../../data/types";
import { useShortcuts } from "../../shell/keys";
import { clockSec } from "../../ui/format";

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
};

export const noFilter: LogFilter = {
  release: null,
  build: false,
  errors: false,
  query: "",
  follow: true,
};

/** Rendered lines are capped; the stream keeps more for filtering. */
const VISIBLE = 500;

const errorWords = /\b(error|fatal|panic|exception)\b/i;

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
 * release, the build), a text or /regex/ filter, errors only and follow.
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
  const app = useServiceLogs(ref, filter.build ? null : release);
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

  // The deploy in flight, the serving release, a picked older one, the build.
  const tabs = [
    ...(deploying
      ? [{ n: deploying.number, label: `r${deploying.number}` }]
      : []),
    ...(active ? [{ n: active, label: `r${active} serving` }] : []),
    ...(filter.release &&
    filter.release !== active &&
    filter.release !== deploying?.number
      ? [{ n: filter.release, label: `r${filter.release}` }]
      : []),
  ];

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-x-3.5 gap-y-2">
        <fieldset aria-label="Source" className="seg font-mono text-xs">
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
        <label className="flex flex-[1_1_220px] items-center gap-1.5">
          <span className="text-mut">Filter</span>
          <input
            ref={input}
            value={filter.query}
            onChange={(e) => onFilter({ ...filter, query: e.target.value })}
            onKeyDown={(e) => {
              if (e.key === "Escape") e.currentTarget.blur();
            }}
            placeholder="text or /regex/"
            className="field min-w-0 flex-1 py-[5px] font-mono"
          />
        </label>
        <label className="flex items-center gap-1.5">
          <input
            type="checkbox"
            checked={filter.errors}
            onChange={(e) => onFilter({ ...filter, errors: e.target.checked })}
          />
          Errors only
        </label>
        <label className="flex items-center gap-1.5">
          <input
            type="checkbox"
            checked={filter.follow}
            onChange={(e) => onFilter({ ...filter, follow: e.target.checked })}
          />
          Follow <kbd>F</kbd>
        </label>
      </div>
      <div
        ref={box}
        role="log"
        aria-label={`${filter.build ? "build" : `r${release}`} logs`}
        className={`overflow-auto border border-line py-2.5 font-mono text-xs leading-[1.7] ${height}`}
      >
        {stream.error &&
          (deploying && release === deploying.number && !filter.build ? (
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
          <div className="px-3.5 text-mut"> following</div>
        )}
      </div>
    </div>
  );
}

/** One log line, with WARN and ERROR words colored and errors tinted. */
export const LogRow = memo(function LogRow({ line }: { line: LogLine }) {
  const error = /\b(ERROR|FATAL|PANIC)\b/.test(line.text);
  const parts = line.text.split(/\b(WARN(?:ING)?|ERROR|FATAL|PANIC)\b/);
  return (
    <div className={`px-3.5 whitespace-pre ${error ? "bg-fail-bg" : ""}`}>
      {line.time && <span className="text-mut">{clockSec(line.time)} </span>}
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
