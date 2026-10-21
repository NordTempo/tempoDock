// Compact formatting for dense tables.

/** Time since `iso` as one unit: "now", "40s", "12m", "2h", "12d", "3 mo". */
export function since(iso: string, now = Date.now()) {
  const s = Math.max(0, Math.round((now - Date.parse(iso)) / 1000));
  if (s < 5) return "now";
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86_400) return `${Math.floor(s / 3600)}h`;
  if (s < 60 * 86_400) return `${Math.floor(s / 86_400)}d`;
  return `${Math.floor(s / (30 * 86_400))} mo`;
}

/** "41m ago", or "just now". */
export function ago(iso: string) {
  const s = since(iso);
  return s === "now" ? "just now" : `${s} ago`;
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

/** "14:02:41" */
export function clockSec(iso: string) {
  return `${clock(iso)}:${pad(new Date(iso).getSeconds())}`;
}

/** "Oct 21" */
export function day(iso: string) {
  return new Date(iso).toLocaleDateString("en", {
    month: "short",
    day: "numeric",
  });
}

/** "2026-12-01" */
export const isoDay = (iso: string) => iso.slice(0, 10);

/** A duration in ms as "4s", "1m 12s", "2h 3m". */
export function elapsed(ms: number) {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}

/** Megabytes as "512 MB", "1 GB", "1.5 GB". */
export function mb(n: number) {
  if (n < 1024) return `${Math.round(n)} MB`;
  const g = n / 1024;
  return `${Number.isInteger(g) ? g : g.toFixed(1)} GB`;
}

/** Parses "768M", "1G", "1.5g" or a plain number of megabytes. */
export function parseMb(input: string): number | null {
  const m = /^\s*(\d+(?:\.\d+)?)\s*([mg])?b?\s*$/i.exec(input);
  if (!m?.[1]) return null;
  const n = Number(m[1]);
  return Math.round(m[2]?.toLowerCase() === "g" ? n * 1024 : n);
}

/** "1 change", "2 changes" */
export const plural = (n: number, one: string, many = `${one}s`) =>
  `${n} ${n === 1 ? one : many}`;

/** CPU and memory limits, "2 CPU · 2 GB"; zero means no limit. */
export function limits(s: { cpus: number; memory_mb: number }) {
  if (!s.cpus && !s.memory_mb) return "no limits";
  return [
    s.cpus ? `${s.cpus} CPU` : "any CPU",
    s.memory_mb ? mb(s.memory_mb) : "any memory",
  ].join(" · ");
}
