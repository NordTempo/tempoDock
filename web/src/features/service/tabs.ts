import { isDatabase } from "../../data/queries";
import type { ServiceView } from "../../data/types";

/**
 * A service's tabs: label, route, G-sequence key and group (watching it,
 * working with it, its record; the strip draws a line between groups).
 * Settings is always the last tab, at every level (project, service), so
 * "to change something, open its Settings" has one place to look. Tabs
 * every service has come first in each group, so they keep their place
 * from one service to the next. The tab strip, the palette's "Logs · web
 * production" commands and the ? help all read this list, so a new tab
 * shows up everywhere. Kept out of ServiceHeader so the shell chunk
 * doesn't pull the header in.
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
  // A terminal in the running container (ADR 0110), for those with admin
  // there (canShell). G X, "exec".
  {
    label: "Shell",
    to: "/$org/$project/$environment/$service/shell",
    key: "g x",
    group: "operate",
  },
  // Database services only (isDatabase). G B, "base".
  {
    label: "Data",
    to: "/$org/$project/$environment/$service/data",
    key: "g b",
    group: "operate",
  },
  {
    label: "Variables",
    to: "/$org/$project/$environment/$service/variables",
    key: "g v",
    group: "operate",
  },
  // Releases and the audit trail; a release opens under it (ADR 0076).
  // History and Settings are the service's record and its configuration.
  {
    label: "History",
    to: "/$org/$project/$environment/$service/history",
    key: "g h",
    group: "record",
  },
  // Everything that configures it, on one page with a list of sections:
  // source, running, deploys, health and uptime, domains and, when
  // stateful, backups. Last, as on every level. G S, settings everywhere.
  {
    label: "Settings",
    to: "/$org/$project/$environment/$service/settings",
    key: "g s",
    group: "record",
  },
] as const;

export type ServiceTab = (typeof serviceTabs)[number];

/**
 * The tabs this service has: Data, Errors and Requests only where they
 * apply, Shell only for those who may open one (canShell).
 */
export const tabsFor = (
  view: Pick<ServiceView, "service" | "domains">,
  canShell: boolean,
) =>
  serviceTabs.filter((tab) => {
    switch (tab.label) {
      case "Shell":
        return canShell;
      case "Data":
        return isDatabase(view.service);
      case "Errors":
      case "Requests":
        return view.domains.length > 0;
      default:
        return true;
    }
  });
