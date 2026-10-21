import { createFileRoute, notFound } from "@tanstack/react-router";
import { useEffect } from "react";
import { envPath, queries } from "../data/queries";
import { VariablesEditor } from "../features/variables/VariablesEditor";
import { rememberProject } from "../shell/location";
import { Page } from "../ui/kit";

// An environment's variables: its shared lines, or (?service=web) what one
// service sees at runtime.
export const Route = createFileRoute("/_app/$project/$environment/")({
  validateSearch: (search: Record<string, unknown>): { service?: string } =>
    typeof search.service === "string" && search.service
      ? { service: search.service }
      : {},
  staticData: { crumb: "Variables" },
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
    ]);
  },
  component: EnvironmentVariables,
});

function EnvironmentVariables() {
  const params = Route.useParams();
  const { service } = Route.useSearch();
  useEffect(() => rememberProject(params.project), [params.project]);
  return (
    <Page gap="gap-4">
      <VariablesEditor
        key={params.environment}
        env={envPath(params)}
        service={service ?? null}
      />
    </Page>
  );
}
