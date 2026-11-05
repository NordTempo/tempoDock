// Cron expressions as the daemon reads them (internal/cron, ADR 0054),
// for the Jobs tab: the editor checks an expression and previews its next
// times as it is typed, and every schedule reads in words. The daemon
// stays the authority; this only has to agree with it for display.

/** A parsed expression: each field's matching values. */
export type Schedule = {
  minute: Set<number>;
  hour: Set<number>;
  dom: Set<number>;
  month: Set<number>;
  dow: Set<number>;
  /** Day fields written `*`: then only the other one decides. */
  domAny: boolean;
  dowAny: boolean;
};

type Field = { name: string; min: number; max: number; names?: string[] };

const months = "jan feb mar apr may jun jul aug sep oct nov dec".split(" ");
const weekdays = "sun mon tue wed thu fri sat".split(" ");

const fields: Field[] = [
  { name: "minute", min: 0, max: 59 },
  { name: "hour", min: 0, max: 23 },
  { name: "day of month", min: 1, max: 31 },
  { name: "month", min: 1, max: 12, names: months },
  { name: "day of week", min: 0, max: 7, names: weekdays },
];

const macros: Record<string, string> = {
  "@yearly": "0 0 1 1 *",
  "@annually": "0 0 1 1 *",
  "@monthly": "0 0 1 * *",
  "@weekly": "0 0 * * 0",
  "@daily": "0 0 * * *",
  "@midnight": "0 0 * * *",
  "@hourly": "0 * * * *",
};

/** The five fields of an expression, macros expanded. */
const split = (expr: string) => {
  const e = expr.trim().toLowerCase();
  return (macros[e] ?? e).split(/\s+/).filter(Boolean);
};

/** One value of a field, a number or a name, in range. */
function value(f: Field, text: string) {
  const named = f.names?.indexOf(text) ?? -1;
  if (named >= 0) return f.min + named;
  if (!/^\d+$/.test(text))
    throw new Error(`${f.name}: "${text}" is not a number`);
  const v = Number(text);
  if (v < f.min || v > f.max)
    throw new Error(`${f.name}: ${v} is outside ${f.min}-${f.max}`);
  return v;
}

/** One field as the set of values it matches. */
function parseField(f: Field, text: string) {
  const out = new Set<number>();
  for (const item of text.split(",")) {
    const slash = item.indexOf("/");
    const range = slash < 0 ? item : item.slice(0, slash);
    let step = 1;
    if (slash >= 0) {
      const s = item.slice(slash + 1);
      step = /^\d+$/.test(s) ? Number(s) : 0;
      if (step < 1)
        throw new Error(`${f.name}: step "${s}" must be a positive number`);
    }
    let lo = f.min;
    let hi = f.max;
    if (range.includes("-")) {
      const [a = "", b = ""] = range.split("-", 2);
      lo = value(f, a);
      hi = value(f, b);
      if (lo > hi)
        throw new Error(`${f.name}: range "${range}" runs backwards`);
    } else if (range !== "*") {
      lo = value(f, range);
      if (slash < 0) hi = lo;
    }
    for (let v = lo; v <= hi; v += step) out.add(v);
  }
  return out;
}

/** Days in each month, February's leap day included. */
const monthDays = [31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];

/**
 * Parses an expression, five fields or a macro. An error says which field
 * is wrong and why, in the daemon's words.
 */
export function parseCron(
  expr: string,
): { schedule: Schedule } | { error: string } {
  const parts = split(expr);
  if (parts.length !== 5)
    return {
      error: `a schedule has 5 fields (minute hour day-of-month month day-of-week), e.g. "0 3 * * *"; got ${parts.length}`,
    };
  try {
    const [minute, hour, dom, month, dow] = fields.map((f, i) =>
      parseField(f, parts[i] ?? ""),
    ) as [Set<number>, Set<number>, Set<number>, Set<number>, Set<number>];
    if (dow.has(7)) dow.add(0); // 7 is Sunday too
    const schedule = {
      minute,
      hour,
      dom,
      month,
      dow,
      domAny: parts[2] === "*",
      dowAny: parts[4] === "*",
    };
    const someDay =
      schedule.domAny ||
      !schedule.dowAny ||
      [...month].some((m) =>
        [...dom].some((d) => d <= (monthDays[m - 1] ?? 0)),
      );
    if (!someDay)
      return { error: "that day of month never happens in the months given" };
    return { schedule };
  } catch (e) {
    return { error: e instanceof Error ? e.message : String(e) };
  }
}

