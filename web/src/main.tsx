import {
  MutationCache,
  QueryCache,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { createRouter, RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { isUnauthorized } from "./api/client";
import { signedOut } from "./data/mutations";
import { routeTree } from "./routeTree.gen";
import { NotFound, RouteError } from "./ui/RouteStates";
import "./index.css";

// A 401 anywhere means the session is gone: log in again, then come back.
function onError(error: unknown) {
  const { pathname, href } = router.state.location;
  if (isUnauthorized(error) && pathname !== "/login") {
    signedOut(queryClient, router, href);
  }
}

const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError }),
  mutationCache: new MutationCache({ onError }),
  defaultOptions: {
    // Live events keep the cache current, so focus refetches are noise.
    queries: { staleTime: 30_000, refetchOnWindowFocus: false },
  },
});

const router = createRouter({
  routeTree,
  context: { queryClient },
  // Loaders read from the query cache, which has its own staleness rules.
  defaultPreloadStaleTime: 0,
  defaultPreload: "intent",
  // Route errors render in place, so the sidebar stays usable.
  defaultErrorComponent: RouteError,
  defaultNotFoundComponent: NotFound,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

const root = document.getElementById("root");
if (!root) throw new Error("#root is missing from index.html");

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
