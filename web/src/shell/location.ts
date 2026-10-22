import { useParams } from "@tanstack/react-router";

/** Where the user is in the project tree: the route's name params. */
export function useCurrentContext() {
  const { project, environment, service, number } = useParams({
    strict: false,
  });
  return { project, environment, service, release: number };
}

const LAST_PROJECT = "dock.project";

/** The project the user last looked at, for pages outside any project. */
export const lastProject = () => localStorage.getItem(LAST_PROJECT);
export const rememberProject = (project: string) =>
  localStorage.setItem(LAST_PROJECT, project);
