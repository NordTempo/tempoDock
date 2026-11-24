import type { components } from "../../api/schema";

// Open alerts in words, for the Overview's one line and the Inbox's rows:
// alerts of one kind about one place grouped ("3 new errors on
// shop.example.com"), and what needs someone as a short list ("an access
// request, a failed backup and 3 new errors"). Pure, so the wording is
// unit tested.

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

/** One kind of news in a few words, by how many: "a failed backup". */
const phrases: Record<string, [one: string, many: string]> = {
  access_request: ["an access request", "access requests"],
  backup_failed: ["a failed backup", "failed backups"],
  deploy_failed: ["a failed deploy", "failed deploys"],
  crash_loop: ["a service crash looping", "services crash looping"],
  unhealthy: ["an unhealthy service", "unhealthy services"],
  uptime_down: ["a check failing", "checks failing"],
  domain_error: ["a domain not working", "domains not working"],
  job_failed: ["a failed job", "failed jobs"],
  new_vulnerability: ["new vulnerabilities", "new vulnerabilities"],
  traffic_drop: ["a traffic drop", "traffic drops"],
  connection_failed: ["a broken connection", "broken connections"],
  certificate_failed: ["a certificate problem", "certificate problems"],
  update_failed: ["a failed update", "failed updates"],
  server_offline: ["a server offline", "servers offline"],
  disk: ["a disk filling up", "disks filling up"],
  memory: ["memory running low", "memory running low"],
  cpu: ["a busy CPU", "busy CPUs"],
};

/** "a", "a and b", "a, b and c". */
const listed = (parts: string[]) =>
  parts.length < 2
    ? (parts[0] ?? "")
    : `${parts.slice(0, -1).join(", ")} and ${parts.at(-1)}`;

/**
 * What needs someone, as one phrase in the order the alerts come (worst
 * first): each kind once, counted when more than one ("an access
 * request, a failed backup and 3 new errors"). Empty for no alerts.
 */
export function needsPhrase(alerts: Alert[]) {
  const counts = new Map<string, number>();
  for (const a of alerts) {
    const kind = errorKinds.has(a.kind) ? "errors" : a.kind;
    counts.set(kind, (counts.get(kind) ?? 0) + 1);
  }
  const allNew = alerts
    .filter((a) => errorKinds.has(a.kind))
    .every((a) => a.kind === "new_error");
  const parts = [...counts].map(([kind, n]) => {
    if (kind === "errors") {
      const what = allNew ? "new error" : "error alert";
      return n === 1 ? `a ${what}` : `${n} ${what}s`;
    }
    const [one, many] = phrases[kind] ?? ["an alert", "alerts"];
    return n === 1 ? one : one === many ? many : `${n} ${many}`;
  });
  return listed(parts);
}
