import { createFileRoute } from "@tanstack/react-router";
import { statusQueries } from "../features/status/data";
import { StatusPagesPage } from "../features/status/StatusPagesPage";

// Status: the organization's public status pages (ADR 0073) and its
// checks of sites outside Dock (ADR 0055), for every member; admins make
// and change them.
export const Route = createFileRoute("/_app/$org/status/")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(statusQueries.list()),
      queryClient.ensureQueryData(statusQueries.outside()),
    ]),
  component: StatusPagesPage,
});
