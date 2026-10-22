import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { useArchiveProject } from "../data/mutations";
import { queries, serviceRef, useIsAdmin } from "../data/queries";
import { DeployBanner } from "../features/overview/DeployBanner";
import { EnvironmentTable } from "../features/overview/EnvironmentTable";
import { LiveColumn, Recent } from "../features/overview/Live";
import { Placements } from "../features/overview/Placements";
import { Problems } from "../features/overview/Problems";
import { useShortcuts } from "../shell/keys";
import { rememberProject } from "../shell/location";
import { usePalette } from "../shell/palette";
import { plural } from "../ui/format";
import { Button, ErrorText, Glyph } from "../ui/kit";

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
    <div className="flex flex-1 flex-wrap items-stretch">
      <main className="flex min-w-0 flex-[999_1_720px] flex-col gap-[26px] px-6 pt-5 pb-8">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <h1 className="text-xl font-semibold">{project}</h1>
          <span className="text-mut">
            {plural(environments.length, "environment")} ·{" "}
            {plural(services.length, "service")}
          </span>
          <div className="ml-auto flex flex-wrap items-center gap-2">
            {environments.length > 1 && (
              <Link
                to="/$project/compare"
                params={{ project }}
                className="px-1 py-[7px] no-underline"
              >
                Compare <kbd>C</kbd>
              </Link>
            )}
            {varsEnv && (
              <Link
                to="/$project/$environment"
                params={{ project, environment: varsEnv.environment.name }}
                className="px-1 py-[7px] no-underline"
              >
                Variables <kbd>V</kbd>
              </Link>
            )}
            <Button primary onClick={() => openPalette("deploy ")}>
              Deploy a service <kbd>D</kbd>
            </Button>
          </div>
        </div>

        {deploying.length > 0 && (
          <div className="-mb-1.5 flex flex-col gap-1">
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
          <p className="text-mut">
            No environments yet. Create one with{" "}
            <code className="font-mono text-white">
              dock environment create
            </code>
            .
          </p>
        )}

        <Placements project={project} />

        <Recent project={project} />

        <p className="text-xs text-mut">
          <span className="text-ok">●</span> running · <Glyph char="◐" /> in
          progress · ▲ needs action · ✕ failed · ↻ restarting · ○ stopped ·{" "}
          <span className="text-prog">◇</span> token · ○ person
        </p>

        <ArchiveProject project={project} />
      </main>
      <LiveColumn project={project} />
    </div>
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
          className="lnk"
          onClick={() => setConfirming(true)}
        >
          Archive {project}
        </button>
      </div>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-3 border border-fail-edge p-3">
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
