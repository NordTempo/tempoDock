import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useApplyChanges } from "../../data/mutations";
import { useProblems } from "../../data/problems";
import { serviceRef, useChanges, useProjectOverview } from "../../data/queries";
import type { EnvironmentView, Problem } from "../../data/types";
import { useShortcuts } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { plural, since } from "../../ui/format";
import { Button, ErrorText, Glyph } from "../../ui/kit";
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

/**
 * The environment a problem is in; "" for servers and for alerts about
 * no one environment.
 */
export function problemEnvironment(p: Problem): string {
  if ("service" in p) return serviceRef(p.service.service).environment;
  if (p.kind === "staged") return p.environment.environment.name;
  if (p.kind === "alert") return p.alert.environment ?? "";
  return "";
}

/**
 * The worst tone of each environment's problems, for the marks on the
 * project page's environment switch: a problem in staging shows while
 * production is open.
 */
export function useProblemTones(project: string): Map<string, Tone> {
  const tones = new Map<string, Tone>();
  for (const p of useAllProblems(project)) {
    const where = problemEnvironment(p);
    if (!where) continue;
    const tone: Tone =
      p.kind === "staged" ||
      (p.kind === "alert" && p.alert.severity !== "critical") ||
      (p.kind === "domain" && p.domain.status !== "error")
        ? "warn"
        : "fail";
    if (tones.get(where) !== "fail") tones.set(where, tone);
  }
  return tones;
}

/**
 * What needs a human in one environment, worst first: a line each, what
 * is wrong and the one action that helps. Problems of no one environment
 * (servers, org-wide alerts) show in every environment. Keys 1 to 9 open
 * (or focus) a problem's action.
 */
export function Problems({
  project,
  environment,
}: {
  project: string;
  environment: string;
}) {
  const problems = fold(
    useAllProblems(project).filter((p) => {
      const where = problemEnvironment(p);
      return where === environment || where === "";
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
  return (
    // One grid for every row (each row a subgrid), so the times and
    // actions line up whatever the width of each.
    <ol
      aria-label={`Problems in ${environment}`}
      className="grid grid-cols-[auto_minmax(0,1fr)_auto_auto] gap-x-3 border-t border-line"
    >
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
  );
}

function problemKey(p: Problem) {
  switch (p.kind) {
    case "server_offline":
      return `server:${p.environment.id}`;
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
  /** Its place in the list: keys 1 to 9 reach its action. */
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

/**
 * One problem as a sentence that starts with what it's about ("web r8
 * failed its health check"), its age and its one action.
 */
function ProblemRow({ n, problem: p, more, project }: RowProps) {
  const org = useOrg();
  switch (p.kind) {
    case "crashed": {
      const { status, active_release } = p.service.service;
      const ref = serviceRef(p.service.service);
      const time = status.updated_at && since(status.updated_at);
      const staged = p.service.service.pending_changes > 0;
      const looping = isCrashLooping(status);
      const logs = (
        <Link
          to="/$org/$project/$environment/$service/logs"
          params={{ org, ...ref }}
          className={actionClass}
          data-problem={n}
        >
          Logs
        </Link>
      );
      if (looping || status.state === "crashed") {
        return (
          <Row
            tone="fail"
            glyph={looping ? "↻" : "✕"}
            who={ref.service}
            time={time}
            action={
              staged ? (
                <ApplyStaged
                  n={n}
                  project={project}
                  environment={ref.environment}
                />
              ) : (
                logs
              )
            }
          >
            <span className="font-mono">r{active_release}</span>{" "}
            {looping
              ? `is in a crash loop: restarted ${status.restart_count} times, exit ${status.exit_code}${healthRestarts(status)}`
              : `crashed, exit ${status.exit_code}`}
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
            tone="fail"
            glyph="✕"
            who={ref.service}
            time={time}
            action={logs}
          >
            <span className="font-mono">r{active_release}</span> runs but fails
            its health check{healthRestarts(status)}.{" "}
            <span className="text-mut">Dock restarts it, with backoff.</span>
          </Row>
        );
      }
      return (
        <Row
          tone="fail"
          glyph="✕"
          who={ref.service}
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
          tone="fail"
          glyph="✕"
          who={ref.service}
          time={since(p.release.created_at)}
          action={
            <Link
              to="/$org/$project/$environment/$service/history/$number"
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
            <span className="text-ok">r{serving} is still serving.</span>
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
          tone="fail"
          glyph="✕"
          who={ref.service}
          time={since(at)}
          action={
            <Link
              to="/$org/$project/$environment/$service/settings"
              hash="backups"
              params={{ org, ...ref }}
              className={actionClass}
              data-problem={n}
            >
              See why
            </Link>
          }
        >
          its latest backup failed
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
          tone={error ? "fail" : "warn"}
          glyph={error ? "✕" : "▲"}
          who={p.domain.hostname}
          time={since(p.domain.created_at)}
          action={
            <Link
              to="/$org/$project/$environment/$service/settings"
              hash="domains"
              params={{ org, ...ref }}
              className={actionClass}
              data-problem={n}
            >
              {error ? "Domains" : "Show record"}
            </Link>
          }
        >
          {!error && record ? (
            <>
              waits for DNS: it needs{" "}
              {record.type === "A" ? "an A record" : "a CNAME"} to{" "}
              <span className="font-mono">{record.value}</span>.
            </>
          ) : (
            <>
              has an error
              {p.domain.status_reason && `: ${p.domain.status_reason}`}.
            </>
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
          tone={critical ? "fail" : "warn"}
          glyph={critical ? "✕" : "▲"}
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
          tone="fail"
          glyph="✕"
          who={p.environment.server ? `Server ${p.environment.server}` : ""}
          action={
            p.environment.server && (
              <Link
                to="/console/servers/$server"
                params={{ server: p.environment.server }}
                className={actionClass}
                data-problem={n}
              >
                Server
              </Link>
            )
          }
        >
          {p.environment.server ? "is" : "Its server is"} not reachable. Its
          services keep running but can't be deployed.
        </Row>
      );
  }
}

/** Changes saved in an environment and not applied yet, with Review. */
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
      tone="warn"
      glyph="▲"
      action={
        <Link
          to="/$org/$project/settings"
          params={{ org, project }}
          search={{ env: ref.environment }}
          hash="variables"
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

/**
 * A problem's line: a coloured mark, the sentence (led by `who`, the
 * service, domain or server it is about), its age and its action.
 */
function Row({
  tone,
  glyph,
  who,
  time,
  action,
  children,
}: {
  tone: Tone;
  glyph: GlyphChar;
  who?: string;
  time?: string | false;
  action: ReactNode;
  children: ReactNode;
}) {
  return (
    <li className="col-span-full grid grid-cols-subgrid items-center border-b border-line py-2">
      <span className={`pl-0.5 ${toneText[tone]}`}>
        <Glyph char={glyph} />
      </span>
      <span className="min-w-0">
        {who && <span className="font-semibold">{who}</span>} {children}
      </span>
      <span className="text-right text-xs whitespace-nowrap text-faint">
        {time}
      </span>
      <span className="justify-self-end">{action}</span>
    </li>
  );
}
