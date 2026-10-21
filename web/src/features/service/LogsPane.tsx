import { useMemo, useRef } from "react";
import { useServiceLogs } from "../../data/queries";
import type { LogLevel, LogLine } from "../../data/types";
import { useShortcut } from "../../shell/keys";
import { clockPrecise } from "../../ui/format";

export type LogFilter = {
  /** "all" app output, one release's app output, or build output. */
  release: "all" | "build" | number;
  warnUp: boolean;
  query: string;
};

export const noFilter: LogFilter = { release: "all", warnUp: false, query: "" };

/** Rendered lines are capped; the cache keeps more for filtering. */
const VISIBLE = 500;

const levelClass: Record<LogLevel, string> = {
  debug: "text-dim",
  info: "",
  warn: "text-warn",
  error: "text-bad",
};

function matches(line: LogLine, f: LogFilter, query: string) {
  if (f.release === "build" ? line.stream !== "build" : line.stream !== "app") {
    return false;
  }
  if (typeof f.release === "number" && line.release !== f.release) return false;
  if (f.warnUp && line.level !== "warn" && line.level !== "error") return false;
  return !query || line.message.toLowerCase().includes(query);
}

/** Live log tail, newest first, with release, level and text filters. */
export function LogsPane({
  serviceId,
  inFlight,
  filter,
  onFilter,
  className,
}: {
  serviceId: string;
  /** Release being deployed; its lines are highlighted. */
  inFlight: number | null;
  filter: LogFilter;
  onFilter: (next: LogFilter) => void;
  className: string;
}) {
  const lines = useServiceLogs(serviceId);
  const input = useRef<HTMLInputElement>(null);
  useShortcut("/", () => input.current?.focus());

  const recentReleases = useMemo(() => {
    const seen = new Set<number>();
    for (const line of lines) {
      if (line.stream === "app") seen.add(line.release);
      if (seen.size === 2) break;
    }
    return [...seen];
  }, [lines]);

  const shown = useMemo(() => {
    const query = filter.query.toLowerCase();
    const out: LogLine[] = [];
    for (const line of lines) {
      if (matches(line, filter, query)) out.push(line);
      if (out.length === VISIBLE) break;
    }
    return out;
  }, [lines, filter]);

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

  return (
    <div>
      <div className="flex items-baseline gap-3.5 pb-1.5 text-xs text-dim">
        {chip("all releases", filter.release === "all", { release: "all" })}
        {recentReleases.map((r) =>
          chip(`r${r}`, filter.release === r, { release: r }),
        )}
        {chip("build", filter.release === "build", { release: "build" })}
        {chip("warn+", filter.warnUp, { warnUp: !filter.warnUp })}
        <span className="flex-1" />
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
        {shown.length === 0 && <span className="text-dim">No lines.</span>}
        {shown.map((line) => (
          <div key={line.id}>
            <span className="text-faint">{clockPrecise(line.at)}</span>{" "}
            <span
              className={line.release === inFlight ? "text-run" : "text-dim"}
            >
              r{line.release}
            </span>{" "}
            <span className={levelClass[line.level]}>{line.level}</span>{" "}
            {line.message}
          </div>
        ))}
      </div>
    </div>
  );
}
