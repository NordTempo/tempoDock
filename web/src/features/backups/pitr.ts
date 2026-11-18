import type { BackupRun, ServiceBackups } from "../../data/types";
import { clock, clockSec, day, elapsed } from "../../ui/format";
import type { Snapshot } from "./data";

// Point-in-time restore of a Postgres database (ADR 0093): its window,
// the archiver's health, the strip the Backups tab draws over the window,
// and the plan of a restore to a moment. Pure functions; the components
// are PitrSection, PitrStrip and TimeRestoreDialog.

export type PITR = NonNullable<ServiceBackups["pitr"]>;
export type Archiver = NonNullable<PITR["archiver"]>;

const second = 1000;
const minute = 60 * second;
const hour = 60 * minute;
const dayMs = 24 * hour;

/** How archiving does: unknown while the archiver can't be asked. */
export type ArchiveState = "ok" | "behind" | "failing" | "unknown";

/**
 * Failing once an upload failed and it hasn't recovered, behind while it
 * is otherwise unhealthy (segments waiting over 5 minutes, or the
 * archiver can't be reached).
 */
export function archiveState(p: PITR): ArchiveState {
  const a = p.archiver;
  if (!a) return "unknown";
  if (a.healthy) return "ok";
  return a.last_error ? "failing" : "behind";
}

/** A stretch of time that restores, in ms, both ends included. */
export type Range = { from: number; through: number };

/**
 * The restorable window: its ranges, oldest first, and their outer
 * bounds. More than one range after archiving stopped and started again
 * (turned off and on, an in-place restore); a moment between two doesn't
 * restore.
 */
export type Window = Range & { ranges: Range[] };

/** The restorable window in ms, when there is one. */
export function windowOf(p: PITR, now: number): Window | undefined {
  if (!p.enabled || !p.from) return undefined;
  const ranges = (p.ranges ?? []).map((r) => ({
    from: Date.parse(r.from),
    through: Date.parse(r.through),
  }));
  const first = ranges[0];
  const last = ranges.at(-1);
  if (!first || !last) {
    // A base backup and no word on where its archive ends (the archiver
    // can't be asked): the server decides how far it reaches, and the
    // dialog lets a time up to now through.
    const from = Date.parse(p.from);
    const all = { from, through: Math.max(from, now) };
    return { ...all, ranges: [all] };
  }
  return { from: first.from, through: last.through, ranges };
}

/** The stretches between a window's ranges, where nothing was archived. */
export const gapsOf = (w: Window): Range[] =>
  w.ranges.slice(1).map((r, i) => ({
    from: w.ranges[i]?.through ?? r.from,
    through: r.from,
  }));

/** The gap `t` falls in, if it does: a moment that doesn't restore. */
export const gapAt = (w: Window, t: number) =>
  gapsOf(w).find((g) => t > g.from && t < g.through);

/** Whether `t` restores: inside one of the window's ranges. */
export const restores = (w: Window, t: number) =>
  w.ranges.some((r) => t >= r.from && t <= r.through);

/** The moment nearest `t` that restores: `t`, or the closest range end. */
export function clampTo(w: Window, t: number) {
  const gap = gapAt(w, t);
  if (gap) return t - gap.from <= gap.through - t ? gap.from : gap.through;
  return Math.min(w.through, Math.max(w.from, t));
}

/** A time to the second, dropping milliseconds: what the inputs hold. */
export const toSecond = (ms: number) => Math.floor(ms / second) * second;

/** RFC 3339 in UTC to the second, for the API and the CLI. */
export const rfc3339 = (ms: number) =>
  new Date(toSecond(ms)).toISOString().replace(/\.\d{3}Z$/, "Z");

/** "14:31:48" today, "Nov 15 21:40:00" another day. */
export function moment(ms: number, now = Date.now()) {
  const iso = new Date(ms).toISOString();
  const today = new Date(ms).toDateString() === new Date(now).toDateString();
  return today ? clockSec(iso) : `${day(iso)} ${clockSec(iso)}`;
}

/** A stretch of time: "40s", "10h 57m", "2d 4h". */
export function stretch(ms: number) {
  if (ms < dayMs) return elapsed(ms);
  const d = Math.floor(ms / dayMs);
  const h = Math.floor((ms % dayMs) / hour);
  return h ? `${d}d ${h}h` : `${d}d`;
}

/** A base backup that a restore can start from: done and still kept. */
const isBase = (r: BackupRun) =>
  r.kind === "pitr" &&
  r.operation === "backup" &&
  r.status === "succeeded" &&
  !r.pruned &&
  !!r.consistent_at;

/**
 * The base backup a restore to `at` starts from: the newest one that was
 * consistent at or before it (the server picks the same way).
 */
export function baseFor(runs: readonly BackupRun[], at: number) {
  const consistent = (r: BackupRun) => Date.parse(r.consistent_at ?? "");
  let best: BackupRun | undefined;
  for (const r of runs) {
    if (!isBase(r) || consistent(r) > at) continue;
    if (!best || consistent(r) > consistent(best)) best = r;
  }
  return best;
}

