import type { SiteSettings } from "./data";

// The site settings the UI edits (ADR 0037): on a site, and as the
// install's defaults. Each is text in a form; lists one entry per line.

/** The site settings phase A1 applies, by their JSON names. */
export type Key = "excluded_paths" | "ignored_ips" | "event_retention_days";

/** A list setting's entries: its non-empty lines. */
export const lines = (text: string) =>
  text
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);

/** A setting's value as the form shows it. */
export function text(s: SiteSettings, key: Key) {
  const v = s[key];
  return Array.isArray(v) ? v.join("\n") : v === undefined ? "" : String(v);
}

/** The setting's value from its text, as the API takes it. */
export function value(key: Key, text: string): SiteSettings {
  return key === "event_retention_days"
    ? { event_retention_days: Number(text) }
    : { [key]: lines(text) };
}

/** Whether a retention's text is a whole number of days the API takes. */
export function validDays(text: string) {
  const days = Number(text);
  return (
    text.trim() !== "" && Number.isInteger(days) && days >= 1 && days <= 3650
  );
}

/** The CLI flags a setting's value takes. */
export function flags(key: Key, text: string) {
  const q = (v: string) => (/^[\w./:-]+$/.test(v) ? v : `'${v}'`);
  if (key === "event_retention_days") return [`--event-retention-days ${text}`];
  const flag = key === "excluded_paths" ? "--exclude-path" : "--ignore-ip";
  const vs = lines(text);
  return vs.length ? vs.map((v) => `${flag} ${q(v)}`) : [`${flag} ''`];
}

export const fields: {
  key: Key;
  label: string;
  help: string;
  list?: boolean;
}[] = [
  {
    key: "excluded_paths",
    label: "Excluded paths",
    help: "Paths not counted, one per line; * matches anything, like /admin/*.",
    list: true,
  },
  {
    key: "ignored_ips",
    label: "Ignored addresses",
    help: "Visits from these addresses or CIDR ranges aren't counted, like your office.",
    list: true,
  },
  {
    key: "event_retention_days",
    label: "Event retention",
    help: "How long raw events are kept, in days. Daily totals stay; filters reach back this far.",
  },
];
