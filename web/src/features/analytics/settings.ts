import type { SiteSettings } from "./data";

// The site settings the UI edits (ADR 0037): on a site, and as the
// install's defaults. Each is text in a form: lists one entry per line,
// switches "on" or "off", choices their value. Public dashboards are per site and have a
// section of their own (PublicSection).

/** How a setting is edited and checked. */
type Kind = "list" | "days" | "percent" | "switch" | "choice";

export const fields = [
  {
    key: "auto_script",
    kind: "switch",
    flag: "--auto-script",
    label: "Add the script",
    help: "Dock puts the tag in each page; off, the pages do.",
  },
  {
    key: "excluded_paths",
    kind: "list",
    flag: "--exclude-path",
    label: "Excluded paths",
    help: "Not counted, one per line; * matches anything.",
    placeholder: "/admin/*",
  },
  {
    key: "ignored_ips",
    kind: "list",
    flag: "--ignore-ip",
    label: "Ignored addresses",
    help: "Addresses or CIDR ranges not counted.",
    placeholder: "203.0.113.7\n10.0.0.0/8",
  },
  {
    key: "event_retention_days",
    kind: "days",
    flag: "--event-retention-days",
    label: "Event retention",
    help: "How far filters reach back. Daily totals stay.",
  },
  {
    key: "errors_and_vitals",
    kind: "switch",
    flag: "--errors",
    label: "Errors and Web Vitals",
    help: "Page errors and load speed; about 1 KB of script.",
  },
  {
    key: "error_retention_days",
    kind: "days",
    flag: "--error-retention-days",
    label: "Error retention",
    help: "How long errors and stacks are kept.",
  },
  {
    key: "consent",
    kind: "choice",
    flag: "--consent",
    label: "Wait for consent",
    help: "What waits for dock.consent(true).",
    options: [
      ["none", "Nothing waits"],
      ["replay", "Replay waits"],
      ["all", "Everything waits"],
    ],
  },
  {
    key: "replay",
    kind: "switch",
    flag: "--replay",
    label: "Session replay",
    help: "Inputs always masked; data-dock-block hides more.",
  },
  {
    key: "replay_sample_rate",
    kind: "percent",
    flag: "--replay-sample",
    label: "Replay sample",
    help: "Page loads recorded; only those load the recorder.",
  },
  {
    key: "mask_all_text",
    kind: "switch",
    flag: "--mask-text",
    label: "Mask all text",
    help: "All text as ****, not only what is typed.",
  },
  {
    key: "replay_retention_days",
    kind: "days",
    flag: "--replay-retention-days",
    label: "Replay retention",
    help: "How long replays are kept.",
  },
] as const satisfies readonly {
  key: keyof SiteSettings;
  kind: Kind;
  flag: string;
  label: string;
  /** One short line: it sits under the label in a settings row. */
  help: string;
  /** A list's example entries, shown in its empty field. */
  placeholder?: string;
  /** A choice's values and their words. */
  options?: readonly (readonly [string, string])[];
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
    case "choice":
      return { [key]: text };
    default:
      return { [key]: Number(text) };
  }
}

/** Whether a setting's text is a value the API takes. */
export function valid(key: Key, text: string) {
  const field = kindOf[key];
  const { kind } = field;
  if (kind === "list" || kind === "switch") return true;
  if ("options" in field) return field.options.some(([v]) => v === text);
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
  const field = kindOf[key];
  if ("options" in field)
    return field.options.find(([o]) => o === v)?.[1] ?? String(v);
  switch (field.kind) {
    case "days":
      return `${v} days`;
    case "percent":
      return `${v}%`;
    default:
      return String(v);
  }
}
