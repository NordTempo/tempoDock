import { createFileRoute, redirect } from "@tanstack/react-router";

// An environment's address: the project page, scrolled to the
// environment's section (its services, actions and lock). Its shared
// variables and log drains are in the project's settings (ADR 0059), and
// ?service=web, what one service sees, is the service's Variables tab.
export const Route = createFileRoute("/_app/$org/$project/$environment/")({
  validateSearch: (search: Record<string, unknown>): { service?: string } =>
    typeof search.service === "string" && search.service
      ? { service: search.service }
      : {},
  beforeLoad: ({ params, search }) => {
    throw search.service
      ? redirect({
          to: "/$org/$project/$environment/$service/variables",
          params: { ...params, service: search.service },
          replace: true,
        })
      : redirect({
          to: "/$org/$project",
          params: { org: params.org, project: params.project },
          hash: `env-${params.environment}`,
          replace: true,
        });
  },
});
