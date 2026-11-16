import { memo, type PointerEvent, useMemo, useState } from "react";
import { clock, day } from "../../ui/format";
import { downsample, type Point, summary } from "./series";

type Props = {
  /** What is plotted, also the accessible name: "CPU". */
  title: string;
  points: Point[];
  /** The time span shown (ms); points outside it are dropped. */
  from: number;
  to: number;
  /** Seconds between points, for bucketing and gaps. */
  resolution: number;
  /** Top of the y axis, like 100 for percent or total memory. */
  max: number;
  /** Label of the top gridline when it means more than the value: "limit". */
  maxNote?: string;
  format: (v: number) => string;
  /**
   * Moments marked with a dashed line, like deploys: label is what
   * hovering their point says, short what stands at the line's top in a
   * roomy chart ("r44").
   */
  markers?: readonly { t: number; label: string; short?: string }[];
  /** Values marked with a dashed line across, like thresholds. */
  guides?: readonly { v: number; label: string; tone: "ok" | "fail" }[];
  /** What an empty chart says. */
  empty?: string;
  /**
   * The Analytics tab's look: taller and blue, with a gridline and value
   * every quarter, five dates, the markers' short labels, and no dot on
   * the latest point. Without it, the compact look of small multiples.
   */
  roomy?: boolean;
};

// The plot's own coordinates; the SVG stretches to its box and strokes
// don't scale, so lines stay 1.5px at any width.
const W = 1000;
const H = 100;

/**
 * One series over time as a small static line chart: hairline grid, a
 * line with a faint wash under it, a crosshair with the nearest point's
 * value on hover or with the arrow keys. Breaks the line where points are
 * missing. No animation, no chart library.
 */
