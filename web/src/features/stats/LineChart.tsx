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
  /** Moments marked with a dashed line, like deploys; label is their title. */
  markers?: readonly { t: number; label: string }[];
  /** What an empty chart says. */
  empty?: string;
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
  empty = "No points yet. One is recorded every minute.",
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
  const time = (t: number) =>
    span > rangeDay
      ? `${day(new Date(t).toISOString())} ${clockOf(t)}`
      : clockOf(t);
  const tick = (t: number) =>
    span > rangeDay ? day(new Date(t).toISOString()) : clockOf(t);

  const described = stats.last
    ? `${title}: now ${format(stats.last.v)}, average ${format(stats.avg ?? 0)}, peak ${format(stats.peak?.v ?? 0)}`
    : `${title}: no data yet`;

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
      <div className="grid grid-cols-[44px_minmax(0,1fr)] gap-x-2">
        <div className="relative text-right text-2xs text-faint tabular-nums">
          <span className="absolute top-0 right-0 -translate-y-1/2">
            {format(max)}
          </span>
          {maxNote && <span className="absolute top-2 right-0">{maxNote}</span>}
          <span className="absolute top-1/2 right-0 -translate-y-1/2">
            {format(max / 2)}
          </span>
          <span className="absolute right-0 bottom-0 translate-y-1/2">
            {format(0)}
          </span>
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
          className="relative h-[104px] outline-offset-4"
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
          <div className="absolute inset-x-0 top-0 border-t border-line" />
          <div className="absolute inset-x-0 top-1/2 border-t border-soft" />
          <div className="absolute inset-x-0 bottom-0 border-t border-line" />
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
          {markers?.map((m) => (
            <span
              key={`${m.t} ${m.label}`}
              title={m.label}
              className="absolute -top-1 bottom-0 w-px border-l border-dashed border-dash"
              style={{ left: `${(xOf(m.t) / W) * 100}%` }}
            />
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
                className="absolute -top-1 z-10 rounded-ctl border border-edge bg-panel px-1.5 py-0.5 text-xs whitespace-nowrap tabular-nums"
                style={
                  xOf(point.t) / W > 0.6
                    ? {
                        right: `${(1 - xOf(point.t) / W) * 100}%`,
                        marginRight: 8,
                      }
                    : { left: `${(xOf(point.t) / W) * 100}%`, marginLeft: 8 }
                }
              >
                <b className="font-semibold">{format(point.v)}</b>{" "}
                <span className="text-mut">{time(point.t)}</span>
              </div>
            </>
          ) : (
            last && <Dot x={xOf(last.t) / W} y={yOf(last.v) / H} />
          )}
        </div>
        <div />
        <div className="relative h-4 text-2xs text-faint tabular-nums">
          <span className="absolute left-0">{tick(from)}</span>
          <span className="absolute left-1/2 -translate-x-1/2">
            {tick(from + span / 2)}
          </span>
          <span className="absolute right-0">now</span>
        </div>
      </div>
    </figure>
  );
});

const rangeDay = 86_400_000 + 60_000;

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
