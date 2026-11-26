import { createFileRoute, redirect } from "@tanstack/react-router";

// Previews were a tab of a project's settings, then a row of its Project
// settings (ADR 0099); they are set on each service now, in its Settings ›
// Source (ADR 0077). Old links land on Project settings.
export const Route = createFileRoute("/_app/$org/$project/settings/previews")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/$org/$project/project-settings",
      params,
      replace: true,
    });
  },
});
