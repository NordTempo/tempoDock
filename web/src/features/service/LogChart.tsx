import { type KeyboardEvent, useMemo, useState } from "react";
import { clock } from "../../ui/format";
import type { LogLevel } from "./logLevel";

const BARS = 60;

/** A line as the chart needs it: when, and how logLevel reads it. */
export type ChartLine = { at: number | null; level: LogLevel };

/**
 * The logs tab's volume over time: a bar per slice of the lines' span,
 * warnings and errors stacked on top in their colors. `seen` outlines the
 * time range on screen; a click on a bar jumps the list to its first line.
 * From the keyboard the bars are one stop: arrows move between them,
 * Enter jumps.
 */
export function LogChart({
  lines,
  seen,
  onJump,
}: {
  /** Oldest first. */
  lines: readonly ChartLine[];
  seen: readonly [from: number, to: number] | null;
  onJump: (at: number) => void;
}) {
  const chart = useMemo(() => {
    let start = Number.POSITIVE_INFINITY;
    let end = Number.NEGATIVE_INFINITY;
    for (const { at } of lines) {
      if (at === null) continue;
      if (at < start) start = at;
      if (at > end) end = at;
    }
    if (start > end) return null;
    const span = end - start + 1;
    const bars = Array.from({ length: BARS }, () => ({
      other: 0,
      warn: 0,
      error: 0,
    }));
    for (const { at, level } of lines) {
      if (at === null) continue;
      const bar = bars[Math.floor(((at - start) / span) * BARS)];
      if (bar) bar[level ?? "other"]++;
    }
    const most = Math.max(...bars.map((b) => b.other + b.warn + b.error));
    return { start, span, bars, most };
  }, [lines]);
  // The bar the keyboard is on; the newest until it moves.
  const [current, setCurrent] = useState(BARS - 1);
  if (!chart) return null;
  const { start, span, bars, most } = chart;
  const height = (n: number) => `${(n / most) * 100}%`;
  const at = (i: number) => start + (span * i) / BARS;
  const step = (e: KeyboardEvent<HTMLButtonElement>, i: number) => {
    const next =
      e.key === "ArrowLeft"
        ? i - 1
        : e.key === "ArrowRight"
          ? i + 1
          : e.key === "Home"
            ? 0
            : e.key === "End"
              ? BARS - 1
              : null;
    if (next === null || next < 0 || next >= BARS) return;
    e.preventDefault();
    setCurrent(next);
    const bar = e.currentTarget.parentElement?.children[next];
    if (bar instanceof HTMLElement) bar.focus();
  };
  const frame = seen && {
    left: `${((seen[0] - start) / span) * 100}%`,
    width: `max(4px, ${((seen[1] - seen[0]) / span) * 100}%)`,
  };
  return (
    <div className="pt-2.5 pb-1.5">
      <fieldset
        aria-label="Lines over time"
        className="relative flex h-11 min-w-0 items-end gap-px"
      >
        {bars.map((b, i) => {
          const label = `${clock(new Date(at(i)).toISOString())}: ${b.other + b.warn + b.error} lines${b.warn ? `, ${b.warn} warn` : ""}${b.error ? `, ${b.error} error` : ""}`;
          return (
            <button
              // Bars are positions in time, never reordered.
              key={i}
              type="button"
              tabIndex={i === current ? 0 : -1}
              aria-label={label}
              title={label}
              onKeyDown={(e) => step(e, i)}
              onFocus={() => setCurrent(i)}
              onClick={() => onJump(at(i))}
              className="flex h-full min-w-0 flex-1 flex-col-reverse hover:bg-hover"
            >
              <span className="bg-edge" style={{ height: height(b.other) }} />
              <span className="bg-warn" style={{ height: height(b.warn) }} />
              <span className="bg-fail" style={{ height: height(b.error) }} />
            </button>
          );
        })}
        {frame && (
          <span
            aria-hidden
            className="pointer-events-none absolute -top-1 -bottom-0.5 rounded-ctl border border-mut"
            style={frame}
          />
        )}
      </fieldset>
      <div className="mt-1 flex justify-between font-mono text-tag text-mut">
        {[0, 1, 2, 3, 4].map((k) => (
          <span key={k}>
            {clock(new Date(start + (span * k) / 4).toISOString())}
          </span>
        ))}
      </div>
    </div>
  );
}
