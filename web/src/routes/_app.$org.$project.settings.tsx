import { createFileRoute, Outlet } from "@tanstack/react-router";
import { useEffect } from "react";
import { queries, useProjectOverview } from "../data/queries";
import { defaultEnvironment } from "../features/overview/load";
import { ProjectHeader } from "../features/overview/ProjectHeader";
import { rememberProject } from "../shell/location";

// A project's Settings tab (ADR 0059): the project page's header, then
// every section on one page. ?env= is the environment the per-environment
// sections (shared variables, log drains) show, the header's switch.
export const Route = createFileRoute("/_app/$org/$project/settings")({
  validateSearch: (search: Record<string, unknown>): { env?: string } =>
    typeof search.env === "string" && search.env ? { env: search.env } : {},
  loader: ({ context: { queryClient }, params }) =>
    queryClient.ensureQueryData(queries.overview(params.project)),
  component: ProjectSettings,
});

function ProjectSettings() {
  const { project } = Route.useParams();
  const { env } = Route.useSearch();
  const overview = useProjectOverview(project);
  useEffect(() => rememberProject(project), [project]);
  const environment = overview.environments.some(
    (e) => e.environment.name === env,
  )
    ? env
    : defaultEnvironment(overview);
  return (
    <>
      <ProjectHeader
        project={project}
        environment={environment}
        tab="settings"
      />
      <Outlet />
    </>
  );
}