export const LineChart = memo(function LineChart({
  title,
  points,
  from,
  to,
  resolution,
  max,
  maxNote,
  format,
  markers,
  guides,
  empty = "No points yet. One is recorded every minute.",
  roomy = false,
}: Props) {
  const span = Math.max(1, to - from);
  const { xOf, yOf } = useMemo(() => {
    const top = Math.max(max, 1e-9);
    return {
      xOf: (t: number) => ((t - from) / span) * W,
      yOf: (v: number) => H - (Math.min(Math.max(v, 0), top) / top) * H,
    };
  }, [from, span, max]);

  // Drawn points (gaps removed) and the SVG paths, per data change only.
  const { shown, line, area } = useMemo(() => {
    const series = downsample(points, from, to, resolution * 1000);
    const shown = series.filter((p): p is Point => p !== null);
    let line = "";
    let area = "";
    let run: Point[] = [];
    const flush = () => {
      const [first] = run;
      const last = run.at(-1);
      if (!first || !last) return;
      const xy = run.map((p) => `${xOf(p.t)},${yOf(p.v)}`);
      line += `M${xy.join("L")}`;
      area += `M${xOf(first.t)},${H}L${xy.join("L")}L${xOf(last.t)},${H}Z`;
      run = [];
    };
    for (const p of series) {
      if (p) run.push(p);
      else flush();
    }
    flush();
    return { shown, line, area };
  }, [points, from, to, resolution, xOf, yOf]);

  const stats = useMemo(() => summary(points), [points]);
  const [active, setActive] = useState<number | null>(null);
  const point = active === null ? undefined : shown[active];
  const last = shown.at(-1);
  /** What a screen reader hears: the active point, or the latest. */
  const reading = point ?? last;
  // A day's point is the day; shorter ones say when in it.
  const time = (t: number) =>
    resolution >= 86_400
      ? day(new Date(t).toISOString())
      : span > rangeDay
        ? `${day(new Date(t).toISOString())} ${clockOf(t)}`
        : clockOf(t);
  // Axis labels: hours up to two days (a day's quarters would all read
  // the same date), dates past that.
  const tick = (t: number) =>
    span > 2 * rangeDay ? day(new Date(t).toISOString()) : clockOf(t);

  const described = stats.last
    ? `${title}: now ${format(stats.last.v)}, average ${format(stats.avg ?? 0)}, peak ${format(stats.peak?.v ?? 0)}`
    : `${title}: no data yet`;

  // The markers in the hovered point's bucket, said with its value.
  const step = resolution * 1000;
  const marked = point
    ? (markers ?? []).filter((m) => m.t >= point.t && m.t < point.t + step)
    : [];
  // Grid fractions from the top; dates as fractions across.
  const rows = roomy ? steps(max) : [0, 0.5, 1];
  const dates = roomy ? [0, 0.25, 0.5, 0.75] : [0, 0.5];
  // Short labels that would overlap the one before them are left out.
  const labelled = useMemo(() => {
    if (!roomy) return new Set<number>();
    const out = new Set<number>();
    let prev = -1;
    for (const [i, m] of (markers ?? []).entries()) {
      const x = xOf(m.t) / W;
      if (m.short && x - prev > 0.04) {
        out.add(i);
        prev = x;
      }
    }
    return out;
  }, [roomy, markers, xOf]);

  const onPointer = (event: PointerEvent<HTMLDivElement>) => {
    if (!shown.length) return;
    const box = event.currentTarget.getBoundingClientRect();
    const t = from + ((event.clientX - box.left) / box.width) * span;
    setActive(nearest(shown, t));
  };

  return (
    <figure className="flex min-w-0 flex-col gap-1.5">
      <figcaption className="flex flex-wrap items-baseline gap-x-3">
        <span className="font-medium">{title}</span>
        {stats.last && (
          <span className="text-xs text-mut tabular-nums">
            now <span className="text-fg">{format(stats.last.v)}</span> · avg{" "}
            {format(stats.avg ?? 0)} · peak {format(stats.peak?.v ?? 0)}
          </span>
        )}
      </figcaption>
      <div
        // The top tick's label is centred on the top line: room above it,
        // so it never runs into the caption.
        className={`grid grid-cols-[44px_minmax(0,1fr)] gap-x-2 ${roomy && labelled.size ? "mt-3" : "mt-2"}`}
      >
        <div className="relative text-right text-2xs text-faint tabular-nums">
          {rows.map((f) => (
            <span
              key={f}
              className="absolute right-0 -translate-y-1/2"
              style={{ top: `${f * 100}%` }}
            >
              {format(Number((max * (1 - f)).toPrecision(12)))}
            </span>
          ))}
          {maxNote && <span className="absolute top-2 right-0">{maxNote}</span>}
        </div>
        {/* Arrow keys step through the points like a slider over time. */}
        <div
          tabIndex={0}
          role="slider"
          aria-label={described}
          aria-valuemin={0}
          aria-valuemax={Math.max(0, shown.length - 1)}
          aria-valuenow={active ?? Math.max(0, shown.length - 1)}
          aria-valuetext={
            reading ? `${format(reading.v)} at ${time(reading.t)}` : "no data"
          }
          className={`relative outline-offset-4 ${roomy ? "h-[150px] text-info" : "h-[104px]"}`}
          onPointerMove={onPointer}
          onPointerLeave={() => setActive(null)}
          onBlur={() => setActive(null)}
          onKeyDown={(event) => {
            if (!shown.length) return;
            const end = shown.length - 1;
            const at = active ?? end;
            const next = {
              ArrowLeft: Math.max(0, at - 1),
              ArrowRight: Math.min(end, at + 1),
              Home: 0,
              End: end,
            }[event.key];
            if (next !== undefined) {
              event.preventDefault();
              setActive(next);
            } else if (event.key === "Escape") setActive(null);
          }}
        >
          {rows.map((f) => (
            <div
              key={f}
              className={`absolute inset-x-0 border-t ${f === 0 || f === 1 ? "border-line" : "border-soft"}`}
              style={{ top: `${f * 100}%` }}
            />
          ))}
          <svg
            viewBox={`0 0 ${W} ${H}`}
            preserveAspectRatio="none"
            className="absolute inset-0 size-full overflow-visible"
            aria-hidden
          >
            <path d={area} fill="currentColor" fillOpacity={0.07} />
            <path
              d={line}
              fill="none"
              stroke="currentColor"
              strokeWidth={1.5}
              strokeLinejoin="round"
              strokeLinecap="round"
              vectorEffect="non-scaling-stroke"
            />
          </svg>
          {guides?.map((g) => (
            <span
              key={g.label}
              className={`absolute inset-x-0 border-t border-dashed ${g.tone === "ok" ? "border-ok/50" : "border-fail/50"}`}
              style={{ top: `${(yOf(g.v) / H) * 100}%` }}
            >
              <span
                // At the left end: the right end holds "now" and the newest
                // deploy markers, which the label would cover.
                className={`absolute left-1 -translate-y-full text-2xs ${g.tone === "ok" ? "text-ok" : "text-fail"}`}
              >
                {g.label}
              </span>
            </span>
          ))}
          {markers?.map((m, i) => (
            <span
              key={`${m.t} ${m.label}`}
              title={m.label}
              className="absolute -top-1 bottom-0 w-px border-l border-dashed border-dash"
              style={{ left: `${(xOf(m.t) / W) * 100}%` }}
            >
              {labelled.has(i) && (
                <span
                  className={`absolute -top-3.5 text-2xs text-faint whitespace-nowrap ${xOf(m.t) / W > 0.85 ? "right-1" : "left-1"}`}
                >
                  {m.short}
                </span>
              )}
            </span>
          ))}
          {!shown.length && (
            <p className="absolute inset-0 grid place-items-center text-xs text-mut">
              {empty}
            </p>
          )}
          {point ? (
            <>
              <div
                className="absolute inset-y-0 w-px bg-edge"
                style={{ left: `${(xOf(point.t) / W) * 100}%` }}
              />
              <Dot x={xOf(point.t) / W} y={yOf(point.v) / H} />
              <div
                className="absolute -top-1 z-10 rounded-ctl border border-edge bg-panel px-1.5 py-0.5 text-xs whitespace-nowrap text-fg tabular-nums"
                style={
                  xOf(point.t) / W > 0.6
                    ? {
                        right: `${(1 - xOf(point.t) / W) * 100}%`,
                        marginRight: 8,
                      }
                    : { left: `${(xOf(point.t) / W) * 100}%`, marginLeft: 8 }
                }
              >
                <b className="font-semibold text-fg">{format(point.v)}</b>{" "}
                <span className="text-mut">{time(point.t)}</span>
                {marked.map((m) => (
                  <span key={`${m.t} ${m.label}`} className="block text-sec">
                    {m.label}
                  </span>
                ))}
              </div>
            </>
          ) : (
            last && !roomy && <Dot x={xOf(last.t) / W} y={yOf(last.v) / H} />
          )}
        </div>
        <div />
        <div className="relative h-4 text-2xs text-faint tabular-nums">
          {dates.map((f) => (
            <span
              key={f}
              className={`absolute ${f ? "-translate-x-1/2" : ""}`}
              style={{ left: `${f * 100}%` }}
            >
              {tick(from + span * f)}
            </span>
          ))}
          <span className="absolute right-0">now</span>
        </div>
      </div>
    </figure>
  );
});

