import type { PlanChange } from "../../data/types";
import {
  formatSetting,
  isSettingKey,
  settingLabel,
} from "../settings/settings";

/** One line of a plan as the dialog shows it, like `dock apply --plan`. */
export type PlanRow = {
  id: string;
  /** + add, ~ change, − remove; blank for kept, ▲ for what needs you. */
  mark: string;
  /** environment/service, or the project or environment a line creates. */
  where: string;
  what: string;
  old?: string;
  new?: string;
  note?: string;
};

const marks: Record<PlanChange["action"], string> = {
  add: "+",
  update: "~",
  remove: "−",
};

/** What a plan line is about, in the CLI's words. */
function what(c: PlanChange) {
  switch (c.kind) {
    case "setting":
      return c.name && isSettingKey(c.name)
        ? settingLabel[c.name]
        : (c.name ?? "");
    case "domain":
      return c.name ? `domain ${c.name}` : "domain (generated)";
    case "project":
    case "environment":
    case "service":
      return c.kind;
    default:
      return c.name ? `${c.kind} ${c.name}` : c.kind;
  }
}

/** A setting's value as the settings page writes it; others as they come. */
function value(c: PlanChange, v: string | undefined) {
  if (c.kind !== "setting" || !c.name || !isSettingKey(c.name)) return v;
  // An update to empty is a real value ("image default"); an add has no old.
  if (v === undefined && c.action !== "update") return undefined;
  return formatSetting(c.name, v ?? "");
}

/**
 * Turns a plan's lines into rows: changes get their action's mark, kept
 * lines none, manual lines ▲. A generated domain's "generated" note is
 * dropped, the row already says so.
 */
export function planRows(
  changes: readonly PlanChange[],
  section: "changes" | "kept" | "manual",
): PlanRow[] {
  return changes.map((c, i) => ({
    id: `${section}:${i}`,
    mark:
      section === "changes" ? marks[c.action] : section === "manual" ? "▲" : "",
    where:
      [c.environment, c.service].filter(Boolean).join("/") || (c.name ?? ""),
    what: what(c),
    old: value(c, c.old),
    new: value(c, c.new),
    note: c.kind === "domain" && c.note === "generated" ? undefined : c.note,
  }));
}
