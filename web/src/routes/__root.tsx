import type { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, Outlet } from "@tanstack/react-router";

// Router context: loaders prefetch into the shared query cache with
// context.queryClient.ensureQueryData(...).
export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()(
  {
    component: Outlet,
    notFoundComponent: () => <p className="p-4">Not found</p>,
    errorComponent: ({ error }) => (
      <p className="p-4">
        {error instanceof Error ? error.message : "Something went wrong"}
      </p>
    ),
  },
);
