import type { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, Outlet } from "@tanstack/react-router";
import { NotFound, RouteError } from "../ui/RouteStates";

// Router context: loaders prefetch into the shared query cache with
// context.queryClient.ensureQueryData(...).
export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()(
  {
    component: Outlet,
    notFoundComponent: NotFound,
    errorComponent: RouteError,
  },
);
