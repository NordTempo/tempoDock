import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useOrg } from "../../shell/location";
import { since } from "../../ui/format";
import { Button, ErrorText, SectionHead } from "../../ui/kit";
import { type Alert, useOrgOpenAlerts } from "../alerts/data";
import { AlertLink, alertText } from "../alerts/source";
import {
  byState,
  type ErrorFacet,
  type ErrorFilter,
  type ErrorIssue,
  useErrorIssue,
  useErrors,
  useResolveError,
} from "./data";

// The errors view (ADR 0062, mock B): the org's open alerts on top, then
// error issues grouped by service, new and regressed first. On the org's
// Errors page a tree of projects and services (with counts) picks what
// the list shows; a service's Errors tab is the same view fixed to it,
// without the tree.

/** Issues the view renders per service before "Show all". */
const PER_SERVICE = 20;

const stateChip: Record<ErrorIssue["state"], string> = {
  new: "text-fail bg-fail-bg",
  regressed: "text-warn-text bg-warn-bg",
  ongoing: "text-mut bg-hover",
  resolved: "text-faint bg-hover",
};

export function ErrorsView({
  filter,
  onFilter,
  fixed,
}: {
  filter: ErrorFilter;
  onFilter: (next: ErrorFilter) => void;
  /** A service's tab: the filter is its service, and there is no tree. */
  fixed?: boolean;
}) {
  const list = useErrors(filter);
  const alerts = useOrgOpenAlerts().filter(
    (a) =>
      (!filter.project || a.project === filter.project) &&
      (!filter.environment || a.environment === filter.environment) &&
      (!filter.service || a.service === filter.service),
  );
  const groups = groupByService(list.issues ?? []);
  const content = (
    <div className="flex min-w-0 flex-col gap-4">
      {alerts.length > 0 && (
        <section aria-label="Open alerts" className="flex flex-col gap-1">
          {alerts.map((a) => (
            <AlertStrip key={a.id} alert={a} />
          ))}
        </section>
      )}
      {groups.length === 0 ? (
        <p className="text-mut">
          {filter.all
            ? "No errors seen."
            : "No open errors. New ones show here as they happen."}
        </p>
      ) : (
        groups.map((g) => <ServiceIssues key={g.key} group={g} />)
      )}
      {list.truncated && (
        <p className="text-xs text-faint">
          The newest issues only; pick a project or service to see the rest.
        </p>
      )}
      <label className="flex items-center gap-1.5 text-xs text-sec">
        <input
          type="checkbox"
          className="accent-fg"
          checked={!!filter.all}
          onChange={(e) => onFilter({ ...filter, all: e.target.checked })}
        />
        Show resolved issues too
      </label>
    </div>
  );
  if (fixed) return content;
  return (
    <div className="grid gap-x-8 gap-y-6 md:grid-cols-[220px_minmax(0,1fr)]">
      <Facets facets={list.facets ?? []} filter={filter} onFilter={onFilter} />
      {content}
    </div>
  );
}

/** One open alert as a ruled, tinted strip with the way to it. */
function AlertStrip({ alert: a }: { alert: Alert }) {
  const critical = a.severity === "critical";
  const { text } = alertText(a);
  return (
    <div
      className={`flex flex-wrap items-baseline gap-x-3 gap-y-1 rounded-r-[6px] border-l-2 px-3 py-2 ${critical ? "border-fail bg-fail-bg" : "border-warn bg-warn-bg"}`}
    >
      <span className={critical ? "text-fail" : "text-warn"}>{a.severity}</span>
      <span className="min-w-0 flex-1">
        {a.title}
        {text && <span className="block text-xs text-sec">{text}</span>}
      </span>
      <span className="text-xs text-mut">{since(a.opened_at)}</span>
      <AlertLink alert={a} className="btn sm no-underline" />
    </div>
  );
}

type Group = {
  key: string;
  project: string;
  environment: string;
  service: string;
  issues: ErrorIssue[];
  events: number;
};

/** Issues by service, the services with new or regressed issues first. */
function groupByService(issues: ErrorIssue[]): Group[] {
  const groups = new Map<string, Group>();
  for (const i of [...issues].sort(byState)) {
    const key = `${i.project}/${i.environment}/${i.service}`;
    let g = groups.get(key);
    if (!g) {
      g = {
        key,
        project: i.project,
        environment: i.environment,
        service: i.service,
        issues: [],
        events: 0,
      };
      groups.set(key, g);
    }
    g.issues.push(i);
    g.events += (i.trend ?? []).reduce((n, c) => n + c, 0);
  }
  return [...groups.values()];
}

/**
 * The tree of where errors are: environments, then projects and their
 * services with open issues, each with its count (new ones in red).
 */
