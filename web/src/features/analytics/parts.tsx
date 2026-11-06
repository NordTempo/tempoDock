import type { SiteMarker } from "./data";

// Pieces the analytics views share: number formats, a value's label,
// changes against the period before, the KPI row's cells and the chart's
// markers.

export const count = (n: number) => n.toLocaleString("en-US");

/**
 * A count chart's values: whole, or with one decimal below 10, so 7
 * visitors over 30 days average 0.2 a day rather than 0.
 */
export const charted = (v: number) => {
  const r = Math.round(v * 10) / 10;
  return r < 10 && !Number.isInteger(r) ? r.toFixed(1) : count(Math.round(r));
};

const regions = new Intl.DisplayNames(["en"], { type: "region" });
/** A country's name from its ISO code ("NO": Norway). */
export const country = (code: string) => {
  try {
    return regions.of(code) ?? code;
  } catch {
    return code;
  }
};

/** How a dimension's value reads; services and releases come labelled. */
export const shown = (
  dimension: string,
  row: { value: string; label?: string },
) =>
  row.label ||
  (row.value === ""
    ? dimension === "referrer" || dimension === "channel"
      ? "Direct"
      : "(none)"
    : dimension === "country"
      ? country(row.value)
      : row.value);

/** A KPI's change: its words, whether it is good, and whether it is small. */
export type Change = { text: string; up: boolean; quiet?: boolean };

/**
 * "+12%" against the period before, with a tone; "no data before" when
 * there was nothing to compare with, and "14× before" past +999%, since a
 * percentage of next to nothing says little. Those, and changes under
 * 10%, are quiet.
 */
export function delta(now: number, before: number): Change | undefined {
  if (!before) {
    return now ? { text: "no data before", up: true, quiet: true } : undefined;
  }
  const pct = Math.round(((now - before) / before) * 100);
  if (pct > 999) {
    return {
      text: `${Math.round(now / before)}× before`,
      up: true,
      quiet: true,
    };
  }
  return {
    text: `${pct >= 0 ? "+" : "−"}${Math.abs(pct)}%`,
    up: pct >= 0,
    quiet: Math.abs(pct) < 10,
  };
}

/** A rate's change in percentage points; a lower bounce rate is good. */
export function points(now: number, before: number): Change {
  const pts = Math.round((now - before) * 100);
  return {
    text: `${pts > 0 ? "+" : pts < 0 ? "−" : "±"}${Math.abs(pts)} pts`,
    up: pts <= 0,
    quiet: Math.abs(pts) < 2,
  };
}

/** 2m 48s. */
export function duration(ms: number) {
  const s = Math.round(ms / 1000);
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}

export function Kpi({
  label,
  value,
  change,
  on,
  onClick,
}: {
  label: string;
  value: string;
  change?: Change;
  /** The metric the chart shows; set only for those it can show. */
  on?: boolean;
  onClick?: () => void;
}) {
  const content = (
    <>
      <span className={`block text-xs ${on ? "text-fg" : "text-mut"}`}>
        {label}
      </span>
      <span className="block text-lg font-semibold">
        {value}
        {change && (
          <small
            className={`ml-1.5 text-xs font-normal ${change.quiet ? "text-mut" : change.up ? "text-ok" : "text-fail"}`}
          >
            {change.text}
          </small>
        )}
      </span>
    </>
  );
  return onClick ? (
    <button
      type="button"
      aria-pressed={on}
      onClick={onClick}
      className={`border-b-2 pb-1 text-left ${on ? "border-fg" : "border-transparent"}`}
    >
      {content}
    </button>
  ) : (
    <div className="border-b-2 border-transparent pb-1">{content}</div>
  );
}

/** A site's markers as its charts draw them: "deploy: web r44", "r44" at the line's top. */
export const chartMarkers = (
  markers: readonly SiteMarker[] | null | undefined,
) =>
  (markers ?? []).map((m) => ({
    t: Date.parse(m.at),
    label: `${m.kind}: ${m.message}`,
    short: m.kind === "deploy" ? m.message.split(" ").at(-1) : m.kind,
  }));
