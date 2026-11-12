import { pct, size } from "../../ui/format";
import { ms } from "../traces/format";
import type { AppMetricChart } from "./data";

// App metrics' values in their units, as `dock metrics` prints them:
// bytes, durations, percent for ratios, a rate a minute, else a number
// with its unit's word ("{connection}" is "connection").

/** A number in three significant digits: "148", "4.2k", "1.3M". */
export function compact(n: number) {
  const a = Math.abs(n);
  if (a >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (a >= 1e4) return `${(n / 1e3).toFixed(1)}k`;
  if (a >= 100 || a === 0) return n.toFixed(0);
  return String(Number(n.toPrecision(3)));
}

/** A chart's axis labels: its format, without a unit's word, so they fit. */
export function axisFormatOf(c: AppMetricChart): (v: number) => string {
  const word = c.unit?.startsWith("{") || !knownUnits.has(c.unit ?? "");
  return word && (c.agg || c.aggs[0]) !== "rate" ? compact : formatOf(c);
}

const knownUnits = new Set(["By", "s", "ms", "us", "ns", "1"]);

/** An axis top at or above n: 1, 2, 2.5 or 5 times a power of ten. */
export function niceTop(n: number) {
  if (!(n > 0)) return 1;
  const p = 10 ** Math.floor(Math.log10(n));
  return (
    [1, 2, 2.5, 5, 10].map((f) => f * p).find((x) => x >= n * 1.02) ?? 10 * p
  );
}

/** How a chart's values read. */
export function formatOf(c: AppMetricChart): (v: number) => string {
  if ((c.agg || c.aggs[0]) === "rate") return (v) => `${compact(v)}/min`;
  switch (c.unit) {
    case "By":
      return size;
    case "s":
      return (v) => ms(v * 1000);
    case "ms":
      return ms;
    case "us":
      return (v) => ms(v / 1000);
    case "ns":
      return (v) => ms(v / 1e6);
    case "1":
      return (v) => pct(v * 100);
  }
  const word = c.unit?.replace(/^\{|\}$/g, "");
  return word ? (v) => `${compact(v)} ${word}` : compact;
}
