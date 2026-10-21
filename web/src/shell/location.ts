import { useParams } from "@tanstack/react-router";
import { useProjects } from "../data/queries";

/** Where the user is in the project tree, from the current route's params. */
export function useCurrentContext() {
  const params = useParams({ strict: false });
  const projects = useProjects();
  for (const project of projects) {
    for (const env of project.environments) {
      const service = env.services.find((s) => s.id === params.serviceId);
      if (service) return { project, env, service };
    }
  }
  const project = projects.find((p) => p.id === params.projectId);
  return { project, env: undefined, service: undefined };
}

export const useCurrentProjectId = () => useCurrentContext().project?.id;
