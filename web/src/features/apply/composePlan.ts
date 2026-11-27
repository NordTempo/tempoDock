import type { PlanChange } from "../../data/types";
import { type PlanRow, planRows } from "./plan";

/**
 * A Compose update's plan lines, by service (Update from Compose): the
 * services it adds first, then those it changes, then those it removes,
 * each service's lines together. Lines are about the one environment the
 * Compose file goes in, so they name the service only ("shared" for the
 * environment's own variables).
 */
export function composeRows(
  changes: readonly PlanChange[],
  section: "changes" | "kept" | "manual",
): PlanRow[] {
  const rank = new Map<string, number>();
  for (const c of changes) {
    if (c.kind !== "service" || !c.service) continue;
    rank.set(c.service, c.action === "add" ? 0 : c.action === "remove" ? 2 : 1);
  }
  const order = (c: PlanChange) =>
    c.kind === "environment" ? -1 : (rank.get(c.service ?? "") ?? 1);
  const sorted = changes
    .map((c, i) => ({ c, i }))
    .sort(
      (a, b) =>
        order(a.c) - order(b.c) ||
        (a.c.service ?? "").localeCompare(b.c.service ?? "") ||
        a.i - b.i,
    );
  const rows = planRows(
    sorted.map((x) => x.c),
    section,
  );
  return rows.map((r, i) => {
    const c = sorted[i]?.c;
    return {
      ...r,
      where:
        c?.service ?? (c?.kind === "environment" ? (c.name ?? "") : "shared"),
    };
  });
}

/** The services a plan names, in any of its lists. */
export function plannedServices(lists: readonly (readonly PlanChange[])[]) {
  const out = new Set<string>();
  for (const list of lists) {
    for (const c of list) if (c.service) out.add(c.service);
  }
  return out;
}
