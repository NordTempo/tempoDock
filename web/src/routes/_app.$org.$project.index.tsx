import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { lazy, Suspense, useEffect, useState } from "react";
import { withOrg } from "../api/client";
import { useArchiveProject } from "../data/mutations";
import {
  canDeploy,
  queries,
  serviceRef,
  useIsAdmin,
  useSession,
} from "../data/queries";
import { canCreateEnvironment } from "../features/create/access";
import { openCreate } from "../features/create/open";
import { ArchivedProject } from "../features/overview/Archived";
import { DeployBanner } from "../features/overview/DeployBanner";
import { EnvironmentTable } from "../features/overview/EnvironmentTable";
import { Activity } from "../features/overview/Live";
import { Placements } from "../features/overview/Placements";
import { Problems } from "../features/overview/Problems";
import { previewQueries } from "../features/previews/data";
import { PreviewsSection } from "../features/previews/PreviewsSection";
import { ServerDots } from "../shell/context";
import { useShortcuts } from "../shell/keys";
import { rememberProject, useOrg } from "../shell/location";
import { usePalette } from "../shell/palette";
import { plural } from "../ui/format";
import { Act, Button, ErrorText, Glyph, Page, PageBar } from "../ui/kit";

const RenameProject = lazy(() => import("../features/overview/RenameProject"));
const ApplyFile = lazy(() => import("../features/apply/ApplyFile"));

export const Route = createFileRoute("/_app/$org/$project/")({
  loader: async ({ context: { queryClient }, params }) => {
    const [overview] = await Promise.all([
      queryClient.ensureQueryData(queries.overview(params.project)),
      queryClient.ensureQueryData(queries.audit({ project: params.project })),
      queryClient.ensureQueryData(queries.placements(params.project)),
      // Preview statuses and settings; the page shows without them.
      queryClient.prefetchQuery(previewQueries.settings(params.project)),
    ]);
    // Staged problems say which releases applying creates. (The palette
    // loads release history for rollbacks itself, once opened.)
    await Promise.all(
      overview.environments
        .filter((e) => e.pending_changes > 0)
        .map((e) =>
          queryClient.ensureQueryData(
            queries.changes({
              project: params.project,
              environment: e.environment.name,
            }),
          ),
        ),
    );
  },
  component: Overview,
});

/** An archived project gets a read-only page; a live one, the overview. */
function Overview() {
  const { project } = Route.useParams();
  const archived = useSuspenseQuery({
    ...queries.overview(project),
    select: (o) => !!o.project.archived_at,
  }).data;
  if (archived) {
    return (
      <ArchivedProject project={project}>
        <Export project={project} />
      </ArchivedProject>
    );
  }
  return <LiveOverview />;
}

