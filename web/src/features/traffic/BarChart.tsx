import { memo, type PointerEvent, useMemo, useState } from "react";
import { clock, day } from "../../ui/format";
import { type Bar, niceMax } from "./series";

type Props = {
  /** What is counted, also the accessible name: "Requests". */
  title: string;
  /** What one bar covers: "per 10 minutes". */
  per: string;
  bars: Bar[];
  /** The time span shown (ms) and one bar's width. */
  from: number;
  to: number;
  bucketMs: number;
};

// The plot's own coordinates; the SVG stretches to its box.
const W = 1000;
const H = 100;

const count = (n: number) => n.toLocaleString("en-US");

/**
 * Requests over time as thin bars, with the 4xx and then the 5xx part of
 * each stacked from the baseline in their status colours, and a legend
 * naming them. A crosshair reads out the bar under the pointer or the
 * arrow keys. Static: no animation, no chart library.
 */
export const BarChart = memo(function BarChart({
  title,
  per,
  bars,
  from,
  to,
  bucketMs,
}: Props) {
  const span = Math.max(1, to - from);
  const top = useMemo(
    () => niceMax(Math.max(1, ...bars.map((b) => b.total))),
    [bars],
  );
  const xOf = (t: number) => ((t - from) / span) * W;
  const yOf = (v: number) => H - (Math.min(v, top) / top) * H;
  // A 2px gap between bars at typical widths, never wider than the bar.
  const width = (bucketMs / span) * W;
  const gap = Math.min(width / 2, 2);

  const [active, setActive] = useState<number | null>(null);
  const bar = active === null ? undefined : bars[active];
  const reading = bar ?? bars.at(-1);
  // Days on the axis past a day (from is aligned down by up to a bar).
  const days = span > 86_400_000 + bucketMs;
  const time = (t: number) => {
    const iso = new Date(t).toISOString();
    return days ? `${day(iso)} ${clock(iso)}` : clock(iso);
  };
  const tick = (t: number) => {
    const iso = new Date(t).toISOString();
    return days ? day(iso) : clock(iso);
  };
  const say = (b: Bar) =>
    `${count(b.total)} requests, ${count(b.client)} 4xx, ${count(b.server)} 5xx at ${time(b.t)}`;

  const onPointer = (event: PointerEvent<HTMLDivElement>) => {
    if (!bars.length) return;
    const box = event.currentTarget.getBoundingClientRect();
    const t = from + ((event.clientX - box.left) / box.width) * span;
    let best = 0;
    bars.forEach((b, i) => {
      const d = Math.abs(b.t + bucketMs / 2 - t);
      if (d < Math.abs((bars[best]?.t ?? 0) + bucketMs / 2 - t)) best = i;
    });
    setActive(best);
  };

  return (
    <figure className="flex min-w-0 flex-col gap-1.5">
      <figcaption className="flex flex-wrap items-baseline gap-x-3">
        <span className="font-medium">{title}</span>
        <span className="text-xs text-mut">{per}</span>
        <span className="ml-auto flex gap-3 text-xs text-mut">
          <Swatch className="bg-info/55">ok</Swatch>
          <Swatch className="bg-warn">4xx</Swatch>
          <Swatch className="bg-fail">5xx</Swatch>
        </span>
      </figcaption>
      <div className="grid grid-cols-[44px_minmax(0,1fr)] gap-x-2">
        <div className="relative text-right text-2xs text-faint tabular-nums">
          <span className="absolute top-0 right-0 -translate-y-1/2">
            {count(top)}
          </span>
          <span className="absolute top-1/2 right-0 -translate-y-1/2">
            {count(top / 2)}
          </span>
          <span className="absolute right-0 bottom-0 translate-y-1/2">0</span>
        </div>
        {/* Arrow keys step through the bars like a slider over time. */}
        <div
          tabIndex={0}
          role="slider"
          aria-label={
            reading ? `${title}: ${say(reading)}` : `${title}: no requests`
          }
          aria-valuemin={0}
          aria-valuemax={Math.max(0, bars.length - 1)}
          aria-valuenow={active ?? Math.max(0, bars.length - 1)}
          aria-valuetext={reading ? say(reading) : "no requests"}
          className="relative h-[104px] outline-offset-4"
          onPointerMove={onPointer}
          onPointerLeave={() => setActive(null)}
          onBlur={() => setActive(null)}
          onKeyDown={(event) => {
            if (!bars.length) return;
            const end = bars.length - 1;
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
            className="absolute inset-0 size-full"
            aria-hidden
          >
            {bars.map((b) => {
              const x = xOf(b.t) + gap / 2;
              const w = Math.max(width - gap, 0.5);
              const errors = b.client + b.server;
              return (
                <g key={b.t}>
                  <rect
                    x={x}
                    y={yOf(b.total)}
                    width={w}
                    height={H - yOf(b.total)}
                    className="fill-info"
                    fillOpacity={0.55}
                  />
                  {errors > 0 && (
                    <rect
                      x={x}
                      y={yOf(errors)}
                      width={w}
                      height={H - yOf(errors)}
                      className="fill-warn"
                    />
                  )}
                  {b.server > 0 && (
                    <rect
                      x={x}
                      y={yOf(b.server)}
                      width={w}
                      height={H - yOf(b.server)}
                      className="fill-fail"
                    />
                  )}
                </g>
              );
            })}
          </svg>
          {!bars.length && (
            <p className="absolute inset-0 grid place-items-center text-xs text-mut">
              No requests through Dock's proxy yet. Counts arrive every minute;
              visitors come from the browser instead.
            </p>
          )}
          {bar && (
            <>
              <div
                className="absolute inset-y-0 w-px bg-edge"
                style={{ left: `${(xOf(bar.t + bucketMs / 2) / W) * 100}%` }}
              />
              <div
                className="absolute -top-1 z-10 rounded-ctl border border-edge bg-panel px-1.5 py-0.5 text-xs whitespace-nowrap tabular-nums"
                style={
                  xOf(bar.t) / W > 0.6
                    ? {
                        right: `${(1 - xOf(bar.t) / W) * 100}%`,
                        marginRight: 8,
                      }
                    : { left: `${(xOf(bar.t) / W) * 100}%`, marginLeft: 8 }
                }
              >
                <b className="font-semibold">{count(bar.total)}</b> requests
                {bar.client > 0 && (
                  <span className="text-warn"> {count(bar.client)} 4xx</span>
                )}
                {bar.server > 0 && (
                  <span className="text-fail"> {count(bar.server)} 5xx</span>
                )}{" "}
                <span className="text-mut">{time(bar.t)}</span>
              </div>
            </>
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

function Swatch({
  className,
  children,
}: {
  className: string;
  children: string;
}) {
  return (
    <span className="flex items-center gap-1">
      <i className={`inline-block size-2 rounded-xs ${className}`} />
      {children}
    </span>
  );
}
