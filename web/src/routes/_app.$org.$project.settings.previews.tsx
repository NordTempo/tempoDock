import { createFileRoute, redirect } from "@tanstack/react-router";

// Previews were a tab of a project's settings; they are a section of its
// Settings page now.
export const Route = createFileRoute("/_app/$org/$project/settings/previews")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/$org/$project/settings",
      params,
      hash: "previews",
      replace: true,
    });
  },
});
