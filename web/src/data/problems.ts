import { useOrgOpenAlerts } from "../features/alerts/data";
import { isCrashLooping, isUnhealthy } from "../ui/status";
import { useProjectOverview, useServers } from "./queries";
import type { Problem, ServiceView } from "./types";

/** A domain needs a human until it is active. */
export const domainNeedsAttention = (d: { status: string }) =>
  d.status === "waiting_for_dns" || d.status === "error";

/**
 * A service is failing when it should be running but isn't: its container
 * crashed, keeps restarting or fails its health check, or its active
 * release lost its container. A service stopped on purpose (ADR 0050)
 * isn't failing.
 */
export function isFailing(s: ServiceView) {
  const { status } = s.service;
  const { state } = status;
  if (state === "crashed" || isCrashLooping(status) || isUnhealthy(status)) {
    return true;
  }
  const expected = !!s.service.active_release && !s.deploying;
  return expected && state === "none";
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
  "unhealthy",
  "domain_error",
  "server_offline",
  "backup_failed",
]);

/**
 * What needs a human on a project, derived from live state, worst first:
 * crashed or unhealthy services, failed latest releases, failed latest backups,
 * offline servers, domains failing or waiting for DNS, and staged changes
 * not applied yet. The org's open alerts about the project that live
 * state doesn't show join them, such as a failed job or an uptime check
 * that is down (platform alerts, about servers, are the operator's).
 * Deploys in flight are not problems; the overview shows them on their
 * own.
 */
export function useProblems(project: string): Problem[] {
  const { environments } = useProjectOverview(project);
  const servers = useServers();
  const alerts = useOrgOpenAlerts();
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
  for (const alert of alerts) {
    if (!derived.has(alert.kind) && alert.project === project) {
      problems.push({ kind: "alert", alert });
    }
  }
  const used = new Set(environments.map((e) => e.environment.server));
  for (const server of servers) {
    if (server.status === "offline" && used.has(server.name)) {
      problems.push({ kind: "server_offline", server });
    }
  }
  return problems.sort((a, b) => severity(a) - severity(b));
}
