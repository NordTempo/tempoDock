import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";
import { canDeploy, queries, serviceRef, useSession } from "../data/queries";
import { canCreateEnvironment } from "../features/create/access";
import { openCreate } from "../features/create/open";
import { ArchivedProject } from "../features/overview/Archived";
import { DeployBanner } from "../features/overview/DeployBanner";
import { EnvironmentTable } from "../features/overview/EnvironmentTable";
import { Activity } from "../features/overview/Live";
import { Problems } from "../features/overview/Problems";
import { Export } from "../features/overview/ProjectSettings";
import { ServerDots } from "../shell/context";
import { useShortcuts } from "../shell/keys";
import { rememberProject, useOrg } from "../shell/location";
import { usePalette } from "../shell/palette";
import { plural } from "../ui/format";
import { Act, Button, Glyph, Page, PageBar } from "../ui/kit";

export const Route = createFileRoute("/_app/$org/$project/")({
  loader: async ({ context: { queryClient }, params }) => {
    const [overview] = await Promise.all([
      queryClient.ensureQueryData(queries.overview(params.project)),
      queryClient.ensureQueryData(queries.audit({ project: params.project })),
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
  const navigate = useNavigate();
  const openPalette = usePalette();
  const services = environments.flatMap((e) => e.services);
  const deploying = services.filter((s) => s.deploying);
  // Settings opens on Variables while an environment's changes wait.
  const staged = environments.find((e) => e.pending_changes > 0);
  const openSettings = () =>
    staged
      ? navigate({
          to: "/$org/$project/settings/variables/$environment",
          params: { org, project, environment: staged.environment.name },
        })
      : navigate({ to: "/$org/$project/settings", params: { org, project } });
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
    { key: "s", label: "settings", run: openSettings },
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
            <Button onClick={openSettings}>
              Settings{staged && <span className="text-warn"> ▲</span>}{" "}
              <kbd>S</kbd>
            </Button>
            {mayDeploy && (
              <Button primary onClick={() => openPalette("deploy ")}>
                Deploy a service <kbd>D</kbd>
              </Button>
            )}
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

        <Activity project={project} />

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
      </Page>
    </>
  );
}
