import type { QueryClient } from "@tanstack/react-query";
import { queries } from "../../data/queries";

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
