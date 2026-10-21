import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useProblems } from "../../data/problems";
import { serviceRef } from "../../data/queries";
import type { Problem, ServiceView } from "../../data/types";
import { since } from "../../ui/format";
import { Glyph } from "../../ui/kit";
import { outOfMemory, releaseStatus, type Tone } from "../../ui/status";

/** The overview's "needs a human" list. Renders nothing when all is well. */
export function Problems({ project }: { project: string }) {
  const problems = useProblems(project);
  if (problems.length === 0) return null;
  return (
    <div className="border-b border-line">
      {problems.map((problem) => (
        <ProblemRow key={problemKey(problem)} problem={problem} />
      ))}
    </div>
  );
}

function problemKey(p: Problem) {
  switch (p.kind) {
    case "server_offline":
      return `server:${p.server.id}`;
    case "staged":
      return `staged:${p.environment.environment.id}`;
    case "domain":
      return `domain:${p.domain.hostname}`;
    default:
      return `${p.kind}:${p.service.service.id}`;
  }
}

const tab = "text-dim hover:text-white";
const mono = "font-mono text-xs";

function ProblemRow({ problem: p }: { problem: Problem }) {
  switch (p.kind) {
    case "crashed": {
      const { status, active_release } = p.service.service;
      const when = status.updated_at ? ` (${since(status.updated_at)})` : "";
      if (status.state !== "restarting" && status.state !== "crashed") {
        return (
          <ServiceRow
            tone="bad"
            service={p.service}
            actions={
              <Link
                to="/$project/$environment/$service"
                params={serviceRef(p.service.service)}
                className={tab}
              >
                Releases
              </Link>
            }
          >
            No running container for r{active_release}
            {when}. Redeploy or roll back.
          </ServiceRow>
        );
      }
      return (
        <ServiceRow
          tone="bad"
          service={p.service}
          actions={
            <>
              <ServiceLink service={p.service} tab="logs" label="Logs" />
              <ServiceLink
                service={p.service}
                tab="variables"
                label="Variables"
              />
            </>
          }
        >
          {status.state === "restarting"
            ? `Restarting: ${status.restart_count} restarts`
            : "Crashed"}
          , exit code <span className={mono}>{status.exit_code}</span>
          {status.oom_killed && `, ${outOfMemory(status)}`}
          {when}.
        </ServiceRow>
      );
    }
    case "deploy_failed":
      return (
        <ServiceRow
          tone="bad"
          service={p.service}
          actions={
            <Link
              to="/$project/$environment/$service/logs"
              params={serviceRef(p.service.service)}
              search={{ release: p.release.number, build: true }}
              className={tab}
            >
              Build log
            </Link>
          }
        >
          r{p.release.number} failed: {p.release.error ?? "unknown error"}
        </ServiceRow>
      );
    case "domain": {
      const record = p.domain.dns_records?.[0];
      return (
        <ServiceRow
          tone={p.domain.status === "error" ? "bad" : "warn"}
          service={p.service}
          actions={
            <ServiceLink service={p.service} tab="domains" label="Domain" />
          }
        >
          {p.domain.status === "error"
            ? "Domain error on "
            : "Waiting for DNS on "}
          <span className={mono}>{p.domain.hostname}</span>
          {p.domain.status_reason && `: ${p.domain.status_reason}`}
          {p.domain.status === "waiting_for_dns" && record && (
            <>
              . Create{" "}
              <span className={mono}>
                {record.type} {record.name} {record.value}
              </span>
            </>
          )}
        </ServiceRow>
      );
    }
    case "deploying":
      return (
        <ServiceRow
          tone="run"
          service={p.service}
          actions={
            <Link
              to="/$project/$environment/$service"
              params={serviceRef(p.service.service)}
              className={tab}
            >
              Follow
            </Link>
          }
        >
          Deploying r{p.release.number}: {releaseStatus(p.release.status).label}
        </ServiceRow>
      );
    case "staged": {
      const { environment, pending_changes: n } = p.environment;
      return (
        <Row
          tone="warn"
          subject={environment.name}
          where="environment"
          actions={
            <Link
              to="/$project/$environment"
              params={{
                project: environment.project,
                environment: environment.name,
              }}
              className={tab}
            >
              Review
            </Link>
          }
        >
          {n} staged {n === 1 ? "change" : "changes"} not applied yet.
        </Row>
      );
    }
    case "server_offline":
      return (
        <Row
          tone="bad"
          subject={p.server.name}
          where="server"
          actions={
            <Link to="/servers" hash={p.server.name} className={tab}>
              Servers
            </Link>
          }
        >
          Offline
          {p.server.last_seen_at &&
            `, last seen ${since(p.server.last_seen_at)} ago`}
          .
        </Row>
      );
  }
}

function ServiceRow({
  service,
  ...props
}: {
  tone: Tone;
  service: ServiceView;
  actions: ReactNode;
  children: ReactNode;
}) {
  return (
    <Row
      subject={service.service.name}
      where={service.service.environment}
      {...props}
    />
  );
}

function Row({
  tone,
  subject,
  where,
  actions,
  children,
}: {
  tone: Tone;
  subject: string;
  where: string;
  actions: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="grid grid-cols-[18px_220px_1fr_auto] items-baseline gap-2.5 border-t border-line px-5 py-[7px] first:border-t-0">
      <Glyph tone={tone} />
      <span>
        <b className="font-semibold">{subject}</b>{" "}
        <span className="text-dim">{where}</span>
      </span>
      <span className="min-w-0">{children}</span>
      <span className="flex gap-3.5">{actions}</span>
    </div>
  );
}

const serviceTabs = {
  logs: "/$project/$environment/$service/logs",
  variables: "/$project/$environment/$service/variables",
  domains: "/$project/$environment/$service/domains",
} as const;

function ServiceLink({
  service,
  tab: name,
  label,
}: {
  service: ServiceView;
  tab: keyof typeof serviceTabs;
  label: string;
}) {
  return (
    <Link
      to={serviceTabs[name]}
      params={serviceRef(service.service)}
      className={tab}
    >
      {label}
    </Link>
  );
}
