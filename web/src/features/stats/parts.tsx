import type { ReactNode } from "react";
import type { LiveState } from "./live";

/** 1h · 24h · 7d (or other ranges), one row above the charts it scopes. */
export function RangeSwitch<R extends string>({
  value,
  options,
  onChange,
}: {
  value: R;
  options: readonly R[];
  onChange: (range: R) => void;
}) {
  return (
    <fieldset className="seg" aria-label="Time range">
      {options.map((r) => (
        <button
          key={r}
          type="button"
          aria-pressed={r === value}
          onClick={() => onChange(r)}
        >
          {r}
        </button>
      ))}
    </fieldset>
  );
}

const liveText: Record<LiveState, ReactNode> = {
  live: (
    <>
      <span className="text-ok">●</span> live, every few seconds
    </>
  ),
  paused: "○ paused while the tab is hidden",
  lost: (
    <span className="text-fail">✕ live updates stopped, reload to retry</span>
  ),
};

/** Whether the numbers next to it are updating. */
export function LiveNote({ state }: { state: LiveState }) {
  return <span className="text-xs text-mut">{liveText[state]}</span>;
}

/** A live number: label, value, and what it is out of. */
export function Stat({
  label,
  value,
  of,
}: {
  label: string;
  value: ReactNode;
  of?: ReactNode;
}) {
  return (
    <div className="flex min-w-[140px] flex-col">
      <span className="text-xs text-mut">{label}</span>
      <span className="text-lg font-semibold">
        {value}
        {of && <span className="text-sm font-normal text-mut"> {of}</span>}
      </span>
    </div>
  );
}
