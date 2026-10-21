import { useSuspenseQueries } from "@tanstack/react-query";
import { queries, serviceRef, useProjectOverview, useServers } from "./queries";
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
  if (state === "crashed" || state === "restarting") return true;
  const expected = !!s.service.active_release && !s.deploying;
  return expected && (state === "stopped" || state === "none");
}

/**
 * What needs a human on a project, derived from live state: crashed
 * services, deploys in flight, failed latest releases, domains waiting for
 * DNS or failing, staged changes not applied yet, and offline servers.
 */
export function useProblems(project: string): Problem[] {
  const { environments } = useProjectOverview(project);
  const servers = useServers();
  const services = environments.flatMap((e) => e.services);
  // The overview has no failed releases, so each service's history is read.
  const latest = useSuspenseQueries({
    queries: services.map((s) => queries.releases(serviceRef(s.service))),
    combine: (results) => results.map((r) => r.data[0]),
  });

  const problems: Problem[] = [];
  services.forEach((service, i) => {
    const release = latest[i];
    if (isFailing(service)) problems.push({ kind: "crashed", service });
    if (service.deploying) {
      problems.push({ kind: "deploying", service, release: service.deploying });
    } else if (release?.status === "failed") {
      problems.push({ kind: "deploy_failed", service, release });
    }
    for (const domain of service.domains) {
      if (domainNeedsAttention(domain)) {
        problems.push({ kind: "domain", service, domain });
      }
    }
  });
  for (const environment of environments) {
    if (environment.pending_changes > 0) {
      problems.push({ kind: "staged", environment });
    }
  }
  const used = new Set(environments.map((e) => e.environment.server));
  for (const server of servers) {
    if (server.status === "offline" && used.has(server.name)) {
      problems.push({ kind: "server_offline", server });
    }
  }
  return problems;
}
