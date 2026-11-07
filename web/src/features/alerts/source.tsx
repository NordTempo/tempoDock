import { Link } from "@tanstack/react-router";
import { useOrg } from "../../shell/location";
import type { Alert } from "./data";

// Where an alert came from, for the rows that show it (the project's
// problems, the console's alerts and overview): its message in plain
// words, a link named for its destination, and its CLI command for a
// copy control. Alerts carry `command` and `target` for these; alerts
// raised before those fields existed have neither, and only for them is
// the title and message parsed (`legacy` below).

/** Raised before alerts had `command` and `target`: parse the text. */
const legacy = (a: Alert) => a.command === undefined && !a.target;

/** A backticked `dock …` command in a legacy message. */
const command = /`(dock [^`]+)`/;

/** The site and error group a site alert points at. */
function siteOf(a: Alert) {
  if (a.target?.site) {
    return { site: a.target.site, group: a.target.error_group };
  }
  if (!legacy(a)) return undefined;
  const [, site, group] =
    a.message?.match(/`dock analytics errors (\S+)(?: ([0-9a-f]{16}))?`/) ??
    a.title.match(/^(?:Errors spiking|Traffic dropped) on (\S+)/) ??
    [];
  return site ? { site, group } : undefined;
}

/**
 * An alert's message and its CLI command. A legacy message has the
 * command in it: the analytics and job alerts end on "; see `dock …`" or
 * "`dock …` shows its output.", which is cut out; an access request's
 * says where to decide it, which the row's link does instead.
 */
export function alertText(a: Alert): { text: string; cli?: string } {
  // An access request's says where to decide it (for channels: email,
  // chat); the row's Review link does that here.
  const message = (a.message ?? "")
    .replace(/^Approve or reject it in Dock, under Requests\.\s*/, "")
    .trim();
  if (!legacy(a)) return { text: message, cli: a.command || undefined };
  const cli = message.match(command)?.[1];
  const text = message
    .replace(/;\s*see `dock [^`]+`/, "")
    .replace(/\s*`dock [^`]+` shows its output\./, "")
    .replace(/^Approve or reject it in Dock, under Requests\.\s*/, "")
    .trim();
  return { text, cli };
}

/**
 * The link to an alert's source, named for where it goes ("Open error",
 * "Review request"); nothing for kinds with no page of their own.
 */
export function AlertLink({
  alert: a,
  className,
  ...rest
}: {
  alert: Alert;
  className?: string;
  /** A hover note, such as the CLI command the message named. */
  title?: string;
  "data-problem"?: number;
}) {
  const org = useOrg();
  const props = { className, ...rest };
  const svc =
    a.project && a.environment && a.service
      ? {
          org,
          project: a.project,
          environment: a.environment,
          service: a.service,
        }
      : undefined;
  const site = siteOf(a);
  switch (a.kind) {
    case "new_error":
    case "error_spike":
      if (!a.project || !site) break;
      return (
        <Link
          to="/$org/analytics/$project/$site/errors"
          params={{ org, project: a.project, site: site.site }}
          search={site.group ? { g: site.group } : {}}
          {...props}
        >
          {site.group ? "Open error" : "Open errors"}
        </Link>
      );
    case "traffic_drop":
      if (!a.project || !site) break;
      return (
        <Link
          to="/$org/analytics/$project/$site"
          params={{ org, project: a.project, site: site.site }}
          search={{ range: "24h" }}
          {...props}
        >
          Open analytics
        </Link>
      );
    case "access_request":
      return (
        <Link
          to="/$org/requests"
          params={{ org }}
          search={a.target?.request ? { id: a.target.request } : {}}
          {...props}
        >
          Review request
        </Link>
      );
    case "backup_failed": {
      // The failed run's row (#backup-<id>), whose log says why.
      const run = a.target?.backup ? `backup-${a.target.backup}` : undefined;
      if (svc)
        return (
          <Link
            to="/$org/$project/$environment/$service/backups"
            params={svc}
            hash={run}
            {...props}
          >
            {run ? "See why" : "Open backups"}
          </Link>
        );
      return (
        <Link to="/console/backups" hash={run} {...props}>
          {run ? "See why" : "Open backups"}
        </Link>
      );
    }
    case "deploy_failed": {
      const number = a.target?.release
        ? String(a.target.release)
        : legacy(a)
          ? a.message?.match(/^Release (\d+) failed/)?.[1]
          : undefined;
      if (!svc) break;
      return number ? (
        <Link
          to="/$org/$project/$environment/$service/releases/$number"
          params={{ ...svc, number }}
          {...props}
        >
          See why
        </Link>
      ) : (
        <Link
          to="/$org/$project/$environment/$service/releases"
          params={svc}
          {...props}
        >
          Open releases
        </Link>
      );
    }
    case "crash_loop":
    case "unhealthy":
      if (!svc) break;
      return (
        <Link
          to="/$org/$project/$environment/$service/logs"
          params={svc}
          {...props}
        >
          Open logs
        </Link>
      );
    case "domain_error":
      if (!svc) break;
      return (
        <Link
          to="/$org/$project/$environment/$service/domains"
          params={svc}
          {...props}
        >
          Open domains
        </Link>
      );
    case "uptime_down":
      if (!svc) break;
      return (
        <Link
          to="/$org/$project/$environment/$service/uptime"
          params={svc}
          {...props}
        >
          Open uptime
        </Link>
      );
    case "job_failed":
      if (!svc) break;
      return (
        <Link
          to="/$org/$project/$environment/$service/jobs"
          params={svc}
          {...props}
        >
          Open jobs
        </Link>
      );
    case "update_failed":
      return (
        <Link to="/console/updates" {...props}>
          Open updates
        </Link>
      );
    case "connection_failed": {
      const [kind] = (a.target?.connection ?? "").split(":");
      const to = connectionPages[kind ?? ""];
      if (!to) break;
      return (
        <Link to={to} {...props}>
          Open {kind === "github" ? "GitHub" : kind}
        </Link>
      );
    }
  }
  if (a.server)
    return (
      <Link
        to="/console/servers/$server"
        params={{ server: a.server }}
        {...props}
      >
        Open server
      </Link>
    );
  if (svc)
    return (
      <Link to="/$org/$project/$environment/$service" params={svc} {...props}>
        Open service
      </Link>
    );
  return null;
}

/** The console page of each kind of platform connection. */
const connectionPages: Record<
  string,
  | "/console/cloudflare"
  | "/console/github"
  | "/console/storage"
  | "/console/registry"
> = {
  cloudflare: "/console/cloudflare",
  github: "/console/github",
  storage: "/console/storage",
  registry: "/console/registry",
};

/** The kind of an alert in a word or two, for a problem row's label. */
export const alertLabel = (a: Alert) =>
  ({
    new_error: "New error",
    error_spike: "Error spike",
    traffic_drop: "Traffic drop",
    access_request: "Request",
    disk: "Disk filling up",
    memory: "Memory pressure",
    cpu: "CPU busy",
    update_failed: "Update failed",
    connection_failed: "Connection failing",
    uptime_down: "Down",
    job_failed: "Job failed",
  })[a.kind] ?? "Alert";
