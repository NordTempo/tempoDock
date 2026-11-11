import { Fragment } from "react";
import { plural } from "../../ui/format";
import type { Finding, ImageScan, ImageUse } from "./data";

// Pieces the scanning screens share: the service overview's row, the
// Images page, an image's page and a release's line.

/** Counts by severity, as a scan and a fix group carry them. */
type Counts = Pick<ImageScan, "critical" | "high" | "medium" | "low" | "other">;

/** Every finding a scan or group counts. */
export const total = (c: Counts) =>
  c.critical + c.high + c.medium + c.low + c.other;

/** Findings below high: medium, low and the rest. */
export const below = (c: Counts) => c.medium + c.low + c.other;

/** The colour of a severity: critical red, high amber, the rest quieter. */
export function severityTone(severity: Finding["severity"] | string) {
  switch (severity) {
    case "critical":
      return "text-fail";
    case "high":
      return "text-warn";
    case "medium":
      return "text-sec";
    default:
      return "text-faint";
  }
}

/** The left edge of a block by its worst count: red, amber or neutral. */
export const edgeOf = (c: Counts) =>
  c.critical > 0 ? "border-fail" : c.high > 0 ? "border-warn" : "border-edge";

/** CISA lists it as exploited in the wild. */
export function Kev() {
  return (
    <span
      className="ml-1.5 rounded-[4px] border border-fail-edge px-1 align-[1px] font-mono text-[11px] leading-4 font-normal text-fail"
      title="Exploited in the wild (CISA's known exploited vulnerabilities)"
    >
      KEV
    </span>
  );
}

/** "2 critical 5 high" in their colours, leaving out the zeros. */
export function WorstCounts({ counts }: { counts: Counts }) {
  return (
    <>
      {counts.critical > 0 && (
        <span className="text-fail">{counts.critical} critical</span>
      )}
      {counts.critical > 0 && counts.high > 0 && " "}
      {counts.high > 0 && <span className="text-warn">{counts.high} high</span>}
    </>
  );
}

/**
 * Whether a scan has findings to show: it is scanned, or was before and
 * is being scanned again (or that failed), when the last findings stand.
 */
export const hasFindings = (
  scan: ImageScan | undefined,
): scan is ImageScan & { scanned_at: string } =>
  !!scan?.scanned_at && scan.state !== "unsupported";

/** A faint word after the scan time while a scan with findings moves on. */
export const rescan = (scan: ImageScan) =>
  scan.state === "queued"
    ? "queued again"
    : scan.state === "scanning"
      ? "scanning again"
      : scan.state === "failed"
        ? "last scan failed"
        : "";

/**
 * What a scan that isn't done says: why it failed or can't be scanned,
 * or what it waits for. No scan at all is a release that went live only
 * now.
 */
export function notScanned(scan: ImageScan | undefined) {
  if (!scan) return "queued";
  if (scan.error)
    return scan.state === "queued" ? `queued: ${scan.error}` : scan.error;
  switch (scan.state) {
    case "queued":
      return "queued";
    case "scanning":
      return "scanning now";
    case "failed":
      return "failed";
    case "unsupported":
      return "can't be scanned";
    case "scanned":
      return "";
  }
}

/**
 * Where an image runs, short: "web · production", one entry per service
 * with its environments ("api · production, staging"). The project leads
 * when the image runs in more than one.
 */
export function usedByText(uses: readonly ImageUse[]) {
  const projects = new Set(uses.map((u) => u.project));
  const groups = new Map<string, string[]>();
  for (const u of uses) {
    const name = projects.size > 1 ? `${u.project}/${u.service}` : u.service;
    const envs = groups.get(name) ?? [];
    if (!envs.includes(u.environment)) envs.push(u.environment);
    groups.set(name, envs);
  }
  return [...groups].map(([name, envs]) => `${name} · ${envs.join(", ")}`);
}

/** usedByText, one entry after another. */
export function UsedBy({ uses }: { uses: readonly ImageUse[] }) {
  const entries = usedByText(uses);
  if (entries.length === 0) return <span className="text-faint">·</span>;
  return (
    <>
      {entries.map((text, i) => (
        <Fragment key={text}>
          {i > 0 && <span className="text-faint">; </span>}
          {text}
        </Fragment>
      ))}
    </>
  );
}

/**
 * The sentence after "N fixable." on the overview: what fixes the worst
 * findings (criticals, else highs). worst_fix fixes some of them, not
 * necessarily all, so with several it says "some".
 */
export function worstFixSentence(scan: ImageScan) {
  const n = scan.critical || scan.high;
  const word = scan.critical > 0 ? "critical" : "high";
  switch (scan.worst_fix) {
    case "rebuild":
      return n === 1
        ? `The ${word} is fixed by rebuilding on a newer base image.`
        : `Rebuilding on a newer base image fixes some of the ${word}s.`;
    case "upgrade":
      return n === 1
        ? `The ${word} is fixed by upgrading the app's dependencies.`
        : `Upgrading the app's dependencies fixes some of the ${word}s.`;
    case "none":
      return `No fix is known for the ${n === 1 ? word : `${word}s`} yet.`;
    default:
      return "";
  }
}

/** "2 critical, 5 high · 20 more", or what there is below high. */
export function summary(scan: ImageScan) {
  const worst = [
    scan.critical > 0 && `${scan.critical} critical`,
    scan.high > 0 && `${scan.high} high`,
  ].filter(Boolean);
  const rest = below(scan);
  if (worst.length === 0)
    return rest > 0 ? `${rest} medium or low` : "None found";
  return `${worst.join(", ")}${rest > 0 ? ` · ${rest} more` : ""}`;
}

/** "Upgrade 2 packages", "Rebuild on a newer base image", "No fix yet". */
export function fixTitle(kind: "rebuild" | "upgrade" | "none", n: number) {
  switch (kind) {
    case "rebuild":
      return "Rebuild on a newer base image";
    case "upgrade":
      return `Upgrade ${plural(n, "package")}`;
    case "none":
      return "No fix yet";
  }
}
