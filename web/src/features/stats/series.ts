import type { StatsRange } from "../../data/types";

/** One point of a chart: a time (ms) and a value. */
export type Point = { t: number; v: number };

export const ranges = ["1h", "24h", "7d"] as const satisfies StatsRange[];

export const rangeMs: Record<StatsRange, number> = {
  "1h": 3_600_000,
  "24h": 86_400_000,
  "7d": 7 * 86_400_000,
};

export const rangeLabel: Record<StatsRange, string> = {
  "1h": "last hour",
  "24h": "last 24 hours",
  "7d": "last 7 days",
};

/** `?range=` for statistics pages; anything else is 1h. */
export const validateRange = (
  search: Record<string, unknown>,
): { range?: StatsRange } => {
  const range = ranges.find((r) => r === search.range);
  return range && range !== "1h" ? { range } : {};
};

/** Most points a chart draws per series. */
const MAX_POINTS = 300;

/**
 * Cuts a series to at most ~300 points, keeping its shape and peaks: the
 * span is split into equal time buckets and each keeps its lowest and
 * highest point, in time order. A bucket without points is a gap (null),
 * where the line breaks. Buckets are never narrower than the resolution,
 * so a 1h range keeps every minute.
 */
export function downsample(
  points: Point[],
  from: number,
  to: number,
  resolutionMs: number,
): (Point | null)[] {
  const span = Math.max(1, to - from);
  const count = Math.max(
    1,
    Math.min(MAX_POINTS / 2, Math.round(span / resolutionMs)),
  );
  const width = span / count;
  const buckets: Point[][] = Array.from({ length: count }, () => []);
  for (const p of points) {
    const i = Math.floor((p.t - from) / width);
    if (i >= 0 && i < count) buckets[i]?.push(p);
    else if (i === count) buckets[count - 1]?.push(p);
  }
  const out: (Point | null)[] = [];
  let seen = false;
  for (const bucket of buckets) {
    const first = bucket[0];
    if (!first) {
      // Gaps before the first point aren't breaks, just no data yet.
      if (seen && out.at(-1) !== null) out.push(null);
      continue;
    }
    seen = true;
    let lo = first;
    let hi = first;
    for (const p of bucket) {
      if (p.v < lo.v) lo = p;
      if (p.v > hi.v) hi = p;
    }
    if (lo === hi) out.push(lo);
    else out.push(...(lo.t < hi.t ? [lo, hi] : [hi, lo]));
  }
  if (out.at(-1) === null) out.pop();
  return out;
}

/** Latest, average and highest value of a series. */
export function summary(points: Point[]) {
  let sum = 0;
  let peak: Point | undefined;
  for (const p of points) {
    sum += p.v;
    if (!peak || p.v > peak.v) peak = p;
  }
  return {
    last: points.at(-1),
    avg: points.length ? sum / points.length : undefined,
    peak,
  };
}
