import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { useArchiveProject } from "../data/mutations";
import { queries, serviceRef, useIsAdmin, useSession } from "../data/queries";
import { canCreateEnvironment } from "../features/create/access";
import { openCreate } from "../features/create/open";
import { DeployBanner } from "../features/overview/DeployBanner";
import { EnvironmentTable } from "../features/overview/EnvironmentTable";
import { Activity } from "../features/overview/Live";
import { Placements } from "../features/overview/Placements";
import { Problems } from "../features/overview/Problems";
import { ServerDots } from "../shell/context";
import { useShortcuts } from "../shell/keys";
import { rememberProject } from "../shell/location";
import { usePalette } from "../shell/palette";
import { plural } from "../ui/format";
import { Act, Button, ErrorText, Glyph, Page, PageBar } from "../ui/kit";

export const Route = createFileRoute("/_app/$project/")({
  loader: async ({ context: { queryClient }, params }) => {
    const [overview] = await Promise.all([
      queryClient.ensureQueryData(queries.overview(params.project)),
      queryClient.ensureQueryData(queries.audit({ project: params.project })),
      queryClient.ensureQueryData(queries.placements(params.project)),
    ]);
    await Promise.all([
      // Staged problems say which releases applying creates.
      ...overview.environments
        .filter((e) => e.pending_changes > 0)
        .map((e) =>
          queryClient.ensureQueryData(
            queries.changes({
              project: params.project,
              environment: e.environment.name,
            }),
          ),
        ),
      // Rollback commands in the palette need each service's history.
      ...overview.environments
        .flatMap((e) => e.services)
        .map((s) =>
          queryClient.prefetchQuery(queries.releases(serviceRef(s.service))),
        ),
    ]);
  },
  component: Overview,
});

function Overview() {
  const { project } = Route.useParams();
  // CPU and memory columns show the latest minute sample; usage events
  // patch it (data/live.ts).
  const { environments } = useSuspenseQuery(queries.overview(project)).data;
  const navigate = useNavigate();
  const openPalette = usePalette();
  const services = environments.flatMap((e) => e.services);
  const deploying = services.filter((s) => s.deploying);
  const varsEnv =
    environments.find((e) => e.pending_changes > 0) ?? environments[0];
  const canNewEnv = canCreateEnvironment(useSession(), project);
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
              navigate({ to: "/$project/compare", params: { project } }),
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
                to: "/$project/$environment",
                params: { project, environment: varsEnv.environment.name },
              }),
          },
        ]
      : []),
    { key: "d", label: "deploy", run: () => openPalette("deploy ") },
    ...(watch
      ? [
          {
            key: "w",
            label: "watch deploy",
            run: () =>
              navigate({
                to: "/$project/$environment/$service",
                params: serviceRef(watch.service),
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
            {canNewEnv && (
              <Act
                cli={`dock environment create <name> -p ${project}`}
                onClick={newEnvironment}
              >
                New environment
              </Act>
            )}
            {environments.length > 1 && (
              <Link to="/$project/compare" params={{ project }} className="btn">
                Compare <kbd>C</kbd>
              </Link>
            )}
            {varsEnv && (
              <Link
                to="/$project/$environment"
                params={{ project, environment: varsEnv.environment.name }}
                className="btn"
              >
                Variables <kbd>V</kbd>
              </Link>
            )}
            <Button primary onClick={() => openPalette("deploy ")}>
              Deploy a service <kbd>D</kbd>
            </Button>
          </>
        }
      />
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

        <ArchiveProject project={project} />
      </Page>
    </>
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
