import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { usePruneBuildCache } from "../../data/mutations";
import type { Problem } from "../../data/types";
import { since } from "../../ui/format";
import { Action, ErrorText, Glyph } from "../../ui/kit";
import { diskFraction, type Tone } from "../../ui/status";

/** The overview's "needs a human" list. Renders nothing when all is well. */
export function Problems({ problems }: { problems: Problem[] }) {
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
  return p.kind === "disk"
    ? `disk:${p.server.id}`
    : `${p.kind}:${p.service.id}`;
}

const tab = "text-dim hover:text-white";
const mono = "font-mono text-xs";

function ProblemRow({ problem: p }: { problem: Problem }) {
  switch (p.kind) {
    case "crash_loop": {
      const [code, ...rest] = p.exit.split(": ");
      return (
        <Row
          tone="bad"
          subject={p.service.name}
          where={p.service.environmentName}
          actions={
            <>
              <ServiceLink id={p.service.id} tab="logs" label="Logs" />
              <ServiceLink
                id={p.service.id}
                tab="variables"
                label="Variables"
              />
            </>
          }
        >
          Crash loop: {p.restarts} restarts in {p.window}.{" "}
          {rest.length ? (
            <>
              {code}: <span className={mono}>{rest.join(": ")}</span>
            </>
          ) : (
            <span className={mono}>{p.exit}</span>
          )}
        </Row>
      );
    }
    case "deploy_failed":
      return (
        <Row
          tone="bad"
          subject={p.service.name}
          where={p.service.environmentName}
          actions={<ServiceLink id={p.service.id} tab="logs" label="Logs" />}
        >
          r{p.release} failed: {p.reason}
        </Row>
      );
    case "dns_waiting":
      return (
        <Row
          tone="warn"
          subject={p.service.name}
          where={p.service.environmentName}
          actions={
            <ServiceLink id={p.service.id} tab="domains" label="Domain" />
          }
        >
          Waiting for DNS on <span className={mono}>{p.domain.hostname}</span>
          {p.domain.status.state === "waiting_dns" &&
            ` for ${since(p.domain.status.since)}`}
          . Create{" "}
          <span className={mono}>
            {p.record.type} {p.record.name} {p.record.value}
          </span>
        </Row>
      );
    case "deploying":
      return (
        <Row
          tone="run"
          subject={p.service.name}
          where={p.service.environmentName}
          actions={
            <Link
              to="/services/$serviceId"
              params={{ serviceId: p.service.id }}
              className={tab}
            >
              Follow
            </Link>
          }
        >
          Deploying r{p.release}: {p.step}
        </Row>
      );
    case "disk":
      return <DiskRow problem={p} />;
  }
}

function DiskRow({
  problem: p,
}: {
  problem: Extract<Problem, { kind: "disk" }>;
}) {
  const prune = usePruneBuildCache();
  const { disk } = p.server;
  return (
    <Row
      tone="warn"
      subject={p.server.name}
      where="server"
      actions={
        <>
          <ErrorText error={prune.error} />
          {disk.prunableGb > 0 && (
            <Action
              disabled={prune.isPending}
              onClick={() => prune.mutate(p.server.id)}
            >
              Prune
            </Action>
          )}
        </>
      }
    >
      Disk {Math.round(diskFraction(p.server) * 100)}% ({disk.usedGb} /{" "}
      {disk.totalGb} GB).
      {disk.prunableGb > 0 && ` Build cache ${disk.prunableGb} GB, prunable.`}
    </Row>
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
  logs: "/services/$serviceId/logs",
  variables: "/services/$serviceId/variables",
  domains: "/services/$serviceId/domains",
} as const;

function ServiceLink({
  id,
  tab: name,
  label,
}: {
  id: string;
  tab: keyof typeof serviceTabs;
  label: string;
}) {
  return (
    <Link to={serviceTabs[name]} params={{ serviceId: id }} className={tab}>
      {label}
    </Link>
  );
}
