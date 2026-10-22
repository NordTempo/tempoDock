import { useOpenAlertsIfAdmin } from "../features/alerts/data";
import { isCrashLooping } from "../ui/status";
import { useProjectOverview, useServers } from "./queries";
import type { Problem, ServiceView } from "./types";

/** A domain needs a human until it is active. */
export const domainNeedsAttention = (d: { status: string }) =>
  d.status === "waiting_for_dns" || d.status === "error";

/**
 * A service is failing when it should be running but isn't: its container
 * crashed or keeps restarting, or its active release has no running
 * container (stopped, or lost).
 */
export function isFailing(s: ServiceView) {
  const { state } = s.service.status;
  if (state === "crashed" || isCrashLooping(s.service.status)) return true;
  const expected = !!s.service.active_release && !s.deploying;
  return expected && (state === "stopped" || state === "none");
}

/** Worst first: broken things, then things waiting on a human. */
const severity = (p: Problem) => {
  switch (p.kind) {
    case "crashed":
      return 0;
    case "deploy_failed":
      return 1;
    case "backup_failed":
      return 2;
    case "server_offline":
      return 3;
    case "alert":
      return p.alert.severity === "critical" ? 2 : 4;
    case "domain":
      return p.domain.status === "error" ? 4 : 5;
    case "staged":
      return 6;
  }
};

/**
 * Alert kinds the overview already derives from live state; their alerts
 * would show twice.
 */
const derived = new Set([
  "deploy_failed",
  "crash_loop",
  "domain_error",
  "server_offline",
  "backup_failed",
]);

/**
 * What needs a human on a project, derived from live state, worst first:
 * crashed services, failed latest releases, failed latest backups,
 * offline servers, domains failing or waiting for DNS, and staged changes
 * not applied yet. For admins, open alerts live state doesn't show join
 * them, such as a full disk on a server the project uses. Deploys in
 * flight are not problems; the overview shows them on their own.
 */
export function useProblems(project: string): Problem[] {
  const { environments } = useProjectOverview(project);
  const servers = useServers();
  const alerts = useOpenAlertsIfAdmin();
  const services = environments.flatMap((e) => e.services);

  const problems: Problem[] = [];
  for (const service of services) {
    const release = service.latest_release;
    if (isFailing(service)) problems.push({ kind: "crashed", service });
    if (!service.deploying && release?.status === "failed") {
      problems.push({ kind: "deploy_failed", service, release });
    }
    if (service.backup?.status === "failed") {
      problems.push({ kind: "backup_failed", service, backup: service.backup });
    }
    for (const domain of service.domains) {
      if (domainNeedsAttention(domain)) {
        problems.push({ kind: "domain", service, domain });
      }
    }
  }
  for (const environment of environments) {
    if (environment.pending_changes > 0) {
      problems.push({ kind: "staged", environment });
    }
  }
  const used = new Set(environments.map((e) => e.environment.server));
  for (const alert of alerts) {
    if (derived.has(alert.kind)) continue;
    const mine =
      alert.project === project ||
      (!alert.project && !!alert.server && used.has(alert.server));
    if (mine) problems.push({ kind: "alert", alert });
  }
  for (const server of servers) {
    if (server.status === "offline" && used.has(server.name)) {
      problems.push({ kind: "server_offline", server });
    }
  }
  return problems.sort((a, b) => severity(a) - severity(b));
}