/** The newest base backup, for the status line. */
export const newestBase = (runs: readonly BackupRun[]) =>
  baseFor(runs, Number.POSITIVE_INFINITY);

/** The strip's time span in ms: what it draws, left to right. */
export type Span = { start: number; end: number };

/**
 * On, the strip runs from the window's start to now (the unarchived tail
 * shows at its right end); off, or before the first base backup, over the
 * last `days` days, where only the ticks restore.
 */
export function stripSpan(p: PITR, now: number): Span {
  const from = p.enabled && p.from ? Date.parse(p.from) : undefined;
  const start = from ?? now - p.days * dayMs;
  return { start: Math.min(start, now - minute), end: now };
}

/** Where `t` sits on the span, 0 at the left edge to 1 at the right. */
export const at = (s: Span, t: number) =>
  Math.min(1, Math.max(0, (t - s.start) / (s.end - s.start)));

/** The time at fraction `f` of the span. */
export const timeAt = (s: Span, f: number) =>
  s.start + Math.min(1, Math.max(0, f)) * (s.end - s.start);

/** A tick: one dump or base backup, or both when they ran together. */
export type Tick = { x: number; at: number; failed: boolean; label: string };

/** A volume snapshot, a diamond under the band. */
export type Diamond = { x: number; at: number; label: string };

/** A deploy of a service using the database; labelled when there's room. */
export type DeployMark = {
  x: number;
  at: number;
  name: string;
  labelled: boolean;
};

/** Marks per kind the strip draws at most: the newest are kept. */
export const maxMarks = 120;

/** Ticks closer than this (of the span) merge into one. */
const mergeWithin = 0.004;

/** A deploy label's width as a share of the strip, for spacing them. */
const labelWidth = 0.06;

const runLabel = (r: BackupRun) => (r.kind === "pitr" ? "base backup" : "dump");

/**
 * Dumps and base backups on the span as ticks, newest kept when there are
 * more than maxMarks: the base backup that joins each daily dump shares
 * its tick ("dump and base backup"), and a tick is failed (red) when any
 * of its runs failed. Running and removed runs are left out.
 */
export function ticks(s: Span, runs: readonly BackupRun[]): Tick[] {
  const done = runs
    .filter(
      (r) =>
        r.operation === "backup" &&
        r.status !== "running" &&
        !r.pruned &&
        Date.parse(r.started_at) >= s.start &&
        Date.parse(r.started_at) <= s.end,
    )
    .sort((a, b) => Date.parse(a.started_at) - Date.parse(b.started_at));
  const out: (Tick & { kinds: Set<string>; failures: Set<string> })[] = [];
  for (const r of done) {
    const t = Date.parse(r.started_at);
    const x = at(s, t);
    const last = out.at(-1);
    const kind = runLabel(r);
    if (last && x - last.x < mergeWithin) {
      last.kinds.add(kind);
      if (r.status === "failed") last.failures.add(kind);
      continue;
    }
    out.push({
      x,
      at: t,
      failed: false,
      label: "",
      kinds: new Set([kind]),
      failures: new Set(r.status === "failed" ? [kind] : []),
    });
  }
  return out.slice(-maxMarks).map(({ kinds, failures, ...t }) => {
    const what = [...kinds].join(" and ");
    const failed = failures.size > 0;
    const iso = new Date(t.at).toISOString();
    const when = `${day(iso)} ${clock(iso)}`;
    return {
      ...t,
      failed,
      label: !failed
        ? `${what}, ${when}`
        : failures.size === kinds.size
          ? `${what}, ${when}: failed`
          : `${what}, ${when}: ${[...failures].join(" and ")} failed`,
    };
  });
}

/** The span's finished snapshots as diamonds, the newest maxMarks. */
export function diamonds(s: Span, snapshots: readonly Snapshot[]): Diamond[] {
  return snapshots
    .filter((x) => x.status === "ready")
    .map((x) => ({ x, t: Date.parse(x.started_at) }))
    .filter(({ t }) => t >= s.start && t <= s.end)
    .sort((a, b) => a.t - b.t)
    .slice(-maxMarks)
    .map(({ x, t }) => ({
      x: at(s, t),
      at: t,
      label: `snapshot ${x.id}, ${day(x.started_at)} ${clock(x.started_at)}${x.note ? `: ${x.note}` : ""}`,
    }));
}

/**
 * Deploys of the services using the database ("api r41"), oldest first.
 * Labels go newest first wherever they don't run into the next one, so
 * the deploy just before an accident is the one most likely named.
 */
