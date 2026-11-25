import type { components } from "../../api/schema";

// Open alerts in words, for the Inbox's rows and the Projects page's
// Needs you: alerts of one kind about one place grouped ("3 new errors on
// shop.example.com"). Pure, so the wording is unit tested.

type Alert = components["schemas"]["Alert"];

/** Alert kinds about error issues: one site's or service's group as one. */
const errorKinds = new Set(["new_error", "error_regressed", "error_spike"]);

/**
 * Alerts that are the same news about the same place, shown as one row:
 * the worst (critical first, else the newest) leads and the rest come
 * along, so resolving the row resolves them all.
 */
export type AlertGroup = { lead: Alert; alerts: Alert[] };

/** Where an alert is, for grouping: its site, else its service or server. */
function placeKey(a: Alert) {
  const site = a.target?.site;
  const at = [a.project, a.environment, a.service, a.server].join("/");
  return site ? `site:${a.project}/${site}` : at;
}

/**
 * Open alerts as rows: error alerts about one site or service in one
 * group, every other alert on its own (two failed backups are two runs to
 * look at). Kept in the order given, by each group's first alert.
 */
export function groupAlerts(alerts: Alert[]): AlertGroup[] {
  const groups: AlertGroup[] = [];
  const byKey = new Map<string, AlertGroup>();
  for (const a of alerts) {
    if (!errorKinds.has(a.kind)) {
      groups.push({ lead: a, alerts: [a] });
      continue;
    }
    const key = placeKey(a);
    const group = byKey.get(key);
    if (group) {
      group.alerts.push(a);
      if (a.severity === "critical" && group.lead.severity !== "critical")
        group.lead = a;
      continue;
    }
    const added = { lead: a, alerts: [a] };
    byKey.set(key, added);
    groups.push(added);
  }
  return groups;
}

/**
 * A group's title: the alert's own for one, else how many and where
 * ("3 new errors on shop.example.com", "2 errors in shop/web").
 */
export function groupTitle({ lead, alerts }: AlertGroup) {
  if (alerts.length === 1) return lead.title;
  const fresh = alerts.every((a) => a.kind === "new_error");
  const what = `${alerts.length} ${fresh ? "new errors" : "errors"}`;
  if (lead.target?.site) return `${what} on ${lead.target.site}`;
  if (lead.project && lead.service)
    return `${what} in ${lead.project}/${lead.service}`;
  return what;
}