function Facets({
  facets,
  filter,
  onFilter,
}: {
  facets: ErrorFacet[];
  filter: ErrorFilter;
  onFilter: (next: ErrorFilter) => void;
}) {
  const envs = [...new Set(facets.map((f) => f.environment))].sort();
  const projects = new Map<string, ErrorFacet[]>();
  for (const f of facets) {
    if (filter.environment && f.environment !== filter.environment) continue;
    projects.set(f.project, [...(projects.get(f.project) ?? []), f]);
  }
  const total = (fs: ErrorFacet[]) => fs.reduce((n, f) => n + f.issues, 0);
  const fresh = (fs: ErrorFacet[]) => fs.reduce((n, f) => n + f.new, 0);
  const row = (
    label: string,
    fs: ErrorFacet[],
    on: boolean,
    next: ErrorFilter,
    sub?: boolean,
  ) => (
    <button
      key={label + (sub ? "-s" : "")}
      type="button"
      aria-pressed={on}
      onClick={() => onFilter(next)}
      className={`flex w-full items-baseline gap-2 rounded-[6px] px-2 py-1 text-left text-[13px] hover:bg-hover aria-pressed:bg-sel aria-pressed:text-fg ${sub ? "pl-5 text-sec" : ""}`}
    >
      <span className="min-w-0 flex-1 truncate">{label}</span>
      <span
        className={`font-mono text-xs tabular-nums ${fresh(fs) ? "text-fail" : "text-mut"}`}
      >
        {total(fs)}
      </span>
    </button>
  );
  return (
    <nav aria-label="Where errors are" className="flex flex-col gap-3">
      {envs.length > 1 && (
        <div>
          <h2 className="mb-1 px-2 text-xs text-faint">Environment</h2>
          {row("All", facets, !filter.environment, {
            ...filter,
            environment: undefined,
          })}
          {envs.map((env) =>
            row(
              env,
              facets.filter((f) => f.environment === env),
              filter.environment === env,
              { ...filter, environment: env, service: undefined },
            ),
          )}
        </div>
      )}
      <div>
        <h2 className="mb-1 px-2 text-xs text-faint">Project and service</h2>
        {row("All", [...projects.values()].flat(), !filter.project, {
          ...filter,
          project: undefined,
          service: undefined,
        })}
        {[...projects.entries()]
          .sort(([a], [b]) => a.localeCompare(b))
          .map(([project, fs]) => (
            <div key={project}>
              {row(project, fs, filter.project === project && !filter.service, {
                ...filter,
                project,
                service: undefined,
              })}
              {filter.project === project &&
                fs
                  .sort((a, b) => b.new - a.new || b.issues - a.issues)
                  .map((f) =>
                    row(
                      envs.length > 1 && !filter.environment
                        ? `${f.service} · ${f.environment}`
                        : f.service,
                      [f],
                      filter.service === f.service &&
                        (!filter.environment ||
                          filter.environment === f.environment),
                      {
                        ...filter,
                        project,
                        service: f.service,
                        environment: filter.environment ?? f.environment,
                      },
                      true,
                    ),
                  )}
            </div>
          ))}
      </div>
    </nav>
  );
}

/** One service's issues under its name. */
function ServiceIssues({ group: g }: { group: Group }) {
  const org = useOrg();
  const [all, setAll] = useState(false);
  const shown = all ? g.issues : g.issues.slice(0, PER_SERVICE);
  return (
    <section aria-label={g.key}>
      <SectionHead
        aside={`${g.issues.length} ${g.issues.length === 1 ? "issue" : "issues"} · ${g.events} in 24 hours`}
        right={
          <Link
            to="/$org/$project/$environment/$service/logs"
            params={{
              org,
              project: g.project,
              environment: g.environment,
              service: g.service,
            }}
            className="text-xs text-mut hover:text-fg hover:underline"
          >
            Logs →
          </Link>
        }
      >
        <span className="font-mono">
          {g.project}/{g.service}
        </span>
        <span className="ml-2 text-xs font-normal text-mut">
          {g.environment}
        </span>
      </SectionHead>
      {/* Column heads, on the rows' grid: what each number is. */}
      <div
        aria-hidden
        className="grid grid-cols-[84px_minmax(0,1fr)_auto_76px_96px] gap-x-3 border-t border-line px-1 pt-2 pb-1 text-xs text-mut"
      >
        <span>State</span>
        <span>Error</span>
        <span>Source</span>
        <span className="text-right">Events</span>
        <span className="text-right">24 h, since</span>
      </div>
      <ul className="border-t border-soft">
        {shown.map((i) => (
          <IssueRow key={i.id} issue={i} />
        ))}
      </ul>
      {g.issues.length > shown.length && (
        <Button small className="mt-2" onClick={() => setAll(true)}>
          Show all {g.issues.length}
        </Button>
      )}
    </section>
  );
}

