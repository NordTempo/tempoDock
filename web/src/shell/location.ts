import { useParams } from "@tanstack/react-router";

/** Where the user is in the project tree: the route's name params. */
export function useCurrentContext() {
  const { project, environment, service } = useParams({ strict: false });
  return { project, environment, service };
}