export function deployMarks(s: Span, deploys: PITR["deploys"]): DeployMark[] {
  const marks = (deploys ?? [])
    .map((d) => ({ d, t: Date.parse(d.at) }))
    .filter(({ t }) => t >= s.start && t <= s.end)
    .sort((a, b) => b.t - a.t)
    .slice(0, maxMarks);
  let lastLabel = Number.POSITIVE_INFINITY;
  return marks
    .map(({ d, t }): DeployMark => {
      const x = at(s, t);
      const labelled = lastLabel - x >= labelWidth;
      if (labelled) lastLabel = x;
      return { x, at: t, name: `${d.service} r${d.number}`, labelled };
    })
    .reverse();
}

/** An axis label: its position and text. */
export type AxisLabel = { x: number; text: string };

/**
 * The axis under the strip: the start (day and time), each local midnight
 * between (a day's name, every few days on a long span), and "now".
 * Labels near the edges are dropped so they don't overlap the ends.
 */
export function axis(s: Span): AxisLabel[] {
  const iso = (t: number) => new Date(t).toISOString();
  const out: AxisLabel[] = [
    { x: 0, text: `${day(iso(s.start))} ${clock(iso(s.start))}` },
  ];
  const days = (s.end - s.start) / dayMs;
  const every = Math.max(1, Math.ceil(days / 10));
  const d = new Date(s.start);
  d.setHours(24, 0, 0, 0);
  for (let i = 0; d.getTime() < s.end; i++, d.setDate(d.getDate() + 1)) {
    const x = at(s, d.getTime());
    if (i % every !== 0 || x < 0.1 || x > 0.94) continue;
    out.push({ x, text: day(iso(d.getTime())) });
  }
  out.push({ x: 1, text: "now" });
  return out;
}

/**
 * Where the unarchived tail starts, while archiving is behind or failing:
 * the newest archived moment (else the window's start). Unset while it
 * keeps up.
 */
export function tailFrom(p: PITR, s: Span) {
  if (!p.enabled || archiveState(p) === "ok" || archiveState(p) === "unknown")
    return undefined;
  const t = p.through ?? p.archiver?.oldest_waiting ?? p.from;
  return at(s, t ? Date.parse(t) : s.start);
}

/**
 * A moment the restore dialog offers in one click; `deploy` is one just
 * before a deploy, `latest` the newest moment archived, which the server
 * resolves (to_latest) instead of taking a time.
 */
export type Shortcut = {
  label: string;
  at: number;
  deploy?: boolean;
  latest?: boolean;
};

/**
 * The dialog's shortcuts inside the window: just before each of the
 * newest three deploys ("before api r41 (13:58:12)"), 15 minutes and an
 * hour ago, and the latest moment. A second before a deploy, since its
 * release went live at that time.
 */
export function shortcuts(
  w: Window,
  deploys: PITR["deploys"],
  now: number,
): Shortcut[] {
  const inside = (t: number) => restores(w, t);
  const before = (deploys ?? [])
    .map((d) => ({ d, t: toSecond(Date.parse(d.at)) - second }))
    .filter(({ t }) => inside(t))
    .slice(0, 3)
    .map(({ d, t }) => ({
      label: `before ${d.service} r${d.number} (${moment(t, now)})`,
      at: t,
      deploy: true,
    }));
  const ago = [
    { label: "15 min ago", at: toSecond(now - 15 * minute) },
    { label: "1 h ago", at: toSecond(now - hour) },
  ].filter((s) => inside(s.at));
  return [
    ...before,
    ...ago,
    { label: "latest", at: toSecond(w.through), latest: true },
  ];
}

/** A local date and time as the date and time inputs hold them. */
export function toInputs(ms: number) {
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return {
    date: `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`,
    time: `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`,
  };
}

/** The inputs' local date and time in ms; NaN while one is incomplete. */
export const fromInputs = (date: string, time: string) =>
  date && time ? new Date(`${date}T${time}`).getTime() : Number.NaN;

/** The browser's zone as the dialog names it: "Europe/Oslo (UTC+1)". */
export function zoneLabel(ms: number) {
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const off = -new Date(ms).getTimezoneOffset();
  const h = Math.floor(Math.abs(off) / 60);
  const m = Math.abs(off) % 60;
  const utc = `UTC${off < 0 ? "-" : "+"}${h}${m ? `:${String(m).padStart(2, "0")}` : ""}`;
  return off === 0 ? `${zone} (UTC)` : `${zone} (${utc})`;
}

/**
 * Everything the strip draws for a database: its span, ticks, snapshots,
 * deploys (while on), the axis and the unarchived tail.
 */
export function stripMarks(
  p: PITR,
  runs: readonly BackupRun[],
  snapshots: readonly Snapshot[],
  now: number,
) {
  const span = stripSpan(p, now);
  const tail = tailFrom(p, span);
  return {
    span,
    ticks: ticks(span, runs),
    diamonds: diamonds(span, snapshots),
    deploys: p.enabled ? deployMarks(span, p.deploys) : [],
    axis: axis(span),
    tail:
      tail === undefined
        ? undefined
        : { x: tail, failing: archiveState(p) === "failing" },
  };
}
