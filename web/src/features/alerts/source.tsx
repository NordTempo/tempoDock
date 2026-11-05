import { Link } from "@tanstack/react-router";
import { useOrg } from "../../shell/location";
import type { Alert } from "./data";

// Where an alert came from, for the rows that show it (the project's
// problems, the console's alerts and overview): its message in plain
// words, a link named for its destination, and the CLI command it
// mentions, which moves to a copy control instead of the sentence.

/** A backticked `dock …` command in a message. */
const command = /`(dock [^`]+)`/;

/** The site and error group a site alert's command names. */
function siteOf(a: Alert) {
  const [, site, group] =
    a.message?.match(/`dock analytics errors (\S+)(?: ([0-9a-f]{16}))?`/) ??
    a.title.match(/^(?:Errors spiking|Traffic dropped) on (\S+)/) ??
    [];
  return site ? { site, group } : undefined;
}

/**
 * An alert's message without the CLI command in it, and the command. The
 * analytics and job alerts end on "; see `dock …`" or "`dock …` shows
 * its output."; an access request's says where to decide it, which the
 * row's link does instead.
 */
export function alertText(a: Alert): { text: string; cli?: string } {
  const message = a.message ?? "";
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
        <Link to="/$org/requests" params={{ org }} {...props}>
          Review request
        </Link>
      );
    case "backup_failed":
      if (svc)
        return (
          <Link
            to="/$org/$project/$environment/$service/backups"
            params={svc}
            {...props}
          >
            Open backups
          </Link>
        );
      return (
        <Link to="/console/backups" {...props}>
          Open backups
        </Link>
      );
    case "deploy_failed": {
      const number = a.message?.match(/^Release (\d+) failed/)?.[1];
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

/** The kind of an alert in a word or two, for a problem row's label. */
export const alertLabel = (a: Alert) =>
  ({
    new_error: "New error",
    error_spike: "Error spike",
    traffic_drop: "Traffic drop",
    access_request: "Request",
    disk: "Disk filling up",
    uptime_down: "Down",
    job_failed: "Job failed",
  })[a.kind] ?? "Alert";
