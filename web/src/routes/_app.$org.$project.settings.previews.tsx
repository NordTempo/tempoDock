import { createFileRoute, redirect } from "@tanstack/react-router";

// Previews were a tab of a project's settings; they are a row of its
// Project settings now (ADR 0099).
export const Route = createFileRoute("/_app/$org/$project/settings/previews")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/$org/$project/project-settings",
      params,
      hash: "previews",
      replace: true,
    });
  },
});
