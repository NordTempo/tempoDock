import { useQueries, useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { isFailing } from "../data/problems";
import {
  queries,
  useProjects,
  useServersIfOperator,
  useSession,
} from "../data/queries";
import type { ProjectOverview } from "../data/types";
import { useOrgOpenAlerts } from "../features/alerts/data";
import { AlertLink } from "../features/alerts/source";
import { AuditTable } from "../features/audit/AuditTable";
import { canCreateProject } from "../features/create/access";
import { openCreate } from "../features/create/open";
import { useOrg } from "../shell/location";
import { ago, plural } from "../ui/format";
import { Icon } from "../ui/icons";
import {
  Button,
  CopyButton,
  Glyph,
  LockMark,
  Page,
  PageBar,
  Panel,
  SectionHead,
} from "../ui/kit";

// An organization's home ("/" lands here too), its Overview in the top
// bar (option 3): what needs someone (open alerts, the Inbox's), its
// projects with their health worst first, and the latest deploys. With
// no projects yet, it offers to create one or says how to deploy from a
// terminal.
export const Route = createFileRoute("/_app/$org/")({
  loader: async ({ context: { queryClient } }) => {
    const projects = await queryClient.ensureQueryData(queries.projects());
    // One project failing to load leaves its row without numbers.
    await Promise.allSettled(
      projects.map((p) =>
        queryClient.ensureQueryData(queries.overview(p.name)),
      ),
    );
  },
  component: Home,
});

function Home() {
  const org = useOrg();
  const projects = useProjects();
  const canCreate = canCreateProject(useSession());
  const overviews = useQueries({
    queries: projects.map((p) => queries.overview(p.name)),
  });
  if (projects.length === 0) return <NoProjects />;
  const rows = projects
    .map((project, i) => ({ project, health: health(overviews[i]?.data) }))
    .sort((a, b) => (b.health?.rank ?? 0) - (a.health?.rank ?? 0));
  return (
    <>
      <PageBar
        title="Overview"
        actions={
          canCreate && (
            <Button primary onClick={() => openCreate({ kind: "project" })}>
              New project
            </Button>
          )
        }
      />
      <Page>
        <NeedsYou />
        <section aria-labelledby="projects-head">
          <SectionHead id="projects-head">Projects</SectionHead>
          <Panel>
            <table className="tabular-nums">
              <thead>
                <tr>
                  <th>Project</th>
                  <th>Health</th>
                  <th className="text-right">Running</th>
                  <th>Environments</th>
                  <th>Last deploy</th>
                  <th>
                    <span className="sr-only">Open</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map(({ project, health: h }) => (
                  <tr key={project.id} className="row-link">
                    <td className="w-[22%]">
                      <Link
                        to="/$org/$project"
                        params={{ org, project: project.name }}
                        className="row-main font-medium hover:underline"
                      >
                        {project.name}
                      </Link>
                    </td>
                    <td className="w-[24%]">
                      {h ? (
                        <span className={h.tone}>
                          <Glyph char={h.glyph} />{" "}
                          {h.parts.map(([text, tone], i) => (
                            <span key={text} className={tone}>
                              {i > 0 && ", "}
                              {text}
                            </span>
                          ))}
                        </span>
                      ) : (
                        <span className="text-faint">not loaded</span>
                      )}
                    </td>
                    <td className="w-[10%] text-right font-mono">
                      {h ? `${h.running} of ${h.total}` : "·"}
                    </td>
                    <td className="w-[24%] truncate">
                      {h?.environments.map((e, i) => (
                        <span key={e.id}>
                          {i > 0 && ", "}
                          {e.name}
                          {e.lock && (
                            <>
                              {" "}
                              <LockMark lock={e.lock} />
                            </>
                          )}
                        </span>
                      )) ?? <span className="text-faint">·</span>}
                    </td>
                    <td className="text-mut">
                      {h?.last ? (
                        <span title={h.last.at}>
                          <span className="font-mono text-sec">
                            {h.last.service} r{h.last.number}
                          </span>{" "}
                          {h.last.failed && (
                            <span className="text-fail">failed </span>
                          )}
                          {ago(h.last.at)}
                        </span>
                      ) : (
                        <span className="text-faint">never</span>
                      )}
                    </td>
                    <td className="row-go" aria-hidden>
                      <Icon name="chevronRight" size={14} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Panel>
        </section>
        <RecentDeploys />
      </Page>
    </>
  );
}

/** A piece of a project's health and the colour of its state. */
type Part = [text: string, tone: string];

/**
 * A project's row: its worst state (failing services, a failed latest
 * deploy, a deploy running), how many services run, its environments and
 * its latest release. rank orders rows, worst first.
 */
function health(o: ProjectOverview | undefined) {
  if (!o) return undefined;
  const environments = (o.environments ?? []).map((e) => e.environment);
  const services = (o.environments ?? []).flatMap((e) => e.services ?? []);
  const failing = services.filter(isFailing).length;
  // A failed deploy counts while it is the news: the latest release, or
  // the one a rollback answered (the overview's rule).
  const failedDeploys = services.filter((s) => {
    if (s.deploying) return false;
    if (s.latest_release?.status === "failed") return true;
    const f = s.last_failed_deploy;
    return !!f && (s.service.active_release ?? 0) <= f.number + 1;
  }).length;
  const deploying = services.filter((s) => s.deploying).length;
  const running = services.filter(
    (s) => s.service.status.state === "running",
  ).length;
  let last: {
    service: string;
    number: number;
    at: string;
    failed: boolean;
  } | null = null;
  for (const s of services) {
    const r = s.deploying ?? s.latest_release;
    if (r && (!last || r.created_at > last.at)) {
      last = {
        service: s.service.name,
        number: r.number,
        at: r.created_at,
        failed: r.status === "failed",
      };
    }
  }
  const base = { environments, running, total: services.length, last };
  if (failing > 0)
    return {
      ...base,
      rank: 3,
      tone: "text-fail",
      glyph: "✕" as const,
      parts: [[`${plural(failing, "service")} failing`, "text-fail"]],
    };
  if (failedDeploys > 0)
    return {
      ...base,
      rank: 2,
      tone: "text-fail",
      glyph: "✕" as const,
      parts: [[`${plural(failedDeploys, "deploy")} failed`, "text-fail"]],
    };
  if (deploying > 0)
    return {
      ...base,
      rank: 1,
      tone: "text-prog",
      glyph: "◐" as const,
      parts: [["deploying", "text-prog"]],
    };
  // Nothing broken: say what isn't running, if anything.
  const idle = [
    [
      services.filter((s) => !s.service.active_release && !s.deploying).length,
      "not deployed",
    ],
    [
      services.filter(
        (s) => s.service.active_release && s.service.status.state === "stopped",
      ).length,
      "stopped",
    ],
    [
      services.filter((s) => s.service.status.state === "asleep").length,
      "asleep",
    ],
  ] as const;
  // Each part in its own state's colour: green covers only what runs.
  const note: Part[] = idle
    .filter(([n]) => n > 0)
    .map(([n, what]) => [`${n} ${what}`, "text-mut"]);
  if (services.length === 0 || running === 0)
    return {
      ...base,
      rank: 0,
      tone: "text-mut",
      glyph: "○" as const,
      parts: note.length > 0 ? note : [["no services", "text-mut"] as Part],
    };
  return {
    ...base,
    rank: 0,
    tone: "text-ok",
    glyph: "●" as const,
    parts: [
      [note.length > 0 ? `${running} running` : "all running", "text-ok"],
      ...note,
    ] as Part[],
  };
}

function NoProjects() {
  const org = useOrg();
  // Only operators can list servers, so only they see where it runs.
  const server = useServersIfOperator().find((s) => s.local)?.name;
  const canCreate = canCreateProject(useSession());
  const steps = [
    [`dock login ${window.location.origin}`, "Sign in from your terminal."],
    [
      "dock project create my-app",
      "Creates the project with a production environment.",
    ],
    [
      "dock link -p my-app",
      "Run it in your app's folder, so later commands need no flags.",
    ],
    [
      "dock deploy -s web",
      "Builds the folder, starts and health checks it. The service is created on first deploy.",
    ],
  ];
  return (
    <>
      <PageBar
        title="Overview"
        actions={
          canCreate && (
            <Button primary onClick={() => openCreate({ kind: "project" })}>
              New project
            </Button>
          )
        }
      />
      <Page width="max-w-[720px]">
        {canCreate && (
          <p className="text-mut">
            Create a project, then add services from an image, a GitHub
            repository or a template.
          </p>
        )}
        <p className="text-mut">
          {canCreate ? "Or deploy" : "Deploy"} your first app from its folder.
          {server && (
            <>
              {" "}
              It runs on <span className="font-mono text-fg">{server}</span>,
              the server you connected.
            </>
          )}
        </p>
        <ol className="border-t border-line">
          {steps.map(([command = "", note], i) => (
            <li
              key={command}
              className="grid grid-cols-[28px_minmax(0,1fr)_auto] items-baseline gap-x-3.5 gap-y-1 border-b border-soft py-3.5"
            >
              <span className="font-mono text-mut">{i + 1}</span>
              <code className="font-mono">{command}</code>
              <CopyButton text={command} />
              <span />
              <span className="col-span-2 text-mut">{note}</span>
            </li>
          ))}
        </ol>
        <p className="text-mut">
          This page lists the project once it appears. Letting an agent do it?{" "}
          <Link to="/$org/settings/tokens" params={{ org }}>
            Create a deploy token
          </Link>
          .
        </p>
      </Page>
    </>
  );
}

/** Open alerts, worst first, the newest six; the rest are in the Inbox. */
function NeedsYou() {
  const org = useOrg();
  const open = useOrgOpenAlerts();
  return (
    <section aria-labelledby="needs-head">
      <SectionHead
        id="needs-head"
        aside={open.length > 0 && `${open.length} open`}
        right={
          <Link to="/$org/inbox" params={{ org }} className="hover:text-fg">
            Inbox →
          </Link>
        }
      >
        Needs you
      </SectionHead>
      {open.length === 0 ? (
        <p className="text-mut">Nothing waits on anyone.</p>
      ) : (
        <ul className="border-t border-line">
          {open.slice(0, 6).map((a) => (
            <li
              key={a.id}
              className="flex flex-wrap items-baseline gap-x-3 border-b border-soft py-2"
            >
              <span
                className={
                  a.severity === "critical" ? "text-fail" : "text-warn"
                }
              >
                {a.severity === "critical" ? "✕" : "▲"}
              </span>
              <span className="min-w-0 flex-1">{a.title}</span>
              <span className="text-xs text-mut">{ago(a.opened_at)}</span>
              <AlertLink alert={a} className="btn sm no-underline" />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/** The latest deploys, rollbacks and restarts across the organization. */
function RecentDeploys() {
  const org = useOrg();
  const events = useQuery(queries.audit({ action: "release.", limit: 8 })).data;
  if (!events?.length) return null;
  return (
    <section aria-labelledby="deploys-head">
      <SectionHead
        id="deploys-head"
        right={
          <Link to="/$org/audit" params={{ org }} className="hover:text-fg">
            All activity →
          </Link>
        }
      >
        Recent deploys
      </SectionHead>
      <AuditTable events={events} compact />
    </section>
  );
}
