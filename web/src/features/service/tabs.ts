import { isDatabase } from "../../data/queries";
import type { ServiceView } from "../../data/types";

/**
 * A service's tabs: label, route, G-sequence key and group (watching it,
 * operating it, configuring it, its history; the strip draws a line
 * between groups). Tabs every service has come first in each group, so
 * they keep their place from one service to the next. The tab strip, the
 * palette's "Logs · web production" commands and the ? help all read
 * this list, so a new tab shows up everywhere. Kept out of
 * ServiceHeader so the shell chunk doesn't pull the header in.
 */
export const serviceTabs = [
  {
    label: "Overview",
    to: "/$org/$project/$environment/$service",
    key: "g o",
    group: "watch",
  },
  {
    label: "Logs",
    to: "/$org/$project/$environment/$service/logs",
    key: "g l",
    group: "watch",
  },
  // Services with a domain only: what reached them. Traffic (ADR 0033),
  // Visitors (ADR 0037) and, with tracing, Traces (ADR 0075).
  {
    label: "Requests",
    to: "/$org/$project/$environment/$service/requests",
    key: "g t",
    group: "watch",
  },
  // Services with a domain only: their site's browser errors for now
  // (ADR 0059), next to Logs and Requests, the other places problems show.
  {
    label: "Errors",
    to: "/$org/$project/$environment/$service/errors",
    key: "g e",
    group: "watch",
  },
  // CPU and memory, and where a service has them its uptime checks
  // (ADR 0055) and the health check on its server: Resources and Uptime
  // in one (ADR 0076).
  {
    label: "Metrics",
    to: "/$org/$project/$environment/$service/metrics",
    key: "g m",
    group: "watch",
  },
  // Commands run once (ADR 0048) and scheduled jobs (ADR 0054), one tab
  // (ADR 0076). G J, "jobs".
  {
    label: "Jobs",
    to: "/$org/$project/$environment/$service/jobs",
    key: "g j",
    group: "operate",
  },
  // Database services only (isDatabase). G B, "base": G D is Domains.
  {
    label: "Data",
    to: "/$org/$project/$environment/$service/data",
    key: "g b",
    group: "operate",
  },
  // Stateful services only. G K, "keep": G B is Data.
  {
    label: "Backups",
    to: "/$org/$project/$environment/$service/backups",
    key: "g k",
    group: "operate",
  },
  {
    label: "Domains",
    to: "/$org/$project/$environment/$service/domains",
    key: "g d",
    group: "configure",
  },
  {
    label: "Variables",
    to: "/$org/$project/$environment/$service/variables",
    key: "g v",
    group: "configure",
  },
  // Source, builder, commands, health check, resources, replicas and
  // server: what was Settings (ADR 0059), so G S still.
  {
    label: "Build & run",
    to: "/$org/$project/$environment/$service/build",
    key: "g s",
    group: "configure",
  },
  {
    label: "Activity",
    to: "/$org/$project/$environment/$service/audit",
    key: "g a",
    group: "history",
  },
] as const;

export type ServiceTab = (typeof serviceTabs)[number];

/** The tabs this service has: Data, Backups, Errors and Requests only where they apply. */
export const tabsFor = (view: Pick<ServiceView, "service" | "domains">) =>
  serviceTabs.filter((tab) => {
    switch (tab.label) {
      case "Data":
        return isDatabase(view.service);
      case "Backups":
        return view.service.stateful;
      case "Errors":
      case "Requests":
        return view.domains.length > 0;
      default:
        return true;
    }
  });
