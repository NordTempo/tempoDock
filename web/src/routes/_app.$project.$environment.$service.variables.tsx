import { createFileRoute } from "@tanstack/react-router";
import { envPath, queries } from "../data/queries";
import { VariablesEditor } from "../features/variables/VariablesEditor";

export const Route = createFileRoute(
  "/_app/$project/$environment/$service/variables",
)({
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(
      queries.variables(params, params.service),
    ),
  component: VariablesTab,
});

function VariablesTab() {
  const params = Route.useParams();
  return (
    <VariablesEditor
      env={envPath(params)}
      service={params.service}
      note={`${params.service} and shared in ${params.environment}`}
    />
  );
}
