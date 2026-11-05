import { useQueries } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { isFailing } from "../data/problems";
import { queries, useProjects, useServers, useSession } from "../data/queries";
import type { ProjectOverview } from "../data/types";
import { canCreateProject } from "../features/create/access";
import { openCreate } from "../features/create/open";
import { useOrg } from "../shell/location";
import { plural, since } from "../ui/format";
import {
  Button,
  CopyButton,
  Glyph,
  LockMark,
  Page,
  PageBar,
  Panel,
} from "../ui/kit";

// An organization's home ("/" lands here too): its projects with their
// health, worst first. With no projects yet, it offers to create one here
// or says how to deploy from a terminal.
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
        title="Projects"
        after={
          <span className="text-xs text-faint">
            {plural(projects.length, "project")}
          </span>
        }
        actions={
          canCreate && (
            <Button primary onClick={() => openCreate({ kind: "project" })}>
              New project
            </Button>
          )
        }
      />
      <Page>
        <Panel>
          <table className="tabular-nums">
            <thead>
              <tr>
                <th>Project</th>
                <th>Health</th>
                <th className="text-right">Running</th>
                <th>Environments</th>
                <th>Last deploy</th>
              </tr>
            </thead>
            <tbody>
              {rows.map(({ project, health: h }) => (
                <tr key={project.id} className="hover:bg-hover">
                  <td className="w-[22%]">
                    <Link
                      to="/$org/$project"
                      params={{ org, project: project.name }}
                      className="font-medium hover:underline"
                    >
                      {project.name}
                    </Link>
                  </td>
                  <td className="w-[24%]">
                    {h ? (
                      <span className={h.tone}>
                        <Glyph char={h.glyph} /> {h.label}
                      </span>
                    ) : (
                      <span className="text-faint">not loaded</span>
                    )}
                  </td>
                  <td className="w-[10%] text-right font-mono">
                    {h ? `${h.running}/${h.total}` : "·"}
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
                        {since(h.last.at)} ago
                      </span>
                    ) : (
                      <span className="text-faint">·</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Panel>
      </Page>
    </>
  );
}

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
  const failedDeploys = services.filter(
    (s) => !s.deploying && s.latest_release?.status === "failed",
  ).length;
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
      label: `${plural(failing, "service")} failing`,
    };
  if (failedDeploys > 0)
    return {
      ...base,
      rank: 2,
      tone: "text-fail",
      glyph: "✕" as const,
      label: `${plural(failedDeploys, "deploy")} failed`,
    };
  if (deploying > 0)
    return {
      ...base,
      rank: 1,
      tone: "text-prog",
      glyph: "◐" as const,
      label: "deploying",
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
  ] as const;
  const note = idle
    .filter(([n]) => n > 0)
    .map(([n, what]) => `${n} ${what}`)
    .join(", ");
  if (services.length === 0 || running === 0)
    return {
      ...base,
      rank: 0,
      tone: "text-mut",
      glyph: "○" as const,
      label: note || "no services",
    };
  return {
    ...base,
    rank: 0,
    tone: "text-ok",
    glyph: "●" as const,
    label: note ? `running, ${note}` : "all running",
  };
}

function NoProjects() {
  const org = useOrg();
  const server = useServers().find((s) => s.local)?.name;
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
    <Page width="max-w-[720px]" gap="gap-5">
      <h1 className="pt-10 text-xl font-semibold">No projects yet</h1>
      {canCreate && (
        <p className="flex flex-wrap items-center gap-3">
          <Button primary onClick={() => openCreate({ kind: "project" })}>
            New project
          </Button>
          <span className="text-mut">
            then add services from an image, a GitHub repository or a template.
          </span>
        </p>
      )}
      <p className="text-md text-mut">
        {canCreate ? "Or deploy" : "Deploy"} your first app from its folder.
        {server && (
          <>
            {" "}
            It runs on <span className="font-mono text-fg">{server}</span>, the
            server you connected.
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
  );
}
