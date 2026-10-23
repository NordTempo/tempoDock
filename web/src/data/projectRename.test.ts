// Open /tests/project-rename.html under Vite. Uses a local fetch fixture only.
import { QueryClient } from "@tanstack/react-query";
import type { Project, Session } from "./types";

/** Fails both the browser title and console for automation. */
function check(ok: boolean, message: string) {
  if (!ok) throw new Error(message);
}
window.addEventListener("unhandledrejection", (event) => {
  document.title = `FAIL: ${String(event.reason)}`;
});
const originalFetch = window.fetch;
const originalStorage = {
  org: localStorage.getItem("dock.org"),
  project: localStorage.getItem("dock.project"),
};
const project: Project = {
  id: 1,
  name: "shop",
  created_at: "2026-10-23T10:00:00Z",
};
let remote = project;
let requests = 0;
window.fetch = async (input) => {
  requests++;
  const url = new URL(
    input instanceof Request ? input.url : String(input),
    location.origin,
  );
  if (url.pathname === "/api/v1/projects") return Response.json([remote]);
  if (url.pathname === "/api/v1/project-ids/1") return Response.json(remote);
  if (url.pathname === `/api/v1/projects/${remote.name}/overview`)
    return Response.json({ project: remote, environments: [] });
  return Response.json(
    { status: 404, code: "not_found", detail: "Project not found" },
    { status: 404 },
  );
};
// openapi-fetch captures fetch at construction, so import it after the fixture.
const [
  { setOrg },
  { lastProject, rememberProject },
  { followProjectRename, projectQuery, reconcileProjectNames },
  { keys },
] = await Promise.all([
  import("../api/client"),
  import("../shell/location"),
  import("./projectRename"),
  import("./queries"),
]);
const client = new QueryClient({
  defaultOptions: { queries: { retry: false, gcTime: Infinity } },
});
const locationState = {
  pathname: "/test/shop/production/web",
  searchStr: "?view=logs",
  hash: "tail",
};
let navigations = 0;
let destination = "";
const router: Parameters<typeof followProjectRename>[1] = {
  state: { location: locationState },
  navigate: async (options) => {
    check(options.replace === true, "Rename must replace history");
    destination = options.href ?? "";
    const url = new URL(destination, location.origin);
    locationState.pathname = url.pathname;
    navigations++;
  },
};
try {
  setOrg("test");
  rememberProject("shop");
  client.setQueryData(keys.projects, [project]);
  client.setQueryData<Session>(keys.session, {
    kind: "token",
    name: "scoped",
    scope: "admin",
    project: "shop",
    org: "test",
    operator: false,
  });
  const oldKeys = [
    keys.overview("shop"),
    ["data", "shop", "production", "web", "history"],
    ["stats", "service", "shop", "production", "web"],
    ["audit", { project: "shop" }],
  ];
  for (const key of oldKeys) {
    check(
      projectQuery(key, "shop"),
      "Rename must include each project cache tree",
    );
    client.setQueryData(key, {});
  }
  remote = { ...project, name: "storefront" };
  const event = { previous_name: "shop", project: remote };
  await Promise.all([
    followProjectRename(client, router, "test", event),
    followProjectRename(client, router, "test", event),
  ]);
  check(
    destination === "/test/storefront/production/web?view=logs#tail",
    "Nested URL, search and hash must follow the rename",
  );
  check(
    navigations === 1 && requests === 1,
    "SSE and mutation response must be idempotent",
  );
  check(
    client.getQueryData<Session>(keys.session)?.project === "storefront",
    "Scoped session label must follow",
  );
  check(lastProject() === "storefront", "Remembered project must follow");
  for (const key of oldKeys)
    check(
      client.getQueryData(key) === undefined,
      "Old-name data must not remain cached",
    );
  check(
    client.getQueryData<Project[]>(keys.projects)?.[0]?.id === 1,
    "Identity must stay stable",
  );

  remote = { ...project, name: "final" };
  await reconcileProjectNames(client, router, "test");
  check(
    locationState.pathname === "/test/final/production/web",
    "Missed event must recover by ID",
  );
  await followProjectRename(client, router, "test", event);
  check(
    locationState.pathname === "/test/final/production/web",
    "Late response must not undo a newer rename",
  );

  // The intermediate name no longer exists by the time its overview loads.
  remote = { ...project, name: "latest" };
  await followProjectRename(client, router, "test", {
    previous_name: "final",
    project: { ...project, name: "intermediate" },
  });
  check(
    locationState.pathname === "/test/latest/production/web",
    "Overtaken rename must resolve its current name by ID",
  );
  setOrg("other");
  const before = requests;
  await followProjectRename(client, router, "test", {
    previous_name: "latest",
    project: { ...project, name: "ignored" },
  });
  check(
    requests === before && locationState.pathname.includes("/latest/"),
    "Old org events must be ignored",
  );
  document.title = "PASS: project rename";
  const results = document.getElementById("results");
  if (results)
    results.textContent =
      "PASS: nested navigation, duplicate events, cache cleanup, scoped session, reconnect, rapid renames and org isolation";
  console.log(document.title);
} finally {
  client.clear();
  window.fetch = originalFetch;
  setOrg(originalStorage.org ?? undefined);
  if (originalStorage.project === null) localStorage.removeItem("dock.project");
  else rememberProject(originalStorage.project);
}
