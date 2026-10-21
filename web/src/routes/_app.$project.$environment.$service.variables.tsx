import { createFileRoute, redirect } from "@tanstack/react-router";

// Variables live on the environment page, which shows what each service
// sees at runtime; a service's Variables tab opens it on that service.
export const Route = createFileRoute(
  "/_app/$project/$environment/$service/variables",
)({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/$project/$environment",
      params: { project: params.project, environment: params.environment },
      search: { service: params.service },
      replace: true,
    });
  },
});
