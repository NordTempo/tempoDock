import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useApplyChanges } from "../../data/mutations";
import { useProblems } from "../../data/problems";
import {
  serviceRef,
  useChanges,
  useIsOperator,
  useProjectOverview,
} from "../../data/queries";
import type { EnvironmentView, Problem } from "../../data/types";
import { useShortcuts } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { plural, since } from "../../ui/format";
import { Button, ErrorText, Glyph, SectionHead } from "../../ui/kit";
import {
  failurePhrase,
  type GlyphChar,
  healthRestarts,
  isCrashLooping,
  isUnhealthy,
  oomPhrase,
  type Tone,
  toneText,
} from "../../ui/status";
import { AlertLink, alertLabel, alertText } from "../alerts/source";
import { errorSummary } from "../service/releases";
import { affectedServices, nextRelease } from "../variables/apply";
import { hiddenFailures } from "./failed";

const actionClass = "btn sm no-underline";

/**
 * The project's problems, plus failed deploys a rollback or restart
 * hides (failed.ts), next to the other failed deploys.
 */
function useAllProblems(project: string): Problem[] {
  const problems = useProblems(project);
  const services = useProjectOverview(project).environments.flatMap(
    (e) => e.services,
  );
  const hidden = hiddenFailures(services);
  if (hidden.size === 0) return problems;
  const extra = services.flatMap((service): Problem[] => {
    const release = hidden.get(service.service.id);
    return release ? [{ kind: "deploy_failed", service, release }] : [];
  });
  // Crashes stay first; failed deploys come right after them.
  const at = problems.findLastIndex((p) => p.kind === "crashed") + 1;
  return [...problems.slice(0, at), ...extra, ...problems.slice(at)];
}

/** The environment a problem is in; "" for servers and alerts. */
function problemEnvironment(p: Problem): string {
  if ("service" in p) return serviceRef(p.service.service).environment;
  if (p.kind === "staged") return p.environment.environment.name;
  return "";
}

/**
 * The overview's numbered "needs a human" list, worst first. Keys 1 to 9
 * open (or focus) a problem's action.
 */
export function Problems({
  project,
  environment,
}: {
  project: string;
  /** Only this environment's problems, and those of no environment (servers, alerts). */
  environment?: string;
}) {
  const problems = fold(
    useAllProblems(project).filter((p) => {
      const where = problemEnvironment(p);
      return !environment || where === environment || where === "";
    }),
  );
  useShortcuts(
    problems.slice(0, 9).map((_, i) => ({
      key: String(i + 1),
      run: () => {
        const action = document.querySelector<HTMLElement>(
          `[data-problem="${i + 1}"]`,
        );
        if (action instanceof HTMLAnchorElement) action.click();
        else action?.focus();
      },
    })),
  );
  if (problems.length === 0) return null;
  const n = problems.length;
  return (
    <section aria-labelledby="problems">
      <SectionHead id="problems">{plural(n, "problem")}</SectionHead>
      <ol className="panel">
        {problems.map(({ problem, more }, i) => (
          <ProblemRow
            key={problemKey(problem)}
            n={i + 1}
            problem={problem}
            more={more}
            project={project}
          />
        ))}
      </ol>
    </section>
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
    case "alert":
      return `alert:${p.alert.id}`;
    default:
      return `${p.kind}:${p.service.service.id}`;
  }
}

type RowProps = {
  n: number;
  problem: Problem;
  /** How many more alerts like this one were folded into its row. */
  more: number;
  project: string;
};

/**
 * Folds alerts of one kind on one service ("New error" on web, three
 * times) into the first one's row, which says how many more there are.
 */
function fold(problems: Problem[]) {
  const rows: { problem: Problem; more: number }[] = [];
  const seen = new Map<string, { problem: Problem; more: number }>();
  for (const problem of problems) {
    if (problem.kind === "alert") {
      const a = problem.alert;
      const id = `${alertLabel(a)}|${a.environment ?? ""}|${a.service ?? ""}`;
      const row = seen.get(id);
      if (row) {
        row.more += 1;
        continue;
      }
      const fresh = { problem, more: 0 };
      seen.set(id, fresh);
      rows.push(fresh);
    } else {
      rows.push({ problem, more: 0 });
    }
  }
  return rows;
}

