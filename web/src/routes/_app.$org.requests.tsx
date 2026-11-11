import { createFileRoute, redirect } from "@tanstack/react-router";

// The old access requests page: the Inbox's first section now
// (ADR 0076); ?id= opens the same request there.
export const Route = createFileRoute("/_app/$org/requests")({
  beforeLoad: ({ params, search }) => {
    const id = Number((search as { id?: unknown }).id);
    throw redirect({
      to: "/$org/inbox",
      params,
      search: Number.isInteger(id) && id > 0 ? { request: id } : {},
      replace: true,
    });
  },
});
