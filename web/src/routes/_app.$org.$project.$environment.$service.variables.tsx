import { createFileRoute } from "@tanstack/react-router";
import { envPath, queries } from "../data/queries";
import { VariablesEditor } from "../features/variables/VariablesEditor";

// A service's Variables tab: what it sees at runtime, grouped (its own,
// edited here; the environment's shared lines and the organization's,
// with where they are edited). The project's Settings edit the shared
// lines. It lays out its own page (the service layout's ownLayout).
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
