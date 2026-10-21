import { createFileRoute, Link } from "@tanstack/react-router";
import { queries, useProjectOverview } from "../data/queries";
import { VariablesEditor } from "../features/variables/VariablesEditor";
import { Crumbs, TopBar } from "../shell/TopBar";

// Environment-level variables, shared by every service in the environment.
export const Route = createFileRoute("/_app/projects/$projectId/variables")({
  validateSearch: (search: Record<string, unknown>): { env?: string } =>
    typeof search.env === "string" ? { env: search.env } : {},
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(queries.overview(params.projectId)),
  component: EnvironmentVariables,
});

function EnvironmentVariables() {
  const { projectId } = Route.useParams();
  const { env: envName } = Route.useSearch();
  const { project, environments } = useProjectOverview(projectId);
  const env = environments.find((e) => e.name === envName) ?? environments[0];

  return (
    <>
      <TopBar>
        <Crumbs>
          {[
            <Link
              key="p"
              to="/projects/$projectId"
              params={{ projectId }}
              className="hover:text-white"
            >
              {project.name}
            </Link>,
            "variables",
          ]}
        </Crumbs>
        <span className="flex gap-3.5">
          {environments.map((e) => (
            <Link
              key={e.id}
              to="/projects/$projectId/variables"
              params={{ projectId }}
              search={{ env: e.name }}
              className={
                e.id === env?.id
                  ? "border-b border-white"
                  : "text-dim hover:text-white"
              }
            >
              {e.name}
            </Link>
          ))}
        </span>
      </TopBar>
      {env ? (
        <VariablesEditor
          key={env.id}
          target={{ environmentId: env.id, serviceId: null }}
          environmentName={env.name}
          note={`Creates a release for each of the ${env.services.length} services in ${env.name}. Health-checked before traffic.`}
        />
      ) : (
        <p className="p-5 text-dim">No environments.</p>
      )}
    </>
  );
}
