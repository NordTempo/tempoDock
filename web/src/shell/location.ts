import { useMatches, useParams } from "@tanstack/react-router";

declare module "@tanstack/react-router" {
  interface StaticDataRouteOption {
    /** The page's name at the end of the top bar breadcrumb. */
    crumb?: string;
  }
}

/** Where the user is in the project tree: the route's name params. */
export function useCurrentContext() {
  const { project, environment, service, number } = useParams({
    strict: false,
  });
  return { project, environment, service, release: number };
}

/** The deepest route's breadcrumb name, like "Compare" or "Variables". */
export const useCrumb = () =>
  useMatches({
    select: (matches) =>
      matches.findLast((m) => m.staticData.crumb)?.staticData.crumb,
  });

const LAST_PROJECT = "dock.project";

/** The project the user last looked at, for pages outside any project. */
export const lastProject = () => localStorage.getItem(LAST_PROJECT);
export const rememberProject = (project: string) =>
  localStorage.setItem(LAST_PROJECT, project);