/** Whether a wall clock day (UTC fields of `d`) is one the schedule runs on. */
function dayMatches(s: Schedule, d: Date) {
  const dom = s.dom.has(d.getUTCDate());
  const dow = s.dow.has(d.getUTCDay());
  if (s.domAny && s.dowAny) return true;
  if (s.domAny) return dow;
  if (s.dowAny) return dom;
  return dom || dow;
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;

/** The last timezone's wall clock, kept since the editor asks repeatedly. */
let lastZone: { tz: string; format: Intl.DateTimeFormat } | undefined;

/**
 * A timezone's wall clock: an instant's local time there, as ms since the
 * epoch read as UTC. Throws a RangeError for an unknown timezone.
 */
function wallClock(tz: string) {
  if (lastZone?.tz !== tz) {
    const format = new Intl.DateTimeFormat("en-US", {
      timeZone: tz,
      hourCycle: "h23",
      year: "numeric",
      month: "numeric",
      day: "numeric",
      hour: "numeric",
      minute: "numeric",
    });
    lastZone = { tz, format };
  }
  const { format } = lastZone;
  return (at: number) => {
    const p: Record<string, number> = {};
    for (const { type, value } of format.formatToParts(at)) p[type] = +value;
    return Date.UTC(
      p.year ?? 0,
      (p.month ?? 1) - 1,
      p.day ?? 1,
      p.hour ?? 0,
      p.minute ?? 0,
    );
  };
}

/** Whether `tz` is an IANA timezone this browser knows. */
export function knownZone(tz: string) {
  try {
    wallClock(tz);
    return true;
  } catch {
    return false;
  }
}

/**
 * The first instant after `after` the schedule is due in a timezone, or
 * undefined within nine years. Walks the wall clock like the daemon: a
 * time that doesn't exist that day is skipped, one that happens twice is
 * due once (the first time, unless that one has passed).
 */
function next(s: Schedule, wallOf: (at: number) => number, after: number) {
  // The instants a wall time names: none, one, or two when clocks go back.
  const instant = (wall: number) => {
    const at = [
      wall - (wallOf(wall - 24 * HOUR) - (wall - 24 * HOUR)),
      wall - (wallOf(wall + 24 * HOUR) - (wall + 24 * HOUR)),
    ].filter((t) => t > after && wallOf(t) === wall);
    return at.length ? Math.min(...at) : undefined;
  };
  let t = Math.floor(wallOf(after) / MINUTE) * MINUTE + MINUTE;
  const limit = new Date(t).getUTCFullYear() + 9;
  for (let d = new Date(t); d.getUTCFullYear() <= limit; d = new Date(t)) {
    const [y, mo, day, h] = [
      d.getUTCFullYear(),
      d.getUTCMonth(),
      d.getUTCDate(),
      d.getUTCHours(),
    ];
    if (!s.month.has(mo + 1)) t = Date.UTC(y, mo + 1, 1);
    else if (!dayMatches(s, d)) t = Date.UTC(y, mo, day + 1);
    else if (!s.hour.has(h)) t = Date.UTC(y, mo, day, h + 1);
    else if (!s.minute.has(d.getUTCMinutes())) t += MINUTE;
    else {
      const at = instant(t);
      if (at !== undefined) return at;
      t += MINUTE;
    }
  }
  return undefined;
}

/** The schedule's next `count` times after `from`, in a timezone. */
export function nextTimes(s: Schedule, tz: string, from: number, count = 3) {
  const wallOf = wallClock(tz);
  const out: Date[] = [];
  for (
    let at = next(s, wallOf, from);
    at !== undefined && out.length < count;
  ) {
    out.push(new Date(at));
    at = out.length < count ? next(s, wallOf, at) : undefined;
  }
  return out;
}

const pad = (n: number | string) => String(n).padStart(2, "0");
const fullDays =
  "Sundays Mondays Tuesdays Wednesdays Thursdays Fridays Saturdays".split(" ");
const title = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** A day-of-week set in words: "weekdays", "Sundays", "Mon, Thu". */
function daysInWords(dow: Set<number>) {
  dow.delete(7);
  const days = [...dow].sort();
  if (days.join() === "1,2,3,4,5") return "weekdays";
  if (days.join() === "0,6") return "weekends";
  if (days.length === 1) return fullDays[days[0] ?? 0];
  return days.map((d) => title(weekdays[d] ?? "")).join(", ");
}

/**
 * A common schedule in words ("daily at 03:00", "every 5 minutes",
 * "weekdays at 09:00, 17:00"); undefined for the rest, which show only
 * the expression. Expects an expression parseCron accepts.
 */
export function describeCron(expr: string) {
  const parsed = parseCron(expr);
  if ("error" in parsed) return undefined;
  const [mi = "", h = "", dom = "", mon = "", dow = ""] = split(expr);
  const num = /^\d+$/;
  const step = (f: string) => /^\*\/(\d+)$/.exec(f)?.[1];
  if (dom === "*" && mon === "*" && dow === "*") {
    if (mi === "*" && h === "*") return "every minute";
    if (step(mi) && h === "*") return `every ${step(mi)} minutes`;
    if (num.test(mi) && h === "*") return `hourly at :${pad(mi)}`;
    if (num.test(mi) && step(h)) return `every ${step(h)} hours at :${pad(mi)}`;
  }
  if (!num.test(mi) || !/^\d+(,\d+)*$/.test(h)) return undefined;
  const at = h
    .split(",")
    .map((x) => `${pad(x)}:${pad(mi)}`)
    .join(", ");
  if (dom === "*" && mon === "*")
    return dow === "*"
      ? `daily at ${at}`
      : `${daysInWords(parsed.schedule.dow)} at ${at}`;
  if (dow !== "*" || !num.test(dom)) return undefined;
  if (mon === "*") return `monthly on day ${Number(dom)} at ${at}`;
  const only = [...parsed.schedule.month];
  if (only.length !== 1) return undefined;
  return `yearly on ${title(months[(only[0] ?? 1) - 1] ?? "")} ${Number(dom)} at ${at}`;
}
