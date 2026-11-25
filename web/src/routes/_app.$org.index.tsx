import { useQueries, useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { isFailing } from "../data/problems";
import {
  queries,
  useProjects,
  useServersIfOperator,
  useSession,
} from "../data/queries";
import type { AuditEvent, ProjectOverview } from "../data/types";
import { AlertTable } from "../features/alerts/AlertTable";
import { useOrgOpenAlerts } from "../features/alerts/data";
import { groupAlerts } from "../features/alerts/summary";
import { pastLabel } from "../features/audit/AuditTable";
import { canCreateProject } from "../features/create/access";
import { openCreate } from "../features/create/open";
import { ProjectGrid } from "../features/overview/ProjectGrid";
import { stages, step } from "../features/overview/pipeline";
import { useOrg } from "../shell/location";
import { cliInstallLine } from "../ui/cliInstall";
import { ago, plural } from "../ui/format";
import { Icon } from "../ui/icons";
import {
  Button,
  CopyButton,
  Glyph,
  Page,
  PageBar,
  Panel,
  SectionHead,
} from "../ui/kit";

// An organization's home ("/" lands here too), Projects in the top bar
// (ADR 0099): what needs someone first, the first few with the button to
// each fix (the Inbox has them all), then its projects. A project with
// something going on shows as a pipeline, its services by environment
// with what lies between them (ProjectGrid); the quiet ones are a row
// each, worst first. Then the latest deploys. Errors and status pages
// have their own views in the top bar, so they aren't repeated here. With
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
  const all = projects
    .map((project, i) => {
      const overview = overviews[i]?.data;
      const h = health(overview);
      return { project, overview, health: h, busy: busy(overview, h) };
    })
    .sort((a, b) => (b.health?.rank ?? 0) - (a.health?.rank ?? 0));
  const grids = all.filter((r) => r.busy);
  const rows = all.filter((r) => !r.busy);
  return (
    <>
      <PageBar
        title="Projects"
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
        {grids.map(
          ({ project, overview }) =>
            overview && <ProjectGrid key={project.id} overview={overview} />,
        )}
        {rows.length > 0 && (
          <section aria-labelledby="projects-head">
            <SectionHead id="projects-head">
              {grids.length > 0 ? "Other projects" : "All projects"}
            </SectionHead>
            <Panel>
              <table>
                <thead className="sr-only">
                  <tr>
                    <th>Project</th>
                    <th>Health</th>
                    <th>Last deploy</th>
                    <th>Open</th>
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
                      <td className="whitespace-normal">
                        {h ? (
                          <span className={h.tone}>
                            <Glyph char={h.glyph} /> {h.text}
                            {h.note && (
                              <span className="text-mut">{h.note}</span>
                            )}
                          </span>
                        ) : (
                          <span className="text-faint">not loaded</span>
                        )}
                      </td>
                      <td className="w-[18%] text-right text-mut">
                        {h?.deploying ? (
                          <span className="text-prog">deploying</span>
                        ) : h?.last ? (
                          <span title={new Date(h.last).toLocaleString()}>
                            deployed {ago(h.last)}
                          </span>
                        ) : (
                          <span className="text-faint">never deployed</span>
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
        )}
        <RecentDeploys />
      </Page>
    </>
  );
}

/**
 * A project's health in one phrase, its worst state first: a failing
 * service or a failed deploy by name when there is one ("web r8 failed,
 * r9 still serving"), else a count; a deploy running; else what runs and
 * what doesn't. `last` is when its newest release was made. rank orders
 * the rows, worst first.
 */
function health(o: ProjectOverview | undefined) {
  if (!o) return undefined;
  const services = (o.environments ?? []).flatMap((e) => e.services ?? []);
  const failing = services.filter(isFailing);
  // A failed deploy counts while it is the news: the latest release, or
  // the one a rollback answered (the overview's rule).
  const failedDeploys = services.flatMap((s) => {
    if (s.deploying) return [];
    if (s.latest_release?.status === "failed")
      return [{ s, number: s.latest_release.number }];
    const f = s.last_failed_deploy;
    return f && (s.service.active_release ?? 0) <= f.number + 1
      ? [{ s, number: f.number }]
      : [];
  });
  const deploying = services.some((s) => s.deploying);
  const running = services.filter(
    (s) => s.service.status.state === "running",
  ).length;
  let last: string | undefined;
  for (const s of services) {
    const at = (s.deploying ?? s.latest_release)?.created_at;
    if (at && (!last || at > last)) last = at;
  }
  const base = { last, deploying, note: "" };
  const [oneFailing] = failing;
  if (oneFailing)
    return {
      ...base,
      rank: 3,
      tone: "text-fail",
      glyph: "✕" as const,
      text:
        failing.length === 1
          ? `${oneFailing.service.name} failing`
          : `${plural(failing.length, "service")} failing`,
    };
  const [oneFailed] = failedDeploys;
  if (oneFailed) {
    const serving = oneFailed.s.service.active_release;
    return {
      ...base,
      rank: 2,
      tone: "text-fail",
      glyph: "✕" as const,
      text:
        failedDeploys.length === 1
          ? `${oneFailed.s.service.name} r${oneFailed.number} failed`
          : `${plural(failedDeploys.length, "deploy")} failed`,
      note:
        failedDeploys.length === 1 && serving
          ? `, r${serving} still serving`
          : "",
    };
  }
  if (deploying)
    return {
      ...base,
      rank: 1,
      tone: "text-prog",
      glyph: "◐" as const,
      text: "deploying",
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
      text: services.length === 0 ? "no services" : note,
    };
  return {
    ...base,
    rank: 0,
    tone: "text-ok",
    glyph: "●" as const,
    text: note ? `${running} running` : "all running",
    note: note ? ` · ${note}` : "",
  };
}

/**
 * Whether a project shows as a pipeline on the page: something in it is
 * broken, deploying or waiting (health ranks it), changes wait to apply,
 * a backup failed, or an image waits to be promoted. Quiet projects are a
 * row each, so ten of them still fit on a screen.
 */
function busy(o: ProjectOverview | undefined, h: ReturnType<typeof health>) {
  if (!o) return false;
  if ((h?.rank ?? 0) > 0) return true;
  const { lanes } = stages(o.environments);
  if (lanes.some((e) => e.pending_changes > 0)) return true;
  if (lanes.some((e) => e.services.some((s) => s.backup?.status === "failed")))
    return true;
  return lanes.some((e, i) => {
    const before = lanes[i - 1];
    if (!before) return false;
    return e.services.some((s) => {
      const k = step(
        before.services.find((b) => b.service.name === s.service.name),
        s,
      )?.kind;
      return k === "ahead" || k === "failed";
    });
  });
}

function NoProjects() {
  const org = useOrg();
  // Only operators can list servers, so only they see where it runs.
  const server = useServersIfOperator().find((s) => s.local)?.name;
  const canCreate = canCreateProject(useSession());
  const steps = [
    [
      cliInstallLine(window.location.origin),
      "Installs the dock CLI (macOS, Linux or WSL) and signs you in. Have it already: dock login.",
    ],
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
        title="Projects"
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

/** Open alerts the page lists; the rest are a count into the Inbox. */
const NEEDS_SHOWN = 5;

/**
 * What needs someone (ADR 0099): the first few open alerts, worst first
 * and grouped as in the Inbox, each with the button to its fix, so the
 * usual case is handled from here; more than that is a line into the
 * Inbox, the one list of all of it.
 */
function NeedsYou() {
  const org = useOrg();
  const open = useOrgOpenAlerts();
  if (open.length === 0) {
    return (
      <p className="flex items-center gap-2.5 border-y border-line py-2.5 text-mut">
        <span className="text-ok">●</span> Nothing needs you.
      </p>
    );
  }
  const groups = groupAlerts(open);
  const more = groups.length - NEEDS_SHOWN;
  return (
    <section aria-labelledby="needs-head">
      <SectionHead
        id="needs-head"
        aside={groups.length}
        right={
          <Link to="/$org/inbox" params={{ org }} className="hover:text-fg">
            {more > 0 ? `${more} more in the Inbox →` : "Open inbox →"}
          </Link>
        }
      >
        Needs you
      </SectionHead>
      <AlertTable
        scope="org"
        groups={groups.slice(0, NEEDS_SHOWN)}
        resolvable
        brief
      />
    </section>
  );
}

/** A release's image without its registry path, digests cut to 7. */
const shortImage = (ref: string) =>
  (ref.split("/").at(-1) ?? ref).replace(
    /@sha256:([0-9a-f]{7})[0-9a-f]+/,
    "@$1",
  );

/**
 * A deploy's audit event as a sentence after the service's name:
 * "r9 rolled back to r7", "deployed r1 from shop-web:1.4.0".
 */
function deployWords(e: AuditEvent) {
  const back = e.summary.match(/^release (\d+) rolls back to release (\d+)/);
  if (back) return `r${back[1]} rolled back to r${back[2]}`;
  const rest = e.summary
    .replace(/\brelease (\d+)/g, "r$1")
    .replace(
      /from image (\S+)/,
      (_, image: string) => `from ${shortImage(image)}`,
    );
  return `${pastLabel(e.action)} ${rest}`;
}

/**
 * The latest five deploys, rollbacks and restarts across the
 * organization: the service, what happened, where and how long ago.
 */
function RecentDeploys() {
  const org = useOrg();
  const events = useQuery(queries.audit({ action: "release.", limit: 5 })).data;
  if (!events?.length) return null;
  return (
    <section aria-labelledby="deploys-head">
      <SectionHead
        id="deploys-head"
        right={
          <Link
            to="/$org/settings/activity"
            params={{ org }}
            className="hover:text-fg"
          >
            All activity →
          </Link>
        }
      >
        Recent deploys
      </SectionHead>
      <ul className="border-t border-line">
        {events.map((e) => {
          const parts = e.target.split("/");
          const service = parts.at(-1);
          return (
            <li
              key={e.id}
              className="flex flex-wrap items-baseline gap-x-3 border-b border-soft py-2"
            >
              <span className="min-w-0 flex-1" title={e.summary}>
                <span className="font-medium">{service}</span> {deployWords(e)}
              </span>
              <span className="font-mono text-xs text-mut">
                {parts.slice(0, -1).join("/")}
              </span>
              <span
                className="w-20 text-right text-xs text-mut"
                title={new Date(e.created_at).toLocaleString()}
              >
                {ago(e.created_at)}
              </span>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