function ProblemRow({ n, problem: p, more, project }: RowProps) {
  const org = useOrg();
  const key = n <= 9 ? String(n) : undefined;
  const operator = useIsOperator();
  switch (p.kind) {
    case "crashed": {
      const { status, active_release } = p.service.service;
      const ref = serviceRef(p.service.service);
      const where = `${ref.environment}/${ref.service}`;
      const time = status.updated_at && since(status.updated_at);
      const staged = p.service.service.pending_changes > 0;
      const looping = isCrashLooping(status);
      if (looping || status.state === "crashed") {
        return (
          <Row
            n={key}
            tone="fail"
            glyph={looping ? "↻" : "✕"}
            title={looping ? "Crash loop" : "Crashed"}
            where={where}
            time={time}
            action={
              staged ? (
                <ApplyStaged
                  n={n}
                  project={project}
                  environment={ref.environment}
                />
              ) : (
                <Link
                  to="/$org/$project/$environment/$service/logs"
                  params={{ org, ...ref }}
                  className={actionClass}
                  data-problem={n}
                >
                  Logs
                </Link>
              )
            }
          >
            <span className="font-mono">r{active_release}</span>{" "}
            {looping
              ? `restarted ${status.restart_count} times, exit ${status.exit_code}${healthRestarts(status)}`
              : `exited with ${status.exit_code}`}
            {status.oom_killed && `, ${oomPhrase(status)}`}.{" "}
            {staged && (
              <span className="text-mut">Changes for it are staged.</span>
            )}
          </Row>
        );
      }
      if (isUnhealthy(status)) {
        return (
          <Row
            n={key}
            tone="fail"
            glyph="✕"
            title="Unhealthy"
            where={where}
            time={time}
            action={
              <Link
                to="/$org/$project/$environment/$service/logs"
                params={{ org, ...ref }}
                className={actionClass}
                data-problem={n}
              >
                Logs
              </Link>
            }
          >
            <span className="font-mono">r{active_release}</span> runs but fails
            its health check{healthRestarts(status)}.{" "}
            <span className="text-mut">Dock restarts it, with backoff.</span>
          </Row>
        );
      }
      return (
        <Row
          n={key}
          tone="fail"
          glyph="✕"
          title="Not running"
          where={where}
          time={time}
          action={
            <Link
              to="/$org/$project/$environment/$service"
              hash="releases"
              params={{ org, ...ref }}
              className={actionClass}
              data-problem={n}
            >
              Releases
            </Link>
          }
        >
          <span className="font-mono">r{active_release}</span> has no running
          container. Redeploy or roll back.
        </Row>
      );
    }
    case "deploy_failed": {
      const ref = serviceRef(p.service.service);
      const serving = p.service.service.active_release;
      return (
        <Row
          n={key}
          tone="fail"
          glyph="✕"
          title="Deploy failed"
          where={`${ref.environment}/${ref.service}`}
          time={since(p.release.created_at)}
          action={
            <Link
              to="/$org/$project/$environment/$service/releases/$number"
              params={{ org, ...ref, number: String(p.release.number) }}
              className={actionClass}
              data-problem={n}
            >
              See why
            </Link>
          }
        >
          <span className="font-mono">r{p.release.number}</span>{" "}
          {failurePhrase(p.release)}:{" "}
          <span className="font-mono">{errorSummary(p.release)}</span>.{" "}
          {serving && (
            <span className="text-ok">r{serving} still serving.</span>
          )}
        </Row>
      );
    }
    case "backup_failed": {
      const ref = serviceRef(p.service.service);
      const at = p.backup.finished_at ?? p.backup.started_at;
      // Trailing punctuation would clash with the sentence's own period.
      const error = p.backup.error?.replace(/[\s:.]+$/, "");
      return (
        <Row
          n={key}
          tone="fail"
          glyph="✕"
          title="Backup failed"
          where={`${ref.environment}/${ref.service}`}
          time={since(at)}
          action={
            <Link
              to="/$org/$project/$environment/$service/backups"
              params={{ org, ...ref }}
              className={actionClass}
              data-problem={n}
            >
              Backups
            </Link>
          }
        >
          The latest backup failed
          {error && (
            <>
              : <span className="font-mono">{error}</span>
            </>
          )}
          .
        </Row>
      );
    }
    case "domain": {
      const ref = serviceRef(p.service.service);
      const record = p.domain.dns_records?.[0];
      const error = p.domain.status === "error";
      return (
        <Row
          n={key}
          tone={error ? "fail" : "warn"}
          glyph={error ? "✕" : "▲"}
          title={error ? "Domain error" : "Waiting for DNS"}
          where={`${ref.environment}/${ref.service}`}
          time={since(p.domain.created_at)}
          action={
            <Link
              to="/$org/$project/$environment/$service/domains"
              params={{ org, ...ref }}
              className={actionClass}
              data-problem={n}
            >
              {error ? "Domains" : "Show record"}
            </Link>
          }
        >
          <span className="font-mono">{p.domain.hostname}</span>{" "}
          {!error && record ? (
            <>
              needs {record.type === "A" ? "an A record" : "a CNAME"} to{" "}
              <span className="font-mono">{record.value}</span>
            </>
          ) : (
            (p.domain.status_reason ?? "")
          )}
        </Row>
      );
    }
    case "staged":
      return <StagedRow n={n} environment={p.environment} project={project} />;
    case "alert": {
      const a = p.alert;
      const critical = a.severity === "critical";
      const { text, cli } = alertText(a);
      return (
        <Row
          n={key}
          tone={critical ? "fail" : "warn"}
          glyph={critical ? "✕" : "▲"}
          title={alertLabel(a)}
          where={
            a.environment && a.service
              ? `${a.environment}/${a.service}`
              : (a.environment ?? a.server ?? "")
          }
          time={since(a.opened_at)}
          action={
            <AlertLink
              alert={a}
              className={actionClass}
              title={cli}
              data-problem={n}
            />
          }
        >
          {a.title}.{text && <span className="text-mut"> {text}</span>}
          {more > 0 && (
            <span className="text-warn-text"> And {more} more like it.</span>
          )}
        </Row>
      );
    }
    case "server_offline":
      return (
        <Row
          n={key}
          tone="fail"
          glyph="✕"
          title="Server offline"
          where={p.server.name}
          time={p.server.last_seen_at && since(p.server.last_seen_at)}
          action={
            operator && (
              <Link
                to="/console/servers/$server"
                params={{ server: p.server.name }}
                className={actionClass}
                data-problem={n}
              >
                Server
              </Link>
            )
          }
        >
          Not reachable
          {p.server.last_seen_at &&
            `, last seen ${since(p.server.last_seen_at)} ago`}
          . Its services keep running but can't be deployed.
        </Row>
      );
  }
}

