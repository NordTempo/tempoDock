import { type KeyboardEvent, type MouseEvent, useState } from "react";
import { moment, type stripMarks, timeAt, at as x } from "./pitr";

const minute = 60_000;

/** What the strip draws, from pitr.ts's stripMarks. */
type StripMarks = ReturnType<typeof stripMarks>;

/**
 * The timeline strip of a database's restore points (ADR 0093, mock A):
 * dumps and base backups as ticks (red when one failed), snapshots as
 * diamonds, deploys of the services using it as marks above, the
 * unarchived tail hatched. With a window and `onPick` it is a slider:
 * hover shows the moment, a click or Enter picks it (clamped into the
 * window), arrows move by a hundredth of the span (Shift: a minute), Home
 * and End go to the window's ends. `value` marks the picked moment and
 * makes arrows pick at once (the dialog); without it the cursor moves and
 * Enter picks (the tab). `compact` drops the labels for the dialog.
 */
export function PitrStrip({
  marks: m,
  window: w,
  value,
  onPick,
  compact,
  label,
}: {
  marks: StripMarks;
  window?: { from: number; through: number };
  value?: number;
  onPick?: (ms: number) => void;
  compact?: boolean;
  /** The slider's accessible name. */
  label: string;
}) {
  const [hover, setHover] = useState<number>();
  const [cursor, setCursor] = useState<number>();
  const clamp = (t: number) =>
    w ? Math.min(w.through, Math.max(w.from, Math.round(t / 1000) * 1000)) : t;
  const shown = hover ?? cursor;
  const now = m.span.end;

  const fromPointer = (e: MouseEvent<HTMLDivElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    return timeAt(m.span, (e.clientX - r.left) / r.width);
  };
  const keys = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!w || !onPick) return;
    const base = value ?? cursor ?? w.through;
    const step = e.shiftKey
      ? minute
      : Math.max(minute, (m.span.end - m.span.start) / 100);
    const to: Record<string, number> = {
      ArrowLeft: base - step,
      ArrowDown: base - step,
      ArrowRight: base + step,
      ArrowUp: base + step,
      Home: w.from,
      End: w.through,
    };
    const next = to[e.key];
    if (next !== undefined) {
      e.preventDefault();
      if (value === undefined) setCursor(clamp(next));
      else onPick(clamp(next));
    } else if ((e.key === "Enter" || e.key === " ") && value === undefined) {
      e.preventDefault();
      onPick(clamp(base));
    }
  };
  const tip = (t: number) => {
    if (w && t > w.through + minute)
      return `not archived yet; latest ${moment(w.through, now)}`;
    const c = clamp(t);
    return value === undefined
      ? `${moment(c, now)}, click to restore`
      : moment(c, now);
  };
  const shownX = shown === undefined ? undefined : x(m.span, shown);
  const selected = value ?? (hover === undefined ? cursor : undefined);

  // Heights: deploy labels, their marks, the band, snapshots, the axis.
  // Without deploy labels (the dialog, or none to show) the band moves up.
  const labels = !compact && m.deploys.some((d) => d.labelled);
  const top = labels
    ? { mark: 18, band: 32, tick: 28, diamond: 56, axis: 66, h: 86 }
    : { mark: 0, band: 10, tick: 6, diamond: 34, axis: 44, h: 62 };
  const band = 20;

  const content = (
    <>
      <div
        className={`absolute inset-x-0 rounded-tag border ${w ? "border-ok-edge bg-ok/10" : "border-dashed border-edge"}`}
        style={{ top: top.band, height: band }}
      />
      {m.tail && (
        <div
          className={`absolute right-0 rounded-r-tag opacity-75 ${m.tail.failing ? "bg-[repeating-linear-gradient(135deg,var(--color-fail)_0_3px,transparent_3px_6px)]" : "bg-[repeating-linear-gradient(135deg,var(--color-warn)_0_3px,transparent_3px_6px)]"}`}
          title={
            m.tail.failing
              ? "Not archived: archiving fails"
              : "Not archived yet: archiving is behind"
          }
          style={{
            top: top.band,
            height: band,
            // At least 24px: half an hour is invisible on seven days.
            left: `min(${m.tail.x * 100}%, calc(100% - 24px))`,
          }}
        />
      )}
      {m.ticks.map((t) => (
        <div
          key={t.at}
          title={t.label}
          className={`absolute -ml-px w-[2px] ${t.failed ? "bg-fail" : "bg-sec"}`}
          style={{ left: `${t.x * 100}%`, top: top.tick, height: band + 8 }}
        />
      ))}
      {m.diamonds.map((d) => (
        <div
          key={d.at}
          title={d.label}
          className="absolute -ml-[4px] size-[8px] rotate-45 bg-info"
          style={{ left: `${d.x * 100}%`, top: top.diamond - 4 }}
        />
      ))}
      {m.deploys.map((d) => (
        <div key={`${d.name}-${d.at}`}>
          <div
            title={`${d.name}, ${moment(d.at, now)}`}
            className="absolute -ml-[5px] size-0 border-x-[5px] border-t-[7px] border-x-transparent border-t-mut"
            style={{ left: `${d.x * 100}%`, top: top.mark }}
          />
          {labels && d.labelled && (
            <div
              aria-hidden
              className="absolute top-0 font-mono text-tag whitespace-nowrap text-mut"
              style={edge(d.x)}
            >
              {d.name}
            </div>
          )}
        </div>
      ))}
      {selected !== undefined && (
        <div
          className="absolute -ml-px w-[2px] bg-info"
          style={{
            left: `${x(m.span, selected) * 100}%`,
            top: top.band - 6,
            height: band + 12,
          }}
        />
      )}
      {shown !== undefined && shownX !== undefined && (
        <>
          <div
            className="absolute w-px bg-fg"
            style={{ left: `${shownX * 100}%`, top: 0, bottom: 0 }}
          />
          <div
            className="absolute -top-[30px] z-10 rounded-ctl border border-edge bg-sel px-2 py-0.5 text-xs whitespace-nowrap text-fg"
            style={edge(shownX)}
          >
            {tip(shown)}
          </div>
        </>
      )}
      {(compact ? [m.axis[0], m.axis.at(-1)] : m.axis).map(
        (a) =>
          a && (
            <div
              key={`${a.x}-${a.text}`}
              aria-hidden
              className="absolute font-mono text-tag whitespace-nowrap text-mut"
              style={{ top: top.axis, ...edge(a.x) }}
            >
              {a.text}
            </div>
          ),
      )}
    </>
  );
  const frame = "relative select-none rounded-ctl";
  if (!w || !onPick)
    return (
      <div className={frame} style={{ height: top.h }}>
        {content}
      </div>
    );
  const valueNow = value ?? cursor ?? w.through;
  return (
    <div
      role="slider"
      tabIndex={0}
      aria-label={label}
      aria-valuemin={w.from}
      aria-valuemax={w.through}
      aria-valuenow={valueNow}
      aria-valuetext={moment(valueNow, now)}
      onMouseMove={(e) => setHover(fromPointer(e))}
      onMouseLeave={() => setHover(undefined)}
      onClick={(e) => onPick(clamp(fromPointer(e)))}
      onKeyDown={keys}
      onBlur={() => setCursor(undefined)}
      className={`${frame} cursor-crosshair outline-offset-4 focus-visible:outline-2 focus-visible:outline-info`}
      style={{ height: top.h }}
    >
      {content}
    </div>
  );
}

/**
 * A label's place at fraction `f`: centred on it, but held inside the
 * strip at either end.
 */
function edge(f: number) {
  if (f <= 0.04) return { left: 0 };
  if (f >= 0.96) return { right: 0 };
  return { left: `${f * 100}%`, transform: "translateX(-50%)" };
}
