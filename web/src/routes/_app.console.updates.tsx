import { createFileRoute } from "@tanstack/react-router";
import { updateQuery } from "../features/update/data";
import { UpdatesPage } from "../features/update/UpdatesPage";

// Updating Dock: newer releases, their notes, and the update (ADR 0028).
export const Route = createFileRoute("/_app/console/updates")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(updateQuery()),
  component: UpdatesPage,
});
