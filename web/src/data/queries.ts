import {
  queryOptions,
  useQuery,
  useSuspenseQueries,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { notFound } from "@tanstack/react-router";
import { api, body, isUnauthorized, list } from "../api/client";
import type { components } from "../api/schema";
import type {
  AuditFilter,
  EnvRef,
  PendingChanges,
  ProjectOverview,
  Server,
  ServerDetail,
  ServerStats,
  Service,
  ServiceRef,
  ServiceStats,
  Session,
  StatsRange,
} from "./types";

// Query keys are hierarchical so one invalidation covers a subtree:
// ["project", "shop"] refreshes the overview, every environment's variables
// and changes, and every service's releases.
const envKey = (e: EnvRef) => ["project", e.project, "env", e.environment];
const serviceKey = (s: ServiceRef) => [...envKey(s), "service", s.service];

export const keys = {
  session: ["session"],
  projects: ["projects"],
  project: (project: string) => ["project", project],
  overview: (project: string) => ["project", project, "overview"],
  env: envKey,
  changes: (e: EnvRef, reveal = false) => [...envKey(e), "changes", reveal],
  variables: (e: EnvRef, service: string | null, reveal = false) => [
    ...envKey(e),
    "variables",
    service,
    reveal,
  ],
  service: serviceKey,
  releases: (s: ServiceRef) => [...serviceKey(s), "releases"],
  release: (s: ServiceRef, number: number) => [
    ...serviceKey(s),
    "release",
    number,
  ],
  volumes: (s: ServiceRef) => [...serviceKey(s), "volumes"],
  /** Covers every server's detail too. */
  servers: ["servers"],
  server: (name: string) => ["servers", name],
  /** Under the project, so audit events about it refetch them. */
  placements: (project: string) => ["project", project, "placements"],
  // Statistics stay outside the project and server trees: events there
  // shouldn't refetch history.
  serverStats: (name: string, range: StatsRange) => [
    "stats",
    "server",
    name,
    range,
  ],
  serviceStats: (s: ServiceRef, range: StatsRange) => [
    "stats",
    "service",
    s.project,
    s.environment,
    s.service,
    range,
  ],
  // The database panel stays outside the project tree too: the data.*
  // audit events its own reads cause mustn't refetch table pages. Live
  // events refresh its parts selectively (./live.ts).
  data: (s: ServiceRef) => ["data", s.project, s.environment, s.service],
  /** Every scope's data access policy. */
  dataPolicy: ["data-policy"],
  tokens: ["tokens"],
  users: ["users"],
  audit: (filter: AuditFilter) => ["audit", filter],
} as const;

/** API path params; refs may carry extra fields (e.g. route params). */
export const envPath = (e: EnvRef) => ({
  project: e.project,
  environment: e.environment,
});
export const servicePath = (s: ServiceRef) => ({
  ...envPath(s),
  service: s.service,
});

/** The ref (and route params) of a service. */
export const serviceRef = (s: Service): ServiceRef => ({
  project: s.project,
  environment: s.environment,
  service: s.name,
});

/**
 * Whether a service has the Data tab: a detected database engine, or the
 * database setting naming one (ADR 0016).
 */
export const isDatabase = (s: Service) =>
  !!s.data_engine || (!!s.database && s.database !== "none");

/** Drops archived services and turns the API's null lists into arrays. */
function toOverview(
  o: components["schemas"]["ProjectOverview"],
): ProjectOverview {
  return {
    project: o.project,
    environments: (o.environments ?? []).map((env) => ({
      ...env,
      services: (env.services ?? [])
        .filter((s) => !s.service.archived_at)
        .map((s) => ({ ...s, domains: s.domains ?? [] })),
    })),
  };
}

function toChanges(c: components["schemas"]["PendingChanges"]): PendingChanges {
  return {
    ...c,
    variables: c.variables ?? [],
    settings: c.settings ?? [],
    volumes: c.volumes ?? [],
    affected: c.affected ?? [],
  };
}

// Query options double as route-loader prefetches:
//   loader: ({ context }) => context.queryClient.ensureQueryData(queries.projects())
export const queries = {
  /** The logged-in caller, or null when there is no session. */
  session: () =>
    queryOptions({
      queryKey: keys.session,
      queryFn: () =>
        body(api.GET("/api/v1/me")).catch((error: unknown) => {
          if (isUnauthorized(error)) return null;
          throw error;
        }),
      staleTime: Number.POSITIVE_INFINITY,
    }),
  projects: () =>
    queryOptions({
      queryKey: keys.projects,
      queryFn: () => list(api.GET("/api/v1/projects")),
    }),
  overview: (project: string) =>
    queryOptions({
      queryKey: keys.overview(project),
      queryFn: () =>
        body(
          api.GET("/api/v1/projects/{project}/overview", {
            params: { path: { project } },
          }),
        ).then(toOverview),
    }),
  releases: (ref: ServiceRef) =>
    queryOptions({
      queryKey: keys.releases(ref),
      queryFn: () =>
        list(
          api.GET(
            "/api/v1/projects/{project}/environments/{environment}/services/{service}/releases",
            { params: { path: servicePath(ref), query: { limit: 20 } } },
          ),
        ),
    }),
  /** One release with its progress events; release events append to it. */
  release: (ref: ServiceRef, number: number) =>
    queryOptions({
      queryKey: keys.release(ref, number),
      queryFn: () =>
        body(
          api.GET(
            "/api/v1/projects/{project}/environments/{environment}/services/{service}/releases/{number}",
            { params: { path: { ...servicePath(ref), number } } },
          ),
        ),
    }),
  volumes: (ref: ServiceRef) =>
    queryOptions({
      queryKey: keys.volumes(ref),
      queryFn: () =>
        list(
          api.GET(
            "/api/v1/projects/{project}/environments/{environment}/services/{service}/volumes",
            { params: { path: servicePath(ref) } },
          ),
        ),
    }),
  /** Staged changes; values are only included with reveal (admin). */
  changes: (env: EnvRef, reveal = false) =>
    queryOptions({
      queryKey: keys.changes(env, reveal),
      queryFn: () =>
        body(
          api.GET(
            "/api/v1/projects/{project}/environments/{environment}/changes",
            { params: { path: envPath(env), query: { reveal } } },
          ),
        ).then(toChanges),
      ...(reveal && { staleTime: 0, gcTime: 0 }),
    }),
  /** Shared variables, plus one service's when `service` is set. */
  variables: (env: EnvRef, service: string | null, reveal = false) =>
    queryOptions({
      queryKey: keys.variables(env, service, reveal),
      queryFn: () =>
        list(
          api.GET(
            "/api/v1/projects/{project}/environments/{environment}/variables",
            {
              params: {
                path: envPath(env),
                query: { reveal, ...(service && { service }) },
              },
            },
          ),
        ),
      // Secret values live in memory only while they are on screen.
      ...(reveal && { staleTime: 0, gcTime: 0 }),
    }),
  servers: () =>
    queryOptions({
      queryKey: keys.servers,
      queryFn: (): Promise<Server[]> => list(api.GET("/api/v1/servers")),
    }),
  /** One server, what is placed on it and its services. Admin only. */
  server: (name: string) =>
    queryOptions({
      queryKey: keys.server(name),
      queryFn: async (): Promise<ServerDetail> => {
        const d = await body(
          api.GET("/api/v1/servers/{server}", {
            params: { path: { server: name } },
          }),
        );
        return {
          ...d,
          placements: d.placements ?? [],
          services: d.services ?? [],
        };
      },
    }),
  /** Where the project's environments of each kind live, once placed. */
  placements: (project: string) =>
    queryOptions({
      queryKey: keys.placements(project),
      queryFn: () =>
        list(
          api.GET("/api/v1/projects/{project}/placements", {
            params: { path: { project } },
          }),
        ),
    }),
  // History has a point a minute at most, so it stays fresh for a minute;
  // usage events append new 1-minute points to open charts (./live.ts).
  serverStats: (name: string, range: StatsRange) =>
    queryOptions({
      queryKey: keys.serverStats(name, range),
      queryFn: async (): Promise<ServerStats> => {
        const s = await body(
          api.GET("/api/v1/servers/{server}/stats", {
            params: { path: { server: name }, query: { range } },
          }),
        );
        return { ...s, points: s.points ?? [] };
      },
      staleTime: 60_000,
    }),
  serviceStats: (ref: ServiceRef, range: StatsRange) =>
    queryOptions({
      queryKey: keys.serviceStats(ref, range),
      queryFn: async (): Promise<ServiceStats> => {
        const s = await body(
          api.GET(
            "/api/v1/projects/{project}/environments/{environment}/services/{service}/stats",
            { params: { path: servicePath(ref), query: { range } } },
          ),
        );
        return { ...s, points: s.points ?? [] };
      },
      staleTime: 60_000,
    }),
  tokens: () =>
    queryOptions({
      queryKey: keys.tokens,
      queryFn: () => list(api.GET("/api/v1/tokens")),
    }),
  users: () =>
    queryOptions({
      queryKey: keys.users,
      queryFn: () => list(api.GET("/api/v1/users")),
    }),
  audit: (filter: AuditFilter = {}) =>
    queryOptions({
      queryKey: keys.audit(filter),
      queryFn: () =>
        list(
          api.GET("/api/v1/audit", {
            params: { query: { ...filter, limit: 100 } },
          }),
        ),
    }),
};

// Suspense hooks: data is always defined. Route loaders prefetch, so these
// rarely suspend in practice.
export const useSession = () => useSuspenseQuery(queries.session()).data;
export const useIsAdmin = () => useSession()?.scope === "admin";
export const useProjects = () => useSuspenseQuery(queries.projects()).data;
export const useProjectOverview = (project: string) =>
  useSuspenseQuery(queries.overview(project)).data;

/** Every project's overview: the sidebar tree and the command palette. */
export function useProjectTree() {
  const projects = useProjects();
  return useSuspenseQueries({
    queries: projects.map((p) => queries.overview(p.name)),
    combine: (results) => results.map((r) => r.data),
  });
}

/** One service with its live state, from its project's overview. */
export function useService(ref: ServiceRef) {
  const service = useSuspenseQuery({
    ...queries.overview(ref.project),
    select: (o) => findService(o, ref),
  }).data;
  if (!service) throw notFound();
  return service;
}

/** One environment with its services, from its project's overview. */
export function useEnvironment(env: EnvRef) {
  const environment = useSuspenseQuery({
    ...queries.overview(env.project),
    select: (o) =>
      o.environments.find((e) => e.environment.name === env.environment),
  }).data;
  if (!environment) throw notFound();
  return environment;
}

export function findService(overview: ProjectOverview, ref: ServiceRef) {
  return overview.environments
    .find((e) => e.environment.name === ref.environment)
    ?.services.find((s) => s.service.name === ref.service);
}

export const useReleases = (ref: ServiceRef) =>
  useSuspenseQuery(queries.releases(ref)).data;
export const useRelease = (ref: ServiceRef, number: number) =>
  useSuspenseQuery(queries.release(ref, number)).data;
export const useVolumes = (ref: ServiceRef) =>
  useSuspenseQuery(queries.volumes(ref)).data;
export const useChanges = (env: EnvRef) =>
  useSuspenseQuery(queries.changes(env)).data;
export const useVariables = (env: EnvRef, service: string | null) =>
  useSuspenseQuery(queries.variables(env, service)).data;
export const useServers = () => useSuspenseQuery(queries.servers()).data;
export const useServer = (name: string) =>
  useSuspenseQuery(queries.server(name)).data;

/**
 * Why nothing can be deployed to a server right now, as deploy buttons
 * say it: "server homelab is offline". Undefined when it can.
 */
export function deployBlock(server: Server | undefined) {
  switch (server?.status) {
    case "offline":
      return `server ${server.name} is offline`;
    case "incompatible":
      return `server ${server.name} runs an incompatible dock version`;
    case "joining":
      return `server ${server.name} hasn't connected yet`;
    case "revoked":
      return `server ${server.name} is revoked`;
    default:
      return undefined;
  }
}

/** deployBlock for the server an environment runs on. */
export function useDeployBlock(env: EnvRef) {
  const { environment } = useEnvironment(env);
  return deployBlock(useServers().find((s) => s.name === environment.server));
}

/**
 * Whether the caller may deploy to an environment: scope deploy or admin,
 * and not limited to another project or environment.
 */
export function canDeploy(session: Session | null, env: EnvRef) {
  if (!session || session.scope === "read") return false;
  if (session.project && session.project !== env.project) return false;
  return !session.environment || session.environment === env.environment;
}

export const useCanDeploy = (env: EnvRef) => canDeploy(useSession(), env);
export const usePlacements = (project: string) =>
  useSuspenseQuery(queries.placements(project)).data;
export const useTokens = () => useSuspenseQuery(queries.tokens()).data;
export const useUsers = () => useSuspenseQuery(queries.users()).data;
export const useAuditLog = (filter: AuditFilter = {}) =>
  useSuspenseQuery(queries.audit(filter)).data;

/** Variables with values, fetched only while revealed. Needs admin. */
export const useRevealedVariables = (
  env: EnvRef,
  service: string | null,
  enabled: boolean,
) => useQuery({ ...queries.variables(env, service, true), enabled });

/** Staged changes with old and new values, only while revealed. */
export const useRevealedChanges = (env: EnvRef, enabled: boolean) =>
  useQuery({ ...queries.changes(env, true), enabled });