const rangeDay = 86_400_000 + 60_000;

/**
 * A roomy chart's gridlines, as fractions from the top: max cut into 4, 5
 * or 2 steps of 1, 2 or 5 times a power of ten (0 to 20 by 5, 0 to 5 by
 * 1), or quarters when it can't be.
 */
function steps(max: number) {
  for (const n of [4, 5, 2]) {
    const step = max / n;
    const lead = step / 10 ** Math.floor(Math.log10(step));
    if ([1, 2, 5].some((x) => Math.abs(lead - x) < 1e-9)) {
      return Array.from({ length: n + 1 }, (_, i) => i / n);
    }
  }
  return [0, 0.25, 0.5, 0.75, 1];
}

const clockOf = (t: number) => clock(new Date(t).toISOString());

/** An 8px marker with a black ring, centered on a point (fractions). */
function Dot({ x, y }: { x: number; y: number }) {
  return (
    <span
      className="absolute size-2 -translate-x-1/2 -translate-y-1/2 rounded-full bg-white ring-2 ring-black"
      style={{ left: `${x * 100}%`, top: `${y * 100}%` }}
    />
  );
}

/** Index of the point closest in time to `t`; points are sorted. */
function nearest(points: Point[], t: number) {
  let lo = 0;
  let hi = points.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if ((points[mid]?.t ?? 0) < t) lo = mid + 1;
    else hi = mid;
  }
  const before = points[lo - 1];
  const at = points[lo];
  if (before && at && t - before.t < at.t - t) return lo - 1;
  return lo;
}
