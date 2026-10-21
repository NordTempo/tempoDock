import { createFileRoute, Link, notFound } from "@tanstack/react-router";
import { envPath, queries, useProjectOverview } from "../data/queries";
import { VariablesEditor } from "../features/variables/VariablesEditor";
import { Crumbs, TopBar } from "../shell/TopBar";

// An environment's page: its shared variables, which every service gets.
export const Route = createFileRoute("/_app/$project/$environment/")({
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
  component: EnvironmentPage,
});

function EnvironmentPage() {
  const params = Route.useParams();
  const { project, environment } = params;
  const { environments } = useProjectOverview(project);
  const services = environments.find(
    (e) => e.environment.name === environment,
  )?.services;

  return (
    <>
      <TopBar>
        <Crumbs>
          {[
            <Link
              key="p"
              to="/$project"
              params={{ project }}
              className="hover:text-white"
            >
              {project}
            </Link>,
            "shared variables",
          ]}
        </Crumbs>
        <span className="flex gap-3.5">
          {environments.map(({ environment: e }) => (
            <Link
              key={e.id}
              to="/$project/$environment"
              params={{ project, environment: e.name }}
              className={
                e.name === environment
                  ? "border-b border-white"
                  : "text-dim hover:text-white"
              }
            >
              {e.name}
            </Link>
          ))}
        </span>
      </TopBar>
      <VariablesEditor
        key={environment}
        env={envPath(params)}
        service={null}
        note={`Shared by the ${services?.length ?? 0} services in ${environment}.`}
      />
    </>
  );
}
