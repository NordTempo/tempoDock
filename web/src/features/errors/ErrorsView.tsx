import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { useOrg } from "../../shell/location";
import { since } from "../../ui/format";
import { Button, ErrorText, Menu, menuItem, SectionHead } from "../../ui/kit";
import {
  byState,
  type ErrorFacet,
  type ErrorFilter,
  type ErrorIssue,
  useErrorIssue,
  useErrors,
  useResolveError,
} from "./data";

// The errors view (ADR 0062, mock B): error issues grouped by service, new
// and regressed first, the full width. Open alerts are the Inbox's (ADR
// 0076), so they aren't repeated here. On the org's Errors page the
// header's path picker (ErrorsPicker) picks what the list shows; a
// service's Errors tab is the same view fixed to it.

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
}: {
  filter: ErrorFilter;
  onFilter: (next: ErrorFilter) => void;
}) {
  const list = useErrors(filter);
  const groups = groupByService(list.issues ?? []);
  const content = (
    <div className="flex min-w-0 flex-col gap-4">
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
  return content;
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
 * Where errors are, as a path in the page header (plan item 6, a combo
 * of mocks A and B): All projects ▾ / All services ▾, and the
 * environment when there are several. Each segment is a menu with the
 * open issues of each choice (red when some are new); the services
 * segment opens once a project is picked. The counts come from the
 * errors API's facets, which ignore the project and service filter, so
 * they stay put while you switch.
 */
export function ErrorsPicker({
  filter,
  onFilter,
}: {
  filter: ErrorFilter;
  onFilter: (next: ErrorFilter) => void;
}) {
  const facets = useErrors(filter).facets ?? [];
  const envs = [...new Set(facets.map((f) => f.environment))].sort();
  const inEnv = facets.filter(
    (f) => !filter.environment || f.environment === filter.environment,
  );
  const projects = [...new Set(inEnv.map((f) => f.project))].sort();
  const services = inEnv
    .filter((f) => f.project === filter.project)
    .sort((a, b) => b.new - a.new || b.issues - a.issues);
  const withEnv = envs.length > 1 && !filter.environment;
  return (
    <span className="flex flex-wrap items-center gap-1 font-mono text-xs">
      <Segment
        label="Project"
        text={filter.project ?? "All projects"}
        on={!!filter.project}
        items={[
          {
            key: "",
            text: "All projects",
            facets: inEnv,
            on: !filter.project,
            next: { ...filter, project: undefined, service: undefined },
          },
          ...projects.map((p) => ({
            key: p,
            text: p,
            facets: inEnv.filter((f) => f.project === p),
            on: filter.project === p,
            next: { ...filter, project: p, service: undefined },
          })),
        ]}
        onPick={onFilter}
      />
      <span aria-hidden className="text-faint">
        /
      </span>
      {filter.project ? (
        <Segment
          label="Service"
          text={filter.service ?? "All services"}
          on={!!filter.service}
          items={[
            {
              key: "",
              text: "All services",
              facets: services,
              on: !filter.service,
              next: { ...filter, service: undefined },
            },
            ...services.map((f) => ({
              key: `${f.environment}/${f.service}`,
              text: withEnv ? `${f.service} · ${f.environment}` : f.service,
              facets: [f],
              on:
                filter.service === f.service &&
                (!filter.environment || filter.environment === f.environment),
              next: {
                ...filter,
                service: f.service,
                environment: filter.environment ?? f.environment,
              },
            })),
          ]}
          onPick={onFilter}
        />
      ) : (
        <span className="btn sm text-faint" title="Pick a project first">
          All services <span aria-hidden>▾</span>
        </span>
      )}
      {envs.length > 1 && (
        <Segment
          label="Environment"
          text={filter.environment ?? "All environments"}
          on={!!filter.environment}
          items={[
            {
              key: "",
              text: "All environments",
              facets,
              on: !filter.environment,
              next: { ...filter, environment: undefined },
            },
            ...envs.map((e) => ({
              key: e,
              text: e,
              facets: facets.filter((f) => f.environment === e),
              on: filter.environment === e,
              next: { ...filter, environment: e, service: undefined },
            })),
          ]}
          onPick={onFilter}
        />
      )}
    </span>
  );
}

/** One segment of the picker: its choice as a button, the choices in a menu. */
function Segment({
  label,
  text,
  on,
  items,
  onPick,
}: {
  label: string;
  text: string;
  on: boolean;
  items: {
    key: string;
    text: string;
    facets: ErrorFacet[];
    on: boolean;
    next: ErrorFilter;
  }[];
  onPick: (next: ErrorFilter) => void;
}) {
  const total = (fs: ErrorFacet[]) => fs.reduce((n, f) => n + f.issues, 0);
  const fresh = (fs: ErrorFacet[]) => fs.reduce((n, f) => n + f.new, 0);
  return (
    <Menu
      label={`${label}: ${text}`}
      button={
        <span className={`btn sm font-mono ${on ? "border-fg text-fg" : ""}`}>
          {text} <span aria-hidden>▾</span>
        </span>
      }
    >
      {(close) => (
        <div className="max-h-80 min-w-56 overflow-y-auto">
          {items.map((it) => {
            const n = fresh(it.facets);
            return (
              <button
                key={it.key}
                type="button"
                role="menuitem"
                aria-current={it.on ? "page" : undefined}
                className={`${menuItem} w-full font-mono text-xs`}
                onClick={() => {
                  onPick(it.next);
                  close();
                }}
              >
                <span className="min-w-0 flex-1 truncate">{it.text}</span>
                <span
                  className={`tabular-nums ${n ? "text-fail" : "text-mut"}`}
                >
                  {total(it.facets)}
                  {n > 0 && ` · ${n} new`}
                </span>
              </button>
            );
          })}
        </div>
      )}
    </Menu>
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
            className={`rounded-full px-2 text-tag leading-[18px] ${stateChip[i.state]}`}
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
      <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-xs [&_dt]:text-mut">
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
        {detail.data?.latest_trace && (
          <>
            <dt>Latest trace</dt>
            <dd>
              <Link
                to="/$org/$project/$environment/$service/requests/traces/$trace"
                params={{ ...svc, trace: detail.data.latest_trace.id }}
                search={{ at: detail.data.latest_trace.at }}
              >
                {detail.data.latest_trace.id.slice(0, 8)}
              </Link>{" "}
              {since(detail.data.latest_trace.at)} ago
            </dd>
          </>
        )}
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
