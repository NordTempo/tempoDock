import {
  type KeyboardEvent,
  type PointerEvent,
  useMemo,
  useState,
} from "react";
import { clock, clockSec } from "../../ui/format";
import type { LogLevel } from "./logLevel";

const BARS = 60;

/** A line as the chart needs it: when, and how logLevel reads it. */
export type ChartLine = { at: number | null; level: LogLevel };

/** The bars a drag (or Shift and arrows) has covered so far. */
type Pick = { anchor: number; head: number };

/**
 * The logs tab's volume over time: a bar per slice of the lines' span,
 * warnings and errors stacked on top in their colors. `seen` outlines the
 * time range on screen. A click on a bar jumps the list to its first line;
 * a drag across bars zooms into that range. From the keyboard the bars
 * are one stop: arrows move between them, Shift and arrows pick a range,
 * Enter jumps or zooms, Escape drops the pick.
 */
export function LogChart({
  lines,
  seen,
  onJump,
  onRange,
}: {
  /** Oldest first. */
  lines: readonly ChartLine[];
  seen: readonly [from: number, to: number] | null;
  onJump: (at: number) => void;
  /** From inclusive, to exclusive, in ms. */
  onRange: (from: number, to: number) => void;
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
  const [pick, setPick] = useState<Pick | null>(null);
  if (!chart) return null;
  const { start, span, bars, most } = chart;
  const height = (n: number) => `${(n / most) * 100}%`;
  const at = (i: number) => start + (span * i) / BARS;
  // Seconds once the chart covers less than ten minutes (a zoomed range).
  const time = (ms: number) =>
    (span < 600_000 ? clockSec : clock)(new Date(ms).toISOString());

  // A pick of one bar jumps to it; a wider one zooms into its bars.
  const finish = (p: Pick) => {
    setPick(null);
    if (p.anchor === p.head) return onJump(at(p.anchor));
    onRange(
      Math.floor(at(Math.min(p.anchor, p.head))),
      Math.ceil(at(Math.max(p.anchor, p.head) + 1)),
    );
  };
  // The bar under the pointer, wherever the drag has wandered.
  const barAt = (e: PointerEvent<HTMLButtonElement>) => {
    const box = e.currentTarget.parentElement?.getBoundingClientRect();
    if (!box) return 0;
    const i = Math.floor(((e.clientX - box.left) / box.width) * BARS);
    return Math.min(BARS - 1, Math.max(0, i));
  };
  const step = (e: KeyboardEvent<HTMLButtonElement>, i: number) => {
    if (e.key === "Escape" && pick) {
      e.preventDefault();
      setPick(null);
      return;
    }
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
    setPick(e.shiftKey ? { anchor: pick?.anchor ?? i, head: next } : null);
    setCurrent(next);
    const bar = e.currentTarget.parentElement?.children[next];
    if (bar instanceof HTMLElement) bar.focus();
  };
  const frame = seen && {
    left: `${((seen[0] - start) / span) * 100}%`,
    width: `max(4px, ${((seen[1] - seen[0]) / span) * 100}%)`,
  };
  const picked = pick && {
    left: `${(Math.min(pick.anchor, pick.head) / BARS) * 100}%`,
    width: `${((Math.abs(pick.anchor - pick.head) + 1) / BARS) * 100}%`,
  };
  return (
    <div className="pt-2.5 pb-1.5">
      <fieldset
        aria-label="Lines over time"
        className="relative flex h-11 min-w-0 select-none items-end gap-px"
      >
        {bars.map((b, i) => {
          const label = `${time(at(i))}: ${b.other + b.warn + b.error} lines${b.warn ? `, ${b.warn} warn` : ""}${b.error ? `, ${b.error} error` : ""}`;
          return (
            <button
              // Bars are positions in time, never reordered.
              key={i}
              type="button"
              tabIndex={i === current ? 0 : -1}
              aria-label={label}
              title={`${label}. Click to jump, drag to zoom.`}
              onKeyDown={(e) => step(e, i)}
              onFocus={() => setCurrent(i)}
              onPointerDown={(e) => {
                if (e.button !== 0) return;
                // Moves outside the bar still reach it until release.
                e.currentTarget.setPointerCapture(e.pointerId);
                setPick({ anchor: i, head: i });
              }}
              onPointerMove={(e) => {
                const head = pick && barAt(e);
                if (pick && head !== null && head !== pick.head) {
                  setPick({ ...pick, head });
                }
              }}
              onPointerUp={() => pick && finish(pick)}
              onPointerCancel={() => setPick(null)}
              // A pointer finishes on release; this is Enter and Space.
              onClick={(e) => {
                if (e.detail === 0) finish(pick ?? { anchor: i, head: i });
              }}
              className="flex h-full min-w-0 flex-1 flex-col-reverse hover:bg-hover"
            >
              <span className="bg-edge" style={{ height: height(b.other) }} />
              <span className="bg-warn" style={{ height: height(b.warn) }} />
              <span className="bg-fail" style={{ height: height(b.error) }} />
            </button>
          );
        })}
        {frame && !pick && (
          <span
            aria-hidden
            className="pointer-events-none absolute -top-1 -bottom-0.5 rounded-ctl border border-mut"
            style={frame}
          />
        )}
        {picked && (
          <span
            aria-hidden
            className="pointer-events-none absolute -top-1 -bottom-0.5 rounded-ctl border border-fg bg-fg/10"
            style={picked}
          />
        )}
      </fieldset>
      <div className="mt-1 flex justify-between font-mono text-tag text-mut">
        {[0, 1, 2, 3, 4].map((k) => (
          <span key={k}>{time(start + (span * k) / 4)}</span>
        ))}
      </div>
    </div>
  );
}
