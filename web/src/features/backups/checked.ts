import type { components } from "../../api/schema";
import type { State } from "../../ui/status";

// Checked backups (ADR 0096) in words: a repository's weekly check and a
// template database's restore drill, as the service's Backups tab and the
// console's Backups page say them. Pure, so the wording is unit tested;
// the callers pass the app's time formatting in (`Words`).

type Check = components["schemas"]["BackupCheck"];

/** How times read: "3d ago" and "Sunday 03:00". */
export type Words = {
  ago: (iso: string) => string;
  at: (iso: string) => string;
};

/** One part of the line: its state, and the command that says more. */
export type CheckedPart = State & { key: string; hint?: string };

/** What a service's backups say about checks (`ServiceBackups`). */
type Checked = { check?: Check; drill?: Check; next_check_at?: string };

const ended = (c: Check) => c.finished_at ?? c.started_at;
const reason = (text: string | undefined) => (text ? `: ${text}` : "");

function checkPart(c: Check, { ago }: Words): CheckedPart {
  const key = "check";
  switch (c.status) {
    case "running":
      return { key, tone: "prog", glyph: "◐", label: "Checking now" };
    case "succeeded":
      return { key, tone: "ok", glyph: "●", label: `Checked ${ago(ended(c))}` };
    case "failed":
      return {
        key,
        tone: "fail",
        glyph: "✕",
        label: `Check failed ${ago(ended(c))}${reason(c.error)}`,
        hint: "dock backup check",
      };
    case "skipped":
      return {
        key,
        tone: "warn",
        glyph: "▲",
        label: `Check skipped${reason(c.detail)}`,
      };
  }
}

function drillPart(d: Check, { ago }: Words): CheckedPart {
  const key = "drill";
  switch (d.status) {
    case "running":
      return { key, tone: "prog", glyph: "◐", label: "Restore drill running" };
    case "succeeded":
      return {
        key,
        tone: "ok",
        glyph: "●",
        label: `Restore drill passed ${ago(ended(d))}${d.detail ? `, ${d.detail}` : ""}`,
      };
    case "failed":
      return {
        key,
        tone: "fail",
        glyph: "✕",
        label: `Restore drill failed ${ago(ended(d))}${reason(d.error)}`,
      };
    case "skipped":
      return {
        key,
        tone: "warn",
        glyph: "▲",
        label: `Restore drill skipped${reason(d.detail)}`,
      };
  }
}

/**
 * The parts of a service's status line, in order: the repository's check,
 * the restore drill, then when the next check runs. Empty when there is
 * nothing to say (no check, no drill and none planned).
 */
export function checkedParts(
  { check, drill, next_check_at }: Checked,
  words: Words,
): CheckedPart[] {
  const parts = [
    ...(check ? [checkPart(check, words)] : []),
    ...(drill ? [drillPart(drill, words)] : []),
  ];
  if (next_check_at)
    return [
      ...parts,
      {
        key: "next",
        tone: "mut",
        label: `${parts.length ? "Next" : "First check"} ${words.at(next_check_at)}`,
      },
    ];
  // No next check means the weekly checks are off.
  if (parts.length && !check)
    return [
      ...parts,
      { key: "next", tone: "mut", label: "Weekly checks are off" },
    ];
  return parts;
}

/**
 * A repository's cell on the console's Backups page: "3d ago", "failed 3d
 * ago" (the error goes under it), "running", or when its first check runs.
 * Undefined when it was never checked and the weekly checks are off.
 */
export function repositoryChecked(
  check: Check | undefined,
  next: string | undefined,
  { ago, at }: Words,
): State | undefined {
  if (!check)
    return next ? { tone: "mut", label: `first check ${at(next)}` } : undefined;
  switch (check.status) {
    case "running":
      return { tone: "prog", glyph: "◐", label: "running" };
    case "succeeded":
      return { tone: "ok", glyph: "●", label: ago(ended(check)) };
    case "failed":
      return {
        tone: "fail",
        glyph: "✕",
        label: `failed ${ago(ended(check))}`,
      };
    case "skipped":
      return { tone: "warn", glyph: "▲", label: "skipped" };
  }
}

/**
 * The latest check of a repositories row. Local repositories are one per
 * server; a bucket's check ran on the control plane's server, so a bucket
 * matches by repository alone.
 */
export const checkOf = (
  checks: readonly Check[],
  row: { repository: string; server?: string },
) =>
  checks.find(
    (c) =>
      c.repository === row.repository &&
      (row.repository !== "local" || c.server === row.server),
  );
