import { createFileRoute } from "@tanstack/react-router";
import { envPath, queries } from "../data/queries";
import { VariablesEditor } from "../features/variables/VariablesEditor";

// A service's Variables tab: what it sees at runtime (shared lines and its
// own), edited in place under the service's header. The environment page
// (/shop/production) has the same editor for the shared lines.
export const Route = createFileRoute(
  "/_app/$org/$project/$environment/$service/variables",
)({
  loader: ({ context: { queryClient }, params }) =>
    Promise.all([
      queryClient.ensureQueryData(queries.variables(envPath(params), null)),
      queryClient.ensureQueryData(queries.changes(envPath(params))),
    ]),
  component: ServiceVariables,
});

function ServiceVariables() {
  const params = Route.useParams();
  return (
    <VariablesEditor
      key={`${params.environment}/${params.service}`}
      env={envPath(params)}
      service={params.service}
    />
  );
}
