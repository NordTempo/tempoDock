import { remembered } from "../../data/remembered";
import type { TrafficPoint, TrafficRange } from "../../data/types";

/** One bar of the requests chart: a bucket's start (ms) and its counts. */
export type Bar = { t: number; total: number; client: number; server: number };

export const trafficRanges = [
  "24h",
  "7d",
  "30d",
] as const satisfies TrafficRange[];

export const trafficRangeMs: Record<TrafficRange, number> = {
  "24h": 86_400_000,
  "7d": 7 * 86_400_000,
  "30d": 30 * 86_400_000,
};

export const trafficRangeLabel: Record<TrafficRange, string> = {
  "24h": "Last 24 hours",
  "7d": "Last 7 days",
  "30d": "Last 30 days",
};

/** How much one bar covers, so a range has 120 to 170 bars. */
export const barMs: Record<TrafficRange, number> = {
  "24h": 600_000,
  "7d": 3_600_000,
  "30d": 6 * 3_600_000,
};

export const barLabel: Record<TrafficRange, string> = {
  "24h": "per 10 minutes",
  "7d": "per hour",
  "30d": "per 6 hours",
};

/** Requests' Traffic range last picked on this browser, 24h until one is. */
export const trafficRange = remembered("requests.range", trafficRanges, "24h");

/** `?range=` for the traffic page; 24h or none shows trafficRange. */
export const validateTrafficRange = (
  search: Record<string, unknown>,
): { range?: TrafficRange } => {
  const range = trafficRanges.find((r) => r === search.range);
  return range && range !== "24h" ? { range } : {};
};

/**
 * Adds points up into bars of `width` ms, aligned to multiples of it, in
 * time order.
 */
export function toBars(points: TrafficPoint[], width: number): Bar[] {
  const bars = new Map<number, Bar>();
  for (const p of points) {
    const at = Date.parse(p.time);
    const t = at - (at % width);
    const b = bars.get(t) ?? { t, total: 0, client: 0, server: 0 };
    b.total += p.requests;
    b.client += p.status_4xx;
    b.server += p.status_5xx;
    bars.set(t, b);
  }
  return [...bars.values()].sort((a, b) => a.t - b.t);
}

/** A round axis top at or above v: 1, 2, 2.5 or 5 times a power of ten. */
export function niceMax(v: number) {
  const p = 10 ** Math.floor(Math.log10(Math.max(v, 1)));
  return [1, 2, 2.5, 5, 10].map((m) => m * p).find((m) => m >= v) ?? v;
}
