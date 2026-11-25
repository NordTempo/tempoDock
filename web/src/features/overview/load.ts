import type { QueryClient } from "@tanstack/react-query";
import { queries } from "../../data/queries";
import type { ProjectOverview } from "../../data/types";

// Kept apart from ProjectView: route loaders aren't code-split, so what
// they import lands in the first page load.

/**
 * Loads what ProjectView reads: the overview and the project's activity,
 * plus the staged changes of environments that have some (their problems
 * say which releases applying creates). The palette loads release history
 * for rollbacks itself, once opened. Answers the overview.
 */
export async function loadProject(queryClient: QueryClient, project: string) {
  const [overview] = await Promise.all([
    queryClient.ensureQueryData(queries.overview(project)),
    queryClient.ensureQueryData(queries.audit({ project })),
  ]);
  await Promise.all(
    overview.environments
      .filter((e) => e.pending_changes > 0)
      .map((e) =>
        queryClient.ensureQueryData(
          queries.changes({ project, environment: e.environment.name }),
        ),
      ),
  );
  return overview;
}

/**
 * The environment a project's page opens on when its address names none:
 * the first production one, else the first that isn't a preview.
 */
export function defaultEnvironment(overview: ProjectOverview) {
  const envs = overview.environments;
  return (
    envs.find((e) => e.environment.kind === "production") ??
    envs.find((e) => !e.environment.preview) ??
    envs[0]
  )?.environment.name;
}