function StagedRow({
  n,
  environment: env,
  project,
}: {
  n: number;
  environment: EnvironmentView;
  project: string;
}) {
  const org = useOrg();
  const ref = { project, environment: env.environment.name };
  const changes = useChanges(ref);
  const releases = affectedServices(changes, env.services).map(nextRelease);
  return (
    <Row
      n={n <= 9 ? String(n) : undefined}
      tone="warn"
      glyph="▲"
      title="Not applied"
      where={env.environment.name}
      action={
        <Link
          to="/$org/$project/settings/variables/$environment"
          params={{ org, ...ref }}
          className={actionClass}
          data-problem={n}
        >
          Review
        </Link>
      }
    >
      {plural(changes.count, "change")} waiting to apply.{" "}
      {releases.length > 0 ? (
        <>
          Applying creates{" "}
          {releases.map((r, i) => (
            <span key={r}>
              {i > 0 && ", "}
              <span className="font-mono">{r}</span>
            </span>
          ))}
          .
        </>
      ) : (
        "Nothing running is redeployed."
      )}
      {changes.last_failure && (
        <span className="text-fail">
          {" "}
          The last apply failed and was re-staged: {changes.last_failure}
        </span>
      )}
    </Row>
  );
}

/** Applies an environment's staged changes from a problem row. */
function ApplyStaged({
  n,
  project,
  environment,
}: {
  n: number;
  project: string;
  environment: string;
}) {
  const apply = useApplyChanges();
  return (
    <span className="flex items-center gap-2">
      <ErrorText error={apply.error} />
      <Button
        primary
        small
        data-problem={n}
        disabled={apply.isPending}
        onClick={() => apply.mutate({ project, environment })}
      >
        Apply changes
      </Button>
    </span>
  );
}

function Row({
  n,
  tone,
  glyph,
  title,
  where,
  time,
  action,
  children,
}: {
  n: string | undefined;
  tone: Tone;
  glyph: GlyphChar;
  title: string;
  where: string;
  time?: string | false;
  action: ReactNode;
  children: ReactNode;
}) {
  return (
    <li className="flex flex-wrap items-center gap-x-4 gap-y-1 border-b border-soft px-3.5 py-2.5 last:border-0">
      <span className="flex w-[172px] items-baseline gap-2.5 whitespace-nowrap">
        {n ? <kbd>{n}</kbd> : <span className="w-[15px]" />}
        <span className={toneText[tone]}>
          <Glyph char={glyph} /> {title}
        </span>
      </span>
      <span className="w-[150px] font-mono">{where}</span>
      <span className="min-w-0 flex-[1_1_360px]">{children}</span>
      {time && <span className="text-xs text-faint">{time}</span>}
      {action}
    </li>
  );
}