function LiveOverview() {
  const org = useOrg();
  const { project } = Route.useParams();
  // CPU and memory columns show the latest minute sample; usage events
  // patch it (data/live.ts).
  const overview = useSuspenseQuery(queries.overview(project)).data;
  const { environments } = overview;
  const session = useSession();
  const canRename =
    session?.scope === "admin" &&
    !session.environment &&
    (!session.project || session.project === project);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [applying, setApplying] = useState(false);
  // Staging a file needs deploy in the project (environment-limited
  // tokens only their own environment, which the dialog fixes).
  const canApply =
    !!session &&
    session.scope !== "read" &&
    (!session.project || session.project === project);
  const navigate = useNavigate();
  const openPalette = usePalette();
  const services = environments.flatMap((e) => e.services);
  const deploying = services.filter((s) => s.deploying);
  const varsEnv =
    environments.find((e) => e.pending_changes > 0) ?? environments[0];
  const canNewEnv = canCreateEnvironment(session, project);
  // Deploy rights may be limited to one environment; any one will do.
  const mayDeploy =
    services.length > 0 &&
    environments.some((e) =>
      canDeploy(session, { project, environment: e.environment.name }),
    );
  const newEnvironment = () => openCreate({ kind: "environment", project });

  useEffect(() => rememberProject(project), [project]);

  const watch = deploying[0];
  useShortcuts([
    ...(environments.length > 1
      ? [
          {
            key: "c",
            label: "compare",
            run: () =>
              navigate({
                to: "/$org/$project/compare",
                params: { org, project },
              }),
          },
        ]
      : []),
    ...(varsEnv
      ? [
          {
            key: "v",
            label: "variables",
            run: () =>
              navigate({
                to: "/$org/$project/$environment",
                params: { org, project, environment: varsEnv.environment.name },
              }),
          },
        ]
      : []),
    ...(mayDeploy
      ? [{ key: "d", label: "deploy", run: () => openPalette("deploy ") }]
      : []),
    ...(watch
      ? [
          {
            key: "w",
            label: "watch deploy",
            run: () =>
              navigate({
                to: "/$org/$project/$environment/$service",
                params: { org, ...serviceRef(watch.service) },
              }),
          },
        ]
      : []),
  ]);

  return (
    <>
      <PageBar
        title={project}
        after={
          <>
            <span className="text-xs text-faint">
              {plural(environments.length, "environment")} ·{" "}
              {plural(services.length, "service")}
            </span>
            <ServerDots project={project} />
          </>
        }
        actions={
          <>
            {canRename && (
              <Act
                cli={`dock project rename ${project} <name>`}
                onClick={() => setRenaming(project)}
              >
                Rename project
              </Act>
            )}
            {canNewEnv && (
              <Act
                cli={`dock environment create <name> -p ${project}`}
                onClick={newEnvironment}
              >
                New environment
              </Act>
            )}
            {environments.length > 1 && (
              <Link
                to="/$org/$project/compare"
                params={{ org, project }}
                className="btn"
              >
                Compare <kbd>C</kbd>
              </Link>
            )}
            {varsEnv && (
              <Link
                to="/$org/$project/$environment"
                params={{ org, project, environment: varsEnv.environment.name }}
                className="btn"
              >
                Variables <kbd>V</kbd>
              </Link>
            )}
            {mayDeploy && (
              <Button primary onClick={() => openPalette("deploy ")}>
                Deploy a service <kbd>D</kbd>
              </Button>
            )}
          </>
        }
      />
      {applying && (
        <Suspense fallback={null}>
          <ApplyFile project={project} onClose={() => setApplying(false)} />
        </Suspense>
      )}
      {renaming === project && (
        <Suspense fallback={null}>
          <RenameProject
            key={project}
            project={overview.project}
            onClose={() => setRenaming(null)}
          />
        </Suspense>
      )}
      <Page>
        {deploying.length > 0 && (
          <div className="flex flex-col gap-1.5">
            {deploying.map((s, i) => (
              <DeployBanner key={s.service.id} service={s} watchKey={i === 0} />
            ))}
          </div>
        )}

        <Problems project={project} />

        {environments.map((env) => (
          <EnvironmentTable key={env.environment.id} env={env} />
        ))}

        {environments.length === 0 && (
          <p className="flex flex-wrap items-center gap-2.5 text-mut">
            No environments yet.
            {canNewEnv && (
              <Button small primary onClick={newEnvironment}>
                New environment
              </Button>
            )}
          </p>
        )}

        <div className="grid grid-cols-[repeat(auto-fit,minmax(min(100%,340px),1fr))] gap-x-7 gap-y-7">
          <Activity project={project} />
          <Placements project={project} />
        </div>

        <PreviewsSection project={project} />

        <p className="text-2xs text-faint">
          <span className="text-ok">
            <Glyph char="●" />
          </span>{" "}
          running ·{" "}
          <span className="text-prog">
            <Glyph char="◐" />
          </span>{" "}
          in progress · ▲ needs action · ✕ failed · ↻ restarting · ○ stopped ·{" "}
          <span className="text-info">◇</span> token · ○ person
        </p>

        <Export
          project={project}
          onApply={canApply ? () => setApplying(true) : undefined}
        />
        <ArchiveProject project={project} />
      </Page>
    </>
  );
}

/**
 * `dock project export`: the project's config as a download, TOML as the
 * CLI prints it or the API's JSON. Variable values are never in it. With
 * onApply, "Apply file" opens `dock apply FILE` (live projects only).
 */
function Export({
  project,
  onApply,
}: {
  project: string;
  onApply?: () => void;
}) {
  const path = `/api/v1/projects/${encodeURIComponent(project)}/export`;
  return (
    <p className="flex flex-wrap items-baseline gap-x-3 text-xs text-faint">
      Export config
      <a
        href={withOrg(`${path}.toml`)}
        download
        className="lnk font-mono"
        title={`dock project export ${project} > ${project}.dock.toml`}
      >
        {project}.dock.toml
      </a>
      <a
        href={withOrg(path)}
        download={`${project}.dock.json`}
        className="lnk font-mono"
        title={`dock project export ${project} --format json`}
      >
        {project}.dock.json
      </a>
      {onApply && (
        <button
          type="button"
          className="lnk"
          title={`dock apply ${project}.dock.toml -p ${project}`}
          onClick={onApply}
        >
          Apply file
        </button>
      )}
    </p>
  );
}

/** Admin only, behind a confirm: archive the whole project. */
function ArchiveProject({ project }: { project: string }) {
  const isAdmin = useIsAdmin();
  const archive = useArchiveProject();
  const navigate = useNavigate();
  const [confirming, setConfirming] = useState(false);
  if (!isAdmin) return null;
  if (!confirming) {
    return (
      <div>
        <button
          type="button"
          className="lnk text-xs"
          onClick={() => setConfirming(true)}
        >
          Archive {project}
        </button>
      </div>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-panel border border-fail-edge bg-fail-bg/40 px-3.5 py-3">
      <span>
        Archive <b className="font-semibold">{project}</b>? Every service stops
        and its routes are removed. Volumes and history are kept.
      </span>
      <Button
        danger
        small
        disabled={archive.isPending}
        onClick={() =>
          archive.mutate(project, { onSuccess: () => navigate({ to: "/" }) })
        }
      >
        Archive {project}
      </Button>
      <Button small onClick={() => setConfirming(false)}>
        Cancel
      </Button>
      <ErrorText error={archive.error} />
    </div>
  );
}
