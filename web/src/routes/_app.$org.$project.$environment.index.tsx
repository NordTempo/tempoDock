import { createFileRoute, notFound } from "@tanstack/react-router";
import { useEffect } from "react";
import { envPath, queries } from "../data/queries";
import { logDrainQueries } from "../features/logdrains/data";
import { LogDrains } from "../features/logdrains/LogDrains";
import { VariablesEditor } from "../features/variables/VariablesEditor";
import { rememberProject } from "../shell/location";

// An environment's page: its variables (its shared lines, or with
// ?service=web what one service sees at runtime) and, under the shared
// lines, its log drains.
export const Route = createFileRoute("/_app/$org/$project/$environment/")({
  validateSearch: (search: Record<string, unknown>): { service?: string } =>
    typeof search.service === "string" && search.service
      ? { service: search.service }
      : {},
  loader: async ({ context: { queryClient }, params }) => {
    const overview = await queryClient.ensureQueryData(
      queries.overview(params.project),
    );
    if (
      !overview.environments.some(
        (e) => e.environment.name === params.environment,
      )
    ) {
      throw notFound();
    }
    await Promise.all([
      queryClient.ensureQueryData(queries.variables(params, null)),
      queryClient.ensureQueryData(queries.changes(params)),
      queryClient.ensureQueryData(logDrainQueries.list(envPath(params))),
    ]);
  },
  component: EnvironmentVariables,
});

function EnvironmentVariables() {
  const params = Route.useParams();
  const { service } = Route.useSearch();
  useEffect(() => rememberProject(params.project), [params.project]);
  const env = envPath(params);
  return (
    <VariablesEditor
      key={params.environment}
      env={env}
      service={service ?? null}
      footer={service ? undefined : <LogDrains env={env} />}
    />
  );
}
