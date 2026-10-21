// Compact formatting for dense tables.

/** Time since `iso` as one unit: "now", "40s", "12m", "2h", "12d". */
export function since(iso: string, now = Date.now()) {
  const s = Math.max(0, Math.round((now - Date.parse(iso)) / 1000));
  if (s < 5) return "now";
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86_400) return `${Math.floor(s / 3600)}h`;
  return `${Math.floor(s / 86_400)}d`;
}

/** Time until `iso`, same units as `since`. */
export const until = (iso: string) =>
  since(new Date().toISOString(), Date.parse(iso));

const pad = (n: number, width = 2) => String(n).padStart(width, "0");

/** "14:02" */
export function clock(iso: string) {
  const d = new Date(iso);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** "14:02:41.118" */
export function clockPrecise(iso: string) {
  const d = new Date(iso);
  return `${clock(iso)}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
}

/** "Oct 21" */
export function day(iso: string) {
  return new Date(iso).toLocaleDateString("en", {
    month: "short",
    day: "numeric",
  });
}

/** Megabytes as "168M", "1G", "2.1G". */
export function mb(n: number) {
  if (n < 1024) return `${Math.round(n)}M`;
  const g = n / 1024;
  return `${Number.isInteger(g) ? g : g.toFixed(1)}G`;
}

/** "47s", "2.3s", "800ms" */
export function duration(ms: number) {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 10_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${Math.round(ms / 1000)}s`;
}

/** Parses "768M", "1G", "1.5g" or a plain number of megabytes. */
export function parseMb(input: string): number | null {
  const m = /^\s*(\d+(?:\.\d+)?)\s*([mg])?b?\s*$/i.exec(input);
  if (!m?.[1]) return null;
  const n = Number(m[1]);
  return Math.round(m[2]?.toLowerCase() === "g" ? n * 1024 : n);
}
