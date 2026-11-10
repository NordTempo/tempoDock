import { createFileRoute } from "@tanstack/react-router";
import { statusQueries } from "../features/status/data";
import { StatusPagesPage } from "../features/status/StatusPagesPage";

// The organization's public status pages (ADR 0073), for every member;
// admins make and change them.
export const Route = createFileRoute("/_app/$org/status-pages/")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(statusQueries.list()),
  component: StatusPagesPage,
});
