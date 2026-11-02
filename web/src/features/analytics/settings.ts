import type { SiteSettings } from "./data";

// The site settings the UI edits (ADR 0037): on a site, and as the
// install's defaults. Each is text in a form: lists one entry per line,
// switches "on" or "off". Public dashboards are per site and have a
// section of their own (PublicSection).

/** How a setting is edited and checked. */
type Kind = "list" | "days" | "percent" | "switch";

export const fields = [
  {
    key: "excluded_paths",
    kind: "list",
    flag: "--exclude-path",
    label: "Excluded paths",
    help: "Paths not counted, one per line; * matches anything, like /admin/*.",
  },
  {
    key: "ignored_ips",
    kind: "list",
    flag: "--ignore-ip",
    label: "Ignored addresses",
    help: "Visits from these addresses or CIDR ranges aren't counted, like your office.",
  },
  {
    key: "event_retention_days",
    kind: "days",
    flag: "--event-retention-days",
    label: "Event retention",
    help: "How long raw events are kept, in days. Daily totals stay; filters reach back this far.",
  },
  {
    key: "errors_and_vitals",
    kind: "switch",
    flag: "--errors",
    label: "Errors and Web Vitals",
    help: "Collect errors thrown on the site's pages and how fast they load. Adds about 1 KB to the script.",
  },
  {
    key: "error_retention_days",
    kind: "days",
    flag: "--error-retention-days",
    label: "Error retention",
    help: "How long errors and their stacks are kept, in days.",
  },
  {
    key: "replay",
    kind: "switch",
    flag: "--replay",
    label: "Session replay",
    help: "Record visits to watch them back. Inputs are always masked; data-dock-block hides an element.",
  },
  {
    key: "replay_sample_rate",
    kind: "percent",
    flag: "--replay-sample",
    label: "Replay sample",
    help: "Percent of page loads recorded, 1 to 100. Only those load the recorder.",
  },
  {
    key: "mask_all_text",
    kind: "switch",
    flag: "--mask-text",
    label: "Mask all text",
    help: "Replays show all text as ****, not only what is typed.",
  },
  {
    key: "replay_retention_days",
    kind: "days",
    flag: "--replay-retention-days",
    label: "Replay retention",
    help: "How long replays are kept, in days.",
  },
] as const satisfies readonly {
  key: keyof SiteSettings;
  kind: Kind;
  flag: string;
  label: string;
  help: string;
}[];

export type Field = (typeof fields)[number];
export type Key = Field["key"];

const kindOf = Object.fromEntries(fields.map((f) => [f.key, f])) as Record<
  Key,
  Field
>;

/** A list setting's entries: its non-empty lines. */
export const lines = (text: string) =>
  text
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);

/** A setting's value as the form shows it. */
export function text(s: SiteSettings, key: Key) {
  const v = s[key];
  if (typeof v === "boolean") return v ? "on" : "off";
  return Array.isArray(v) ? v.join("\n") : v === undefined ? "" : String(v);
}

/** The setting's value from its text, as the API takes it. */
export function value(key: Key, text: string): SiteSettings {
  switch (kindOf[key].kind) {
    case "list":
      return { [key]: lines(text) };
    case "switch":
      return { [key]: text === "on" };
    default:
      return { [key]: Number(text) };
  }
}

/** Whether a setting's text is a value the API takes. */
export function valid(key: Key, text: string) {
  const { kind } = kindOf[key];
  if (kind === "list" || kind === "switch") return true;
  const n = Number(text);
  return (
    text.trim() !== "" &&
    Number.isInteger(n) &&
    n >= 1 &&
    n <= (kind === "days" ? 3650 : 100)
  );
}

/** The CLI flags a setting's value takes. */
export function flags(key: Key, text: string) {
  const { kind, flag } = kindOf[key];
  if (kind !== "list") return [`${flag} ${text}`];
  const q = (v: string) => (/^[\w./:-]+$/.test(v) ? v : `'${v}'`);
  const vs = lines(text);
  return vs.length ? vs.map((v) => `${flag} ${q(v)}`) : [`${flag} ''`];
}

/** A setting's value as a switch shows it: "none", "395 days", "on". */
export function summary(s: SiteSettings, key: Key) {
  const v = s[key];
  if (Array.isArray(v)) return v.length ? `${v.length}` : "none";
  if (typeof v === "boolean") return v ? "on" : "off";
  switch (kindOf[key].kind) {
    case "days":
      return `${v} days`;
    case "percent":
      return `${v}%`;
    default:
      return String(v);
  }
}
