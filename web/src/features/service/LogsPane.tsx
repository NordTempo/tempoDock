import { useMemo, useRef } from "react";
import { useBuildLog, useServiceLogs } from "../../data/live";
import { useReleases, useService } from "../../data/queries";
import type { LogLine, ServiceRef } from "../../data/types";
import { useShortcut } from "../../shell/keys";
import { clockPrecise } from "../../ui/format";

export type LogFilter = {
  /** A release's output; null follows the active release. */
  release: number | null;
  /** Build output instead of container output. */
  build: boolean;
  stderr: boolean;
  query: string;
};

export const noFilter: LogFilter = {
  release: null,
  build: false,
  stderr: false,
  query: "",
};

/** Rendered lines are capped; the stream keeps more for filtering. */
const VISIBLE = 500;

/** Live log tail, newest first, with release, stream and text filters. */
export function LogsPane({
  serviceRef: ref,
  filter,
  onFilter,
  className,
}: {
  serviceRef: ServiceRef;
  filter: LogFilter;
  onFilter: (next: LogFilter) => void;
  className: string;
}) {
  const { service, deploying } = useService(ref);
  const releases = useReleases(ref);
  const input = useRef<HTMLInputElement>(null);
  useShortcut("/", () => input.current?.focus());

  const active = service.active_release ?? null;
  const release =
    filter.release ?? (filter.build ? releases[0]?.number : active);
  const app = useServiceLogs(ref, filter.build ? null : (release ?? null));
  const build = useBuildLog(ref, filter.build ? (release ?? null) : null);
  const stream = filter.build ? build : app;

  // The deploy in flight and the last two others are one click away.
  const recent = [
    ...(deploying ? [deploying.number] : []),
    ...releases.map((r) => r.number).filter((n) => n !== active),
  ]
    .filter((n, i, all) => all.indexOf(n) === i && n !== active)
    .slice(0, 2);

  const shown = useMemo(() => {
    const query = filter.query.toLowerCase();
    const out: LogLine[] = [];
    for (const line of stream.lines) {
      if (filter.stderr && line.stream !== "stderr") continue;
      if (query && !line.text.toLowerCase().includes(query)) continue;
      out.push(line);
      if (out.length === VISIBLE) break;
    }
    return out;
  }, [stream.lines, filter]);

  const chip = (label: string, on: boolean, next: Partial<LogFilter>) => (
    <button
      key={label}
      type="button"
      aria-pressed={on}
      onClick={() => onFilter({ ...filter, ...next })}
      className={on ? "border-b border-white text-white" : "hover:text-white"}
    >
      {label}
    </button>
  );

  const what = `${filter.build ? "build of " : ""}r${release}`;
  const status = stream.live
    ? `following ${what}`
    : release
      ? `end of ${what}`
      : "";

  return (
    <div>
      <div className="flex items-baseline gap-3.5 pb-1.5 text-xs whitespace-nowrap text-dim">
        {chip(
          active ? `active r${active}` : "active",
          filter.release === null,
          { release: null },
        )}
        {recent.map((n) => chip(`r${n}`, filter.release === n, { release: n }))}
        {chip("build", filter.build, { build: !filter.build })}
        {!filter.build &&
          chip("stderr", filter.stderr, { stderr: !filter.stderr })}
        <span className="flex-1" />
        <span className="truncate">{status}</span>
        <label className="flex items-baseline gap-1">
          <span aria-hidden>/</span>
          <input
            ref={input}
            value={filter.query}
            onChange={(e) => onFilter({ ...filter, query: e.target.value })}
            onKeyDown={(e) => {
              if (e.key === "Escape") e.currentTarget.blur();
            }}
            placeholder="filter"
            aria-label="Filter log lines"
            className="w-28 bg-black text-white outline-none placeholder:text-dim focus:border-b focus:border-white"
          />
        </label>
      </div>
      <div
        className={`overflow-auto font-mono text-xs leading-[1.55] whitespace-pre ${className}`}
      >
        {stream.error && <div className="text-bad">{stream.error}</div>}
        {shown.length === 0 && !stream.error && (
          <span className="text-dim">
            {release ? "No lines." : "Not deployed yet."}
          </span>
        )}
        {shown.map((line) => (
          <div key={line.key}>
            {line.time && (
              <>
                <span className="text-faint">
                  {clockPrecise(line.time)}
                </span>{" "}
              </>
            )}
            {line.stream === "stderr" && <span className="text-bad">err </span>}
            {line.text}
          </div>
        ))}
      </div>
    </div>
  );
}
