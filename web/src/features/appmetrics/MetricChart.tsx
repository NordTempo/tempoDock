import { memo, type ReactNode, useMemo, useState } from "react";
import { clock, day } from "../../ui/format";
import type { AppMetricChart } from "./data";
import { axisFormatOf, formatOf, niceTop } from "./format";

// An app metric's chart: its lines (a value of its attribute each, or
// one) over the range, as LineChart draws one, in the compact look of
// the Metrics tab's small multiples: hairline grid, 1.5px lines, a
// crosshair with each line's value on hover or with the arrow keys. No
// chart library, no animation.

const W = 1000;
const H = 100;

/** Line colours, biggest line first; the rest are faint. */
const colors = [
  "text-info",
  "text-mem-platform",
  "text-warn",
  "text-mem-kernel",
  "text-disk-build",
];

export const MetricChart = memo(function MetricChart({
  chart: c,
  title,
  from,
  stepSeconds,
  action,
}: {
  chart: AppMetricChart;
  title: ReactNode;
  /** The range's start (ms) and step: a line's i-th value is at from + i·step. */
  from: number;
  stepSeconds: number;
  /** Beside the title: Unpin. */
  action?: ReactNode;
}) {
  const format = formatOf(c);
  const axis = axisFormatOf(c);
  const step = stepSeconds * 1000;
  const steps = c.lines[0]?.values.length ?? 0;
  const span = Math.max(1, steps * step);
  const top = useMemo(() => {
    let m = 0;
    for (const l of c.lines)
      for (const v of l.values) if (v !== null && v > m) m = v;
    // Bytes round in MiB, as they read: 20M, not 19.1M.
    return c.unit === "By" ? niceTop(m / 2 ** 20) * 2 ** 20 : niceTop(m);
  }, [c.lines, c.unit]);
  // Each line's path, broken where a step has no value.
  const paths = useMemo(
    () =>
      c.lines.map((l) => {
        let d = "";
        let pen = false;
        l.values.forEach((v, i) => {
          if (v === null) {
            pen = false;
            return;
          }
          const x = ((i + 0.5) / Math.max(1, steps)) * W;
          const y = H - (Math.max(0, v) / top) * H;
          d += `${pen ? "L" : "M"}${x},${y}`;
          pen = true;
        });
        return d;
      }),
    [c.lines, steps, top],
  );
  const [active, setActive] = useState<number | null>(null);
  const latest = (i: number) => {
    const vs = c.lines[i]?.values ?? [];
    for (let j = vs.length - 1; j >= 0; j--) {
      const v = vs[j];
      if (v !== null && v !== undefined) return v;
    }
    return null;
  };
  const one = c.lines.length === 1 ? latest(0) : null;
  const time = (i: number) => {
    const iso = new Date(from + i * step).toISOString();
    return span > 2 * 86_400_000 ? `${day(iso)} ${clock(iso)}` : clock(iso);
  };
  const tick = (f: number) => {
    const iso = new Date(from + span * f).toISOString();
    return span > 2 * 86_400_000 ? day(iso) : clock(iso);
  };
  const reading =
    active === null
      ? null
      : c.lines
          .map((l, i) => ({ l, i, v: l.values[active] }))
          .filter((x) => x.v !== null && x.v !== undefined)
          .slice(0, 6);
  const described = `${typeof title === "string" ? title : c.name}: ${
    c.lines.length === 0
      ? "no points in this range"
      : one !== null
        ? `now ${format(one)}`
        : `${c.lines.length + (c.more ?? 0)} lines`
  }`;

  return (
    <figure className="flex min-w-0 flex-col gap-1.5">
      <figcaption className="flex flex-wrap items-baseline gap-x-3">
        <span className="min-w-0 truncate font-medium">{title}</span>
        {one !== null && (
          <span className="text-xs text-mut tabular-nums">
            now <span className="text-fg">{format(one)}</span>
          </span>
        )}
        {action && <span className="ml-auto">{action}</span>}
      </figcaption>
      <div className="mt-2 grid grid-cols-[52px_minmax(0,1fr)] gap-x-2">
        <div className="relative text-right text-2xs text-faint tabular-nums">
          {[0, 0.5, 1].map((f) => (
            <span
              key={f}
              className="absolute right-0 -translate-y-1/2"
              style={{ top: `${f * 100}%` }}
            >
              {axis(Number((top * (1 - f)).toPrecision(12)))}
            </span>
          ))}
        </div>
        <div
          tabIndex={0}
          role="slider"
          aria-label={described}
          aria-valuemin={0}
          aria-valuemax={Math.max(0, steps - 1)}
          aria-valuenow={active ?? Math.max(0, steps - 1)}
          className="relative h-[104px] outline-offset-4"
          onPointerMove={(e) => {
            if (!steps) return;
            const box = e.currentTarget.getBoundingClientRect();
            const f = (e.clientX - box.left) / box.width;
            setActive(Math.min(steps - 1, Math.max(0, Math.floor(f * steps))));
          }}
          onPointerLeave={() => setActive(null)}
          onBlur={() => setActive(null)}
          onKeyDown={(e) => {
            if (!steps) return;
            const at = active ?? steps - 1;
            const next = {
              ArrowLeft: Math.max(0, at - 1),
              ArrowRight: Math.min(steps - 1, at + 1),
              Home: 0,
              End: steps - 1,
            }[e.key];
            if (next !== undefined) {
              e.preventDefault();
              setActive(next);
            } else if (e.key === "Escape") setActive(null);
          }}
        >
          {[0, 0.5, 1].map((f) => (
            <div
              key={f}
              className={`absolute inset-x-0 border-t ${f === 0.5 ? "border-soft" : "border-line"}`}
              style={{ top: `${f * 100}%` }}
            />
          ))}
          <svg
            viewBox={`0 0 ${W} ${H}`}
            preserveAspectRatio="none"
            className="absolute inset-0 size-full overflow-visible"
            aria-hidden
          >
            {/* Biggest last, on top. */}
            {paths
              .map((d, i) => ({ d, i }))
              .reverse()
              .map(({ d, i }) => (
                <path
                  key={c.lines[i]?.label ?? i}
                  d={d}
                  className={colors[i] ?? "text-faint"}
                  fill="none"
                  stroke="currentColor"
                  strokeWidth={1.5}
                  strokeLinejoin="round"
                  strokeLinecap="round"
                  vectorEffect="non-scaling-stroke"
                />
              ))}
          </svg>
          {c.lines.length === 0 && (
            <p className="absolute inset-0 grid place-items-center text-xs text-mut">
              No points in this range.
            </p>
          )}
          {active !== null && reading && reading.length > 0 && (
            <>
              <div
                className="absolute inset-y-0 w-px bg-edge"
                style={{ left: `${((active + 0.5) / steps) * 100}%` }}
              />
              <div
                className="absolute -top-1 z-10 rounded-ctl border border-edge bg-panel px-1.5 py-0.5 text-xs whitespace-nowrap tabular-nums"
                style={
                  active / steps > 0.6
                    ? {
                        right: `${(1 - active / steps) * 100}%`,
                        marginRight: 8,
                      }
                    : {
                        left: `${((active + 1) / steps) * 100}%`,
                        marginLeft: 8,
                      }
                }
              >
                <span className="text-mut">{time(active)}</span>
                {reading.map(({ l, i, v }) => (
                  <span key={l.label} className="flex gap-2">
                    {c.lines.length > 1 && (
                      <span className={colors[i] ?? "text-faint"}>
                        {l.label || "(none)"}
                      </span>
                    )}
                    <b className="ml-auto font-semibold text-fg">
                      {format(v ?? 0)}
                    </b>
                  </span>
                ))}
              </div>
            </>
          )}
        </div>
        <div />
        <div className="relative h-4 text-2xs text-faint tabular-nums">
          {[0, 0.5].map((f) => (
            <span
              key={f}
              className={`absolute ${f ? "-translate-x-1/2" : ""}`}
              style={{ left: `${f * 100}%` }}
            >
              {tick(f)}
            </span>
          ))}
          <span className="absolute right-0">now</span>
        </div>
      </div>
      {c.lines.length > 1 && (
        <ul className="flex flex-wrap gap-x-3 text-xs" aria-label="Lines">
          {c.lines.slice(0, colors.length).map((l, i) => (
            <li key={l.label} className={colors[i]}>
              {l.label || "(none)"}
              <span className="ml-1 text-mut tabular-nums">
                {latest(i) !== null ? format(latest(i) ?? 0) : ""}
              </span>
            </li>
          ))}
          {c.lines.length + (c.more ?? 0) > colors.length && (
            <li className="text-faint">
              {c.lines.length + (c.more ?? 0) - colors.length} more
            </li>
          )}
        </ul>
      )}
    </figure>
  );
});
