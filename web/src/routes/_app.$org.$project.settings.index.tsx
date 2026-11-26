import { createFileRoute, Link, redirect } from "@tanstack/react-router";
import { lazy, Suspense, useState } from "react";
import { queries, useProjectOverview, useSeesPlatform } from "../data/queries";
import type { Environment } from "../data/types";
import { logDrainQueries } from "../features/logdrains/data";
import { LogDrains } from "../features/logdrains/LogDrains";
import { DeleteEnvironment } from "../features/overview/DeleteEnvironment";
import { defaultEnvironment } from "../features/overview/load";
import { projectScoped } from "../features/overview/sections";
import { VariablesEditor } from "../features/variables/VariablesEditor";
import { Act, Page } from "../ui/kit";
import { SettingRow } from "../ui/sections";

const MovePanel = lazy(() => import("../features/move/MovePanel"));

// The Settings tab: the environment picked in the header and nothing
// else (ADR 0099): its shared variables, the server it runs on, its
// log drains and deleting it (ADR 0103). What applies to every environment is on Project settings;
// old links to those sections (#previews, #archive...) land there.
export const Route = createFileRoute("/_app/$org/$project/settings/")({
  beforeLoad: ({ params, location, search }) => {
    const hash = location.hash;
    if (projectScoped.has(hash)) {
      throw redirect({
        to: "/$org/$project/project-settings",
        params,
        search,
        hash,
        replace: true,
      });
    }
  },
  loaderDeps: ({ search }) => ({ env: search.env }),
  loader: async ({ context: { queryClient }, params, deps }) => {
    const overview = await queryClient.ensureQueryData(
      queries.overview(params.project),
    );
    const known = overview.environments.some(
      (e) => e.environment.name === deps.env,
    );
    const environment = known ? deps.env : defaultEnvironment(overview);
    if (!environment) return;
    const env = { project: params.project, environment };
    queryClient.prefetchQuery(logDrainQueries.list(env));
    await Promise.all([
      queryClient.ensureQueryData(queries.variables(env, null)),
      queryClient.ensureQueryData(queries.changes(env)),
    ]);
  },
  component: Settings,
});

function Settings() {
  const { project } = Route.useParams();
  const { env: asked } = Route.useSearch();
  const overview = useProjectOverview(project);
  const view =
    overview.environments.find((e) => e.environment.name === asked) ??
    overview.environments.find(
      (e) => e.environment.name === defaultEnvironment(overview),
    );
  if (!view) {
    return (
      <Page>
        <p className="text-mut">
          No environments yet. The project's own settings are on{" "}
          <Link
            from={Route.fullPath}
            to="/$org/$project/project-settings"
            className="text-sec"
          >
            Project settings
          </Link>
          .
        </p>
      </Page>
    );
  }
  const env = { project, environment: view.environment.name };
  return (
    <Page>
      <h2 className="flex flex-wrap items-baseline gap-x-3 text-lg font-semibold">
        <span className="font-mono">{env.environment}</span>
        <span className="text-xs font-normal text-mut">
          settings for this environment only
        </span>
      </h2>
      <div>
        <SettingRow id="variables" label="Shared variables">
          <VariablesEditor key={env.environment} env={env} service={null} />
        </SettingRow>
        <SettingRow id="server" label="Server">
          <EnvironmentServer environment={view.environment} />
        </SettingRow>
        <SettingRow id="log-drains" label="Log drains">
          <Suspense fallback={null}>
            <LogDrains key={env.environment} env={env} />
          </Suspense>
        </SettingRow>
        {!view.environment.preview && (
          <SettingRow id="delete" label="Delete" danger>
            <DeleteEnvironment
              environment={view.environment}
              only={
                overview.environments.filter((x) => !x.environment.preview)
                  .length <= 1
              }
            />
          </SettingRow>
        )}
      </div>
    </Page>
  );
}

/**
 * The server the environment runs on, with Move for operators (the
 * move's form and progress open under it).
 */
function EnvironmentServer({ environment: e }: { environment: Environment }) {
  const operator = useSeesPlatform();
  const [moving, setMoving] = useState(false);
  return (
    <div className="flex flex-col gap-2.5">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
        <span className="min-w-0 flex-1">
          {e.server ? (
            <span className="font-mono">{e.server}</span>
          ) : (
            <span className="text-mut">not placed yet</span>
          )}
          {e.moving && <span className="ml-2 text-xs text-prog">moving</span>}
        </span>
        {operator && (
          <Act
            small
            cli={`dock environment move ${e.name} --to <server> -p ${e.project}`}
            aria-expanded={moving}
            onClick={() => setMoving(!moving)}
          >
            {moving ? "Hide" : "Move"}
          </Act>
        )}
      </div>
      {moving && (
        <Suspense fallback={null}>
          <MovePanel
            env={{ project: e.project, environment: e.name }}
            server={e.server ?? ""}
            kind={e.kind}
            operator={operator}
          />
        </Suspense>
      )}
    </div>
  );
}
