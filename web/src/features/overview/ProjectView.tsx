import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";
import { canDeploy, queries, serviceRef, useSession } from "../../data/queries";
import { ServerDots } from "../../shell/context";
import { useShortcuts } from "../../shell/keys";
import { rememberProject, useOrg } from "../../shell/location";
import { Act, Button, Glyph, Page, PageBar } from "../../ui/kit";
import { sitesQuery } from "../analytics/loaders";
import { canCreateEnvironment } from "../create/access";
import { openCreate } from "../create/open";
import { DeployBanner } from "./DeployBanner";
import { EnvironmentTable } from "./EnvironmentTable";
import { Activity } from "./Live";
import { Problems } from "./Problems";

/**
 * A live project's page: what needs a look, a table of services per
 * environment, and recent activity. With `environment` it is that
 * environment's page (/org/project/env): one table, its problems and
 * activity, titled by the environment under the project.
 */
export function ProjectView({
  project,
  environment: only,
}: {
  project: string;
  /** Scopes the page to one environment: its problems, services and actions. */
  environment?: string;
}) {
  const org = useOrg();
  // The project's analytics, one click from its page: the site for the
  // environment in view, else the first one the project has.
  const site = useQuery({
    ...sitesQuery(project),
    select: (sites) => {
      const live = sites.filter((x) => !x.archived);
      return (live.find((x) => !only || x.environment === only) ?? live[0])
        ?.name;
    },
  }).data;
  // CPU and memory columns show the latest minute sample; usage events
  // patch it (data/live.ts).
  const overview = useSuspenseQuery(queries.overview(project)).data;
  const environments = only
    ? overview.environments.filter((e) => e.environment.name === only)
    : overview.environments;
  const session = useSession();
  const navigate = useNavigate();
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
  const deployable = environments.filter((e) =>
    canDeploy(session, { project, environment: e.environment.name }),
  );
  const here = only;
  // A new service goes to the environment in the URL, else production, or
  // the first environment the caller may deploy to; the dialog lets them
  // pick another.
  const target =
    deployable.find((e) => e.environment.name === here) ??
    deployable.find((e) => e.environment.kind === "production") ??
    deployable[0];
  const newService = target
    ? () =>
        openCreate({
          kind: "service",
          env: { project, environment: target.environment.name },
        })
    : undefined;
  const newEnvironment = () => openCreate({ kind: "environment", project });

  useEffect(() => rememberProject(project), [project]);

  const watch = deploying[0];
  useShortcuts([
    ...(overview.environments.length > 1
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
    ...(newService
      ? [{ key: "n", label: "new service", run: newService }]
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
        parent={
          here && (
            <Link
              to="/$org/$project"
              params={{ org, project }}
              className="hover:text-fg"
            >
              {project}
            </Link>
          )
        }
        title={here ?? project}
        after={<ServerDots project={project} />}
        actions={
          <>
            {canNewEnv && !only && (
              <Act
                cli={`dock environment create <name> -p ${project}`}
                onClick={newEnvironment}
              >
                New environment
              </Act>
            )}
            {site && (
              <Link
                to="/$org/analytics/$project/$site"
                params={{ org, project, site }}
                className="btn"
              >
                Analytics
              </Link>
            )}
            {overview.environments.length > 1 && (
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
            {newService && (
              <Act
                primary
                cli={`dock service create <name> -p ${project} -e ${target?.environment.name}`}
                onClick={newService}
              >
                New service <kbd>N</kbd>
              </Act>
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

        <Problems project={project} environment={only} />

        {environments.map((env) => (
          <EnvironmentTable
            key={env.environment.id}
            env={env}
            scoped={!!only}
          />
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

        <Activity project={project} environment={only} />

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
