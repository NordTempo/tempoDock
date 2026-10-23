import { createFileRoute } from "@tanstack/react-router";
import { orgQueries } from "../features/org/data";
import { VariablesPage } from "../features/org/VariablesPage";

export const Route = createFileRoute("/_app/$org/settings/variables")({
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(orgQueries.variables()),
  component: VariablesPage,
});