/** An issue's row; opening it shows its latest sample and releases. */
function IssueRow({ issue: i }: { issue: ErrorIssue }) {
  const [open, setOpen] = useState(false);
  return (
    <li className="border-b border-soft">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="grid w-full grid-cols-[84px_minmax(0,1fr)_auto_76px_96px] items-baseline gap-x-3 px-1 py-2 text-left hover:bg-hover"
      >
        <span>
          <span
            className={`rounded-full px-2 text-[11.5px] leading-[18px] ${stateChip[i.state]}`}
          >
            {i.state}
          </span>
        </span>
        <span className="min-w-0">
          <span className="block truncate font-medium" title={i.title}>
            {i.title}
          </span>
          {i.culprit && (
            <span className="block truncate font-mono text-xs text-faint">
              {i.culprit}
            </span>
          )}
        </span>
        <span className="text-xs text-mut">{i.source}</span>
        <span
          className="text-right font-mono text-xs tabular-nums"
          title={`${i.count} events`}
        >
          {i.count}
        </span>
        <span className="flex flex-col items-end gap-0.5">
          <Spark trend={i.trend ?? []} state={i.state} />
          <span className="text-xs text-mut">{releaseNote(i)}</span>
        </span>
      </button>
      {open && <IssueDetail issue={i} />}
    </li>
  );
}

/** Where an issue started: "since r48", "back in r48", or how long ago. */
function releaseNote(i: ErrorIssue): string {
  if (i.state === "regressed" && i.last_release)
    return `back in r${i.last_release}`;
  if (i.first_release) return `since r${i.first_release}`;
  return `${since(i.first_seen)} old`;
}

/** The last 24 hours, an hour a point; nothing moves once drawn. */
function Spark({
  trend,
  state,
}: {
  trend: number[];
  state: ErrorIssue["state"];
}) {
  const w = 72;
  const h = 16;
  const top = Math.max(1, ...trend);
  const step = trend.length > 1 ? w / (trend.length - 1) : w;
  const points = trend
    .map(
      (n, x) =>
        `${(x * step).toFixed(1)},${(h - 1 - (n / top) * (h - 2)).toFixed(1)}`,
    )
    .join(" ");
  const stroke =
    state === "new"
      ? "var(--color-fail)"
      : state === "regressed"
        ? "var(--color-warn)"
        : "var(--color-mut)";
  return (
    <svg width={w} height={h} aria-hidden className="block">
      <polyline fill="none" stroke={stroke} strokeWidth="1.5" points={points} />
    </svg>
  );
}

/** An opened issue: its sample, where it started, and Resolve. */
function IssueDetail({ issue: i }: { issue: ErrorIssue }) {
  const org = useOrg();
  const detail = useErrorIssue(i.id);
  const resolve = useResolveError();
  const svc = {
    org,
    project: i.project,
    environment: i.environment,
    service: i.service,
  };
  return (
    <div className="flex flex-col gap-2 bg-panel px-3 py-3">
      <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-[13px] [&_dt]:text-mut">
        <dt>First seen</dt>
        <dd>
          {since(i.first_seen)} ago
          {i.first_release ? (
            <>
              {" in "}
              <Link
                to="/$org/$project/$environment/$service/history/$number"
                params={{ ...svc, number: String(i.first_release) }}
              >
                release {i.first_release}
              </Link>
            </>
          ) : null}
        </dd>
        <dt>Last seen</dt>
        <dd>
          {since(i.last_seen)} ago
          {i.last_release ? ` in release ${i.last_release}` : ""}
        </dd>
        {i.resolved_at && (
          <>
            <dt>Resolved</dt>
            <dd>
              {since(i.resolved_at)} ago
              {i.resolved_by && ` by ${i.resolved_by}`}
            </dd>
          </>
        )}
      </dl>
      {detail.data?.sample ? (
        <pre className="max-h-[320px] overflow-auto rounded-panel border border-line bg-side px-3 py-2 font-mono text-xs whitespace-pre-wrap text-sec">
          {detail.data.sample}
        </pre>
      ) : (
        <ErrorText error={detail.error} />
      )}
      <div className="flex items-center gap-2">
        {i.state !== "resolved" && (
          <Button
            small
            disabled={resolve.isPending}
            title="Mark it fixed; it comes back as regressed if it happens again"
            onClick={() => resolve.mutate(i.id)}
          >
            Resolve
          </Button>
        )}
        <ErrorText error={resolve.error} />
        <span className="text-xs text-faint">
          fingerprint{" "}
          <span className="font-mono">{i.fingerprint.slice(0, 12)}</span>
        </span>
      </div>
    </div>
  );
}
