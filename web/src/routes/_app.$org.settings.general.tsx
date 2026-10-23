import { createFileRoute } from "@tanstack/react-router";
import { orgQueries } from "../features/org/data";
import { GeneralPage } from "../features/org/GeneralPage";

export const Route = createFileRoute("/_app/$org/settings/general")({
  loader: ({ context }) =>
    context.queryClient.ensureQueryData(orgQueries.org()),
  component: GeneralPage,
});
