import { useSuspenseQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";
import { queries, serviceRef, useSession } from "../../data/queries";
import { useShortcuts } from "../../shell/keys";
import { rememberProject, useOrg } from "../../shell/location";
import { Button, Page } from "../../ui/kit";
import { canCreateEnvironment } from "../create/access";
import { openCreate } from "../create/open";
import { DeployBanner } from "./DeployBanner";
import { EnvironmentTable } from "./EnvironmentTable";
import { Activity } from "./Live";
import { defaultEnvironment } from "./load";
import { Problems } from "./Problems";
import { ProjectHeader, type ProjectTab } from "./ProjectHeader";

/**
 * A live project's page, for one environment at a time (the address's,
 * else defaultEnvironment): the shared header, then the Services tab
 * (deploys in flight, what needs a look, the services) or the Activity
 * tab. Settings is its own route under the same header.
 */
export function ProjectView({
  project,
  environment: named,
  tab = "services",
}: {
  project: string;
  /** The environment in the address, if any. */
  environment?: string;
  tab?: Exclude<ProjectTab, "settings">;
}) {
  const org = useOrg();
  const navigate = useNavigate();
  const overview = useSuspenseQuery(queries.overview(project)).data;
  const environment = named ?? defaultEnvironment(overview);
  const env = overview.environments.find(
    (e) => e.environment.name === environment,
  );
  const session = useSession();
  const deploying = env?.services.filter((s) => s.deploying) ?? [];

  useEffect(() => rememberProject(project), [project]);

  const watch = deploying[0];
  useShortcuts(
    watch
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
      : [],
  );

  return (
    <>
      <ProjectHeader project={project} environment={environment} tab={tab} />
      <Page>
        {!env ? (
          <p className="flex flex-wrap items-center gap-2.5 text-mut">
            No environments yet.
            {canCreateEnvironment(session, project) && (
              <Button
                small
                primary
                onClick={() => openCreate({ kind: "environment", project })}
              >
                New environment
              </Button>
            )}
          </p>
        ) : tab === "activity" ? (
          <Activity
            project={project}
            environment={env.environment.name}
            limit={50}
          />
        ) : (
          <>
            {deploying.length > 0 && (
              <div className="flex flex-col gap-1.5">
                {deploying.map((s, i) => (
                  <DeployBanner
                    key={s.service.id}
                    service={s}
                    watchKey={i === 0}
                  />
                ))}
              </div>
            )}
            <Problems project={project} environment={env.environment.name} />
            <EnvironmentTable env={env} />
          </>
        )}
      </Page>
    </>
  );
}
