import { createFileRoute, Link, notFound } from "@tanstack/react-router";
import { envPath, queries, useProjectOverview } from "../data/queries";
import { LockStrip } from "../features/overview/EnvironmentLock";
import { VariablesEditor } from "../features/variables/VariablesEditor";
import { useOrg } from "../shell/location";

// An environment's shared variables, one environment at a time (ADR 0059).
// Log drains have their own tab.
export const Route = createFileRoute(
  "/_app/$org/$project/settings/variables/$environment",
)({
  loader: async ({ context: { queryClient }, params }) => {
    const overview = await queryClient.ensureQueryData(
      queries.overview(params.project),
    );
    if (
      !overview.environments.some(
        (e) => e.environment.name === params.environment,
      )
    ) {
      throw notFound();
    }
    await Promise.all([
      queryClient.ensureQueryData(queries.variables(params, null)),
      queryClient.ensureQueryData(queries.changes(params)),
    ]);
  },
  component: SharedVariables,
});

function SharedVariables() {
  const org = useOrg();
  const params = Route.useParams();
  const { environments } = useProjectOverview(params.project);
  const env = envPath(params);
  const lock = environments.find(
    (e) => e.environment.name === params.environment,
  )?.environment.lock;
  // Capped like the other settings tabs, scope list included.
  return (
    <div className="flex w-full max-w-[1400px] flex-col">
      {environments.length > 1 && (
        <nav
          aria-label="Environment"
          className="flex flex-wrap items-center gap-1.5 px-4 pt-4 sm:px-7"
        >
          {environments.map((e) => (
            <Link
              key={e.environment.id}
              to="/$org/$project/settings/variables/$environment"
              params={{
                org,
                project: params.project,
                environment: e.environment.name,
              }}
              className="btn"
              activeProps={{ className: "bg-sel text-fg" }}
            >
              {e.environment.name}
              {e.pending_changes > 0 && <span className="text-warn"> ▲</span>}
            </Link>
          ))}
        </nav>
      )}
      {lock && (
        <div className="px-4 pt-4 sm:px-7">
          <LockStrip envRef={env} lock={lock} />
        </div>
      )}
      <VariablesEditor key={params.environment} env={env} service={null} />
    </div>
  );
}
