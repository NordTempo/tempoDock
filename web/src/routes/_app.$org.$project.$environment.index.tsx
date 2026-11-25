import { createFileRoute, notFound, redirect } from "@tanstack/react-router";
import { loadProject } from "../features/overview/load";
import { ProjectView } from "../features/overview/ProjectView";

// An environment's address: the project's page with this environment
// picked, on its Services tab, or Activity with ?tab=activity. Its shared
// variables, log drains and placement are in the project's Settings
// (ADR 0059), and ?service=web, what one service sees, is the service's
// Variables tab.
export const Route = createFileRoute("/_app/$org/$project/$environment/")({
  validateSearch: (
    search: Record<string, unknown>,
  ): { service?: string; tab?: "activity" } => ({
    service:
      typeof search.service === "string" && search.service
        ? search.service
        : undefined,
    tab: search.tab === "activity" ? ("activity" as const) : undefined,
  }),
  beforeLoad: ({ params, search }) => {
    if (search.service) {
      throw redirect({
        to: "/$org/$project/$environment/$service/variables",
        params: { ...params, service: search.service },
        replace: true,
      });
    }
  },
  loader: async ({ context: { queryClient }, params }) => {
    const overview = await loadProject(queryClient, params.project);
    const known = overview.environments.some(
      (e) => e.environment.name === params.environment,
    );
    if (!known) throw notFound({ routeId: "/_app/$org" });
  },
  component: Environment,
});

function Environment() {
  const { project, environment } = Route.useParams();
  const { tab } = Route.useSearch();
  return (
    <ProjectView
      project={project}
      environment={environment}
      tab={tab ?? "services"}
    />
  );
}
