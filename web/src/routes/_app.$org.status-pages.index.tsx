import { createFileRoute, redirect } from "@tanstack/react-router";

// The older address of Status (renamed from Status pages, 2026-11-28):
// bookmarks and links keep working.
export const Route = createFileRoute("/_app/$org/status-pages/")({
  beforeLoad: ({ params }) => {
    throw redirect({ to: "/$org/status", params, replace: true });
  },
});
