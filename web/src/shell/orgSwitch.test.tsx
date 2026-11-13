// Open /tests/org-switch.html under Vite. Uses a local fetch fixture only.
// Switching organizations while a link to the old one is hovered (its
// preload runs the org route's beforeLoad): the switch must finish in the
// new org, with no page failing on a cancelled query. And a page whose
// query is cancelled anyway loads again instead of saying it didn't load.
import {
  CancelledError,
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { createRoot } from "react-dom/client";
import type { Org, Project, Session } from "../data/types";

/** Fails both the browser title and console for automation. */
function check(ok: boolean, message: string) {
  if (!ok) throw new Error(message);
}
window.addEventListener("unhandledrejection", (event) => {
  document.title = `FAIL: ${String(event.reason)}`;
});
const originalFetch = window.fetch;
const originalOrg = localStorage.getItem("dock.org");

const orgs: Org[] = ["a", "b"].map((slug) => ({
  slug,
  name: slug.toUpperCase(),
  role: "admin",
  require_two_factor: false,
  created_at: "2026-11-13T10:00:00Z",
}));
const sleep = (ms: number) => new Promise((done) => setTimeout(done, ms));
// The org of every request for projects, in order.
const asked: string[] = [];
window.fetch = async (input) => {
  const request = input instanceof Request ? input : new Request(input);
  const org = request.headers.get("Dock-Org") ?? "";
  const path = new URL(request.url).pathname;
  if (path === "/api/v1/me") {
    const me: Session = { kind: "user", name: "saile", operator: false, org };
    return Response.json(me);
  }
  if (path === "/api/v1/orgs") return Response.json(orgs);
  if (path === "/api/v1/projects") {
    asked.push(org);
    // Slow, so the other org's preload lands while it is in flight.
    await sleep(200);
    const projects: Project[] = [
      { id: 1, name: `${org}-app`, created_at: "2026-11-13T10:00:00Z" },
    ];
    return Response.json(projects);
  }
  if (path.endsWith("/overview")) return Response.json({ environments: [] });
  return Response.json([]);
};
// openapi-fetch captures fetch at construction, so import it after the fixture.
const [{ currentOrg, setOrg }, { routeTree }, { RouteError }] =
  await Promise.all([
    import("../api/client"),
    import("../routeTree.gen"),
    import("../ui/RouteStates"),
  ]);
const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false } },
});
const router = createRouter({
  routeTree,
  context: { queryClient },
  history: createMemoryHistory({ initialEntries: ["/a"] }),
  defaultPreloadStaleTime: 0,
});
try {
  setOrg("a");
  await router.load();
  check(currentOrg() === "a", "The first page must act in org a");

  // Switch to b; while b's projects load, a link to a is hovered.
  const switched = router.navigate({ to: "/$org", params: { org: "b" } });
  await sleep(50);
  const hovered = router.preloadRoute({ to: "/$org", params: { org: "a" } });
  await Promise.all([switched, hovered]);
  await sleep(300);

  const failed = router.state.matches.filter((m) => m.status === "error");
  check(
    failed.length === 0,
    `No page may fail after the switch: ${failed.map((m) => `${m.routeId}: ${String(m.error)}`).join(", ")}`,
  );
  check(currentOrg() === "b", `The UI must act in org b, not ${currentOrg()}`);
  check(
    !asked.slice(1).includes("a"),
    `A preload must not load org a's data after the switch: ${asked.join(", ")}`,
  );
  const projects = queryClient.getQueryData<Project[]>(["projects"]);
  check(projects?.[0]?.name === "b-app", "The cache must hold b's projects");

  // A loader whose query is cancelled once: the page shows, not the error.
  let loads = 0;
  const root = createRootRoute({ errorComponent: RouteError });
  const page = createRoute({
    getParentRoute: () => root,
    path: "/",
    loader: () => {
      if (++loads === 1) throw new CancelledError();
    },
    component: () => <p id="loaded">loaded</p>,
  });
  const host = document.createElement("div");
  document.body.append(host);
  createRoot(host).render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider
        router={createRouter({
          routeTree: root.addChildren([page]),
          history: createMemoryHistory(),
        })}
      />
    </QueryClientProvider>,
  );
  for (let i = 0; i < 50 && !host.querySelector("#loaded"); i++)
    await sleep(20);
  check(
    !!host.querySelector("#loaded") && loads === 2,
    `A cancelled load must load again: ${loads} loads, page "${host.textContent}"`,
  );

  document.title = "PASS: org switch";
  const results = document.getElementById("results");
  if (results)
    results.textContent =
      "PASS: a hovered link to the old org neither flips the org nor fails the new one's page; a cancelled load loads again";
  console.log(document.title);
} finally {
  queryClient.clear();
  window.fetch = originalFetch;
  setOrg(originalOrg ?? undefined);
}
