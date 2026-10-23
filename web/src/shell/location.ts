import { useParams } from "@tanstack/react-router";
import { currentOrg } from "../api/client";

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

/**
 * The organization the page acts in: the URL's, or (outside /$org, as in
 * the console) the one last used. Links to org pages pass it as `org`.
 */
export function useOrg(): string {
  const { org } = useParams({ strict: false });
  return org ?? currentOrg() ?? "";
}
