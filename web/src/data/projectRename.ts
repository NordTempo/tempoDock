import type { QueryClient, QueryKey } from "@tanstack/react-query";
import type { RegisteredRouter } from "@tanstack/react-router";
import { ApiError, api, body, currentOrg } from "../api/client";
import type { components } from "../api/schema";
import { lastProject, rememberProject } from "../shell/location";
import { renameFeedProject } from "./feed";
import { keys, queries } from "./queries";
import type { Project, Session } from "./types";

type Renamed = components["schemas"]["ProjectRenamed"];
const pending = new WeakMap<QueryClient, Promise<void>>();
type RenameRouter = {
  state: {
    location: Pick<
      RegisteredRouter["state"]["location"],
      "pathname" | "searchStr" | "hash"
    >;
  };
  navigate: RegisteredRouter["navigate"];
};

/** Includes the independent stats, Data panel and audit cache trees. */
export function projectQuery(key: QueryKey, name: string) {
  if ((key[0] === "project" || key[0] === "data") && key[1] === name) {
    return true;
  }
  if (key[0] === "stats" && key[1] === "service" && key[2] === name) {
    return true;
  }
  const filter = key[1];
  return (
    key[0] === "audit" &&
    typeof filter === "object" &&
    filter !== null &&
    "project" in filter &&
    filter.project === name
  );
}

/** Serializes rename events and responses, including successive renames. */
export function followProjectRename(
  client: QueryClient,
  router: RenameRouter,
  org: string,
  renamed: Renamed,
) {
  const next = (pending.get(client) ?? Promise.resolve()).then(async () => {
    if (currentOrg() !== org) return;
    const { previous_name: previous } = renamed;
    let { project } = renamed;
    if (previous === project.name) return;
    const cached = client
      .getQueryData<Project[]>(keys.projects)
      ?.find((p) => p.id === project.id);
    // A response for an earlier rename can arrive after its successor.
    if (cached && cached.name !== previous && cached.name !== project.name) {
      return;
    }
    const path = router.state.location.pathname.split("/");
    if (
      cached?.name === project.name &&
      !(path[1] === org && path[2] === previous) &&
      !client
        .getQueryCache()
        .findAll({ predicate: (q) => projectQuery(q.queryKey, previous) })
        .length
    )
      return;
    await client.cancelQueries({
      predicate: (q) => projectQuery(q.queryKey, previous),
    });
    // The sidebar suspends on every project's overview. Load the new one
    // before moving the list, just as project creation does.
    const overview = await client
      .ensureQueryData(queries.overview(project.name, project.id))
      .catch((error: unknown) => {
        if (
          !(error instanceof ApiError) ||
          ![404, 409].includes(error.status) ||
          currentOrg() !== org
        )
          throw error;
        return undefined;
      });
    if (!overview || overview.project.id !== project.id) {
      // Another rename can overtake this fetch, or its name may be reused.
      // Resolve the identity once and guard the next name-based request too.
      project = await body(
        api.GET("/api/v1/project-ids/{id}", {
          params: { path: { id: project.id } },
        }),
      );
      if (currentOrg() !== org) return;
      await client.fetchQuery({
        ...queries.overview(project.name, project.id),
        staleTime: 0,
      });
    }
    if (currentOrg() !== org) return;
    client.setQueriesData<Project[]>({ queryKey: keys.projects }, (list) =>
      list
        ?.map((p) => (p.id === project.id ? project : p))
        .sort((a, b) => a.name.localeCompare(b.name)),
    );
    client.setQueryData<Session | null>(keys.session, (session) =>
      session?.project === previous
        ? { ...session, project: project.name }
        : session,
    );
    renameFeedProject(previous, project.name);
    if (lastProject() === previous) rememberProject(project.name);
    const location = router.state.location;
    const route = location.pathname.split("/");
    if (route[1] === org && route[2] === previous) {
      route[2] = project.name;
      await router.navigate({
        href: `${route.join("/")}${location.searchStr}${location.hash ? `#${location.hash}` : ""}`,
        replace: true,
      });
    }
    if (currentOrg() !== org) return;
    client.removeQueries({
      predicate: (q) => projectQuery(q.queryKey, previous),
    });
    // These cross-project views contain copied project names too.
    await Promise.all(
      [keys.tokens, keys.servers, ["audit"]].map((queryKey) =>
        client.invalidateQueries({ queryKey }),
      ),
    );
  });
  pending.set(
    client,
    next.catch(() => {}),
  );
  return next;
}

/** Recovers missed renames from stable IDs, never from an old-name alias. */
export async function reconcileProjectNames(
  client: QueryClient,
  router: RenameRouter,
  org: string,
) {
  if (currentOrg() !== org) return;
  const previous = client.getQueryData<Project[]>(keys.projects) ?? [];
  const current = await client.fetchQuery({
    ...queries.projects(),
    staleTime: 0,
  });
  if (currentOrg() !== org) return;
  for (const old of previous) {
    const project = current.find((p) => p.id === old.id);
    if (project && project.name !== old.name) {
      await followProjectRename(client, router, org, {
        previous_name: old.name,
        project,
      });
    }
  }
}
