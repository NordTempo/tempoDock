import {
  MutationCache,
  QueryCache,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { createRouter, RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { isUnauthorized, loadBackend } from "./api/backend";
import { keys } from "./data/queries";
import { routeTree } from "./routeTree.gen";
import { NotFound, RouteError } from "./shell/states";
import "./tempobase.css";

// tempoBase's UI (ADR 0024 section 4): Dock's stack, style G, its own shell.

// A 401 anywhere means the session is gone: sign in again, then come back.
function onError(error: unknown) {
  const { pathname, href } = router.state.location;
  if (!isUnauthorized(error) || pathname === "/login") return;
  queryClient.clear();
  queryClient.setQueryData(keys.me, null);
  router.navigate({ to: "/login", search: { redirect: href } });
}

const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError }),
  mutationCache: new MutationCache({ onError }),
  defaultOptions: {
    // Nothing changes behind a person's back often enough to refetch on
    // focus; the changeset list opts in (the connection route).
    queries: { staleTime: 30_000, refetchOnWindowFocus: false, retry: 1 },
  },
});

const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultPreloadStaleTime: 0,
  defaultPreload: "intent",
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

loadBackend().then(() =>
  createRoot(root).render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </StrictMode>,
  ),
);
