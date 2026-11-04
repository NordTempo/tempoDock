import {
  type QueryFunctionContext,
  type QueryKey,
  queryOptions,
  type UseQueryOptions,
  useQuery,
  useQueryClient,
  useSuspenseQueries,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { notFound } from "@tanstack/react-router";
import { useEffect } from "react";
import {
  ApiError,
  api,
  body,
  currentOrg,
  isUnauthorized,
  list,
  noPrompt,
  setOrg,
} from "../api/client";
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
  ServiceBackups,
  ServiceRef,
  ServiceStats,
  ServiceTraffic,
  Session,
  StatsRange,
  TemplateInfo,
  TrafficRange,
} from "./types";

// Query keys are hierarchical so one invalidation covers a subtree:
// ["project", "shop"] refreshes the overview, every environment's variables
// and changes, and every service's releases.
const envKey = (e: EnvRef) => ["project", e.project, "env", e.environment];
const serviceKey = (s: ServiceRef) => [...envKey(s), "service", s.service];

export const keys = {
  session: ["session"],
  orgs: ["orgs"],
  projects: ["projects"],
  project: (project: string) => ["project", project],
  /** Under projects, so project.* audit events refetch it. */
  archivedProjects: ["projects", "archived"],
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
  /** Who can open the service's domains; service.access audits refetch it. */
  access: (s: ServiceRef) => [...serviceKey(s), "access"],
  /** The GitHub source (push-to-deploy); a release building or done refetches it. */
  git: (s: ServiceRef) => [...serviceKey(s), "git"],
  /** Under the service, so backup.* audit events refetch it. */
  backups: (s: ServiceRef) => [...serviceKey(s), "backups"],
  templates: ["templates"],
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
  serviceTraffic: (s: ServiceRef, range: TrafficRange) => [
    "stats",
    "traffic",
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

/** The port a service listens on: its setting, its template's, or 8080. */
export const listenPort = (s: Service) =>
  s.port ||
  (s.template === "postgres" ? 5432 : s.template === "redis" ? 6379 : 8080);

/**
 * Whether a service has the Data tab: a detected database engine, or the
 * database setting naming one (ADR 0016).
 */
export const isDatabase = (s: Service) =>
  !!s.data_engine || (!!s.database && s.database !== "none");

/**
 * Sets archived services apart from live ones and turns the API's null
 * lists into arrays.
 */
function toOverview(
  o: components["schemas"]["ProjectOverview"],
): ProjectOverview {
  return {
    project: o.project,
    environments: (o.environments ?? []).map((env) => {
      const all = (env.services ?? []).map((s) => ({
        ...s,
        domains: s.domains ?? [],
      }));
      return {
        ...env,
        services: all.filter((s) => !s.service.archived_at),
        archived: all.filter((s) => s.service.archived_at),
      };
    }),
  };
}

function toChanges(c: components["schemas"]["PendingChanges"]): PendingChanges {
  return {
    ...c,
    variables: c.variables ?? [],
    settings: c.settings ?? [],
    volumes: c.volumes ?? [],
    services: c.services ?? [],
    domains: c.domains ?? [],
    affected: c.affected ?? [],
  };
}

/**
 * The caller in the organization the UI acts in. A remembered one they
 * no longer belong to is forgotten; a user with several and none chosen
 * acts in their first.
 */
export async function currentMe() {
  const me = await body(api.GET("/api/v1/me")).catch((error: unknown) => {
    if (!(error instanceof ApiError && error.status === 404 && currentOrg())) {
      throw error;
    }
    setOrg(undefined);
    return body(api.GET("/api/v1/me"));
  });
  if (me.org) {
    if (me.kind === "user") setOrg(me.org);
    return me;
  }
  if (me.kind !== "user") return me;
  const [first] = await list(api.GET("/api/v1/orgs"));
  if (!first) return me;
  setOrg(first.slug);
  return body(api.GET("/api/v1/me"));
}

/**
 * Request options for reading secret values. The first read follows a
 * click on Reveal and may ask the person to confirm it's them (ADR 0050);
 * a refetch of values already on screen (a live event) fails with
 * reauth_required instead of prompting out of nowhere.
 */
export const revealRequest = ({ client, queryKey }: QueryFunctionContext) =>
  client.getQueryData(queryKey) === undefined ? {} : { middleware: [noPrompt] };

/**
 * Query options for secret values: in memory only while on screen, and
 * fetched again only when something changed them, never on focus or
 * reconnect. Not retried: a cancelled step-up is an answer.
 */
export const revealed = {
  staleTime: 0,
  gcTime: 0,
  retry: false,
  refetchOnWindowFocus: false,
  refetchOnReconnect: false,
} as const;

// Query options double as route-loader prefetches:
//   loader: ({ context }) => context.queryClient.ensureQueryData(queries.projects())
export const queries = {
  /** The logged-in caller, or null when there is no session. */
  session: () =>
    queryOptions({
      queryKey: keys.session,
      queryFn: () =>
        currentMe().catch((error: unknown) => {
          if (isUnauthorized(error)) return null;
          throw error;
        }),
      staleTime: Number.POSITIVE_INFINITY,
    }),
  /** The caller's organizations, with their role in each. */
  orgs: () =>
    queryOptions({
      queryKey: keys.orgs,
      queryFn: () => list(api.GET("/api/v1/orgs")),
      staleTime: Number.POSITIVE_INFINITY,
    }),
  projects: () =>
    queryOptions({
      queryKey: keys.projects,
      queryFn: () => list(api.GET("/api/v1/projects")),
    }),
  /** Archived projects, last archived first (`dock project list --all`). */
  archivedProjects: () =>
    queryOptions({
      queryKey: keys.archivedProjects,
      queryFn: async () =>
        (
          await list(
            api.GET("/api/v1/projects", {
              params: { query: { archived: true } },
            }),
          )
        )
          .flatMap((p) =>
            p.archived_at ? [{ ...p, archived_at: p.archived_at }] : [],
          )
          .sort((a, b) => b.archived_at.localeCompare(a.archived_at)),
    }),
  overview: (project: string, expectedID?: number) =>
    queryOptions({
      queryKey: keys.overview(project),
      queryFn: () =>
        body(
          api.GET("/api/v1/projects/{project}/overview", {
            params: {
              path: { project },
              header: expectedID
                ? { "Dock-Project-ID": expectedID }
                : undefined,
            },
          }),
        ).then(toOverview),
    }),
  /**
   * The newest releases: 20 everywhere, up to 100 on the Releases tab
   * (`dock release list -n`). Release events patch every length.
   */
  releases: (ref: ServiceRef, limit = 20) =>
    queryOptions({
      queryKey:
        limit === 20 ? keys.releases(ref) : [...keys.releases(ref), limit],
      queryFn: () =>
        list(
          api.GET(
            "/api/v1/projects/{project}/environments/{environment}/services/{service}/releases",
            { params: { path: servicePath(ref), query: { limit } } },
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
  /** A stateful service's backup schedule and latest runs. */
  backups: (ref: ServiceRef) =>
    queryOptions({
      queryKey: keys.backups(ref),
      queryFn: async (): Promise<ServiceBackups> => {
        const b = await body(
          api.GET(
            "/api/v1/projects/{project}/environments/{environment}/services/{service}/backups",
            { params: { path: servicePath(ref) } },
          ),
        );
        return { ...b, runs: b.runs ?? [] };
      },
    }),
  /** The database templates; they only change with a dock release. */
  templates: () =>
    queryOptions({
      queryKey: keys.templates,
      queryFn: async (): Promise<TemplateInfo[]> =>
        (await list(api.GET("/api/v1/templates"))).map((t) => ({
          ...t,
          exports: t.exports ?? [],
        })),
      staleTime: Number.POSITIVE_INFINITY,
    }),
  /** Staged changes; values are only included with reveal (admin). */
  changes: (env: EnvRef, reveal = false) =>
    queryOptions({
      queryKey: keys.changes(env, reveal),
      queryFn: (ctx) =>
        body(
          api.GET(
            "/api/v1/projects/{project}/environments/{environment}/changes",
            {
              params: { path: envPath(env), query: { reveal } },
              ...(reveal && revealRequest(ctx)),
            },
          ),
        ).then(toChanges),
      ...(reveal && revealed),
    }),
  /** Shared variables, plus one service's when `service` is set. */
  variables: (env: EnvRef, service: string | null, reveal = false) =>
    queryOptions({
      queryKey: keys.variables(env, service, reveal),
      queryFn: (ctx) =>
        list(
          api.GET(
            "/api/v1/projects/{project}/environments/{environment}/variables",
            {
              params: {
                path: envPath(env),
                query: { reveal, ...(service && { service }) },
              },
              ...(reveal && revealRequest(ctx)),
            },
          ),
        ),
      ...(reveal && revealed),
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
  serviceTraffic: (ref: ServiceRef, range: TrafficRange) =>
    queryOptions({
      queryKey: keys.serviceTraffic(ref, range),
      queryFn: async (): Promise<ServiceTraffic> => {
        const t = await body(
          api.GET(
            "/api/v1/projects/{project}/environments/{environment}/services/{service}/traffic",
            { params: { path: servicePath(ref), query: { range } } },
          ),
        );
        return { ...t, points: t.points ?? [], paths: t.paths ?? [] };
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
            params: { query: { ...filter, limit: filter.limit ?? 100 } },
          }),
        ),
    }),
};

// Suspense hooks: data is always defined. Route loaders prefetch, so these
// rarely suspend in practice.
export const useSession = () => useSuspenseQuery(queries.session()).data;
export const useIsAdmin = () => useSession()?.scope === "admin";
/** Platform operators run the console: servers, backups, accounts (ADR 0027). */
export const useIsOperator = () => useSession()?.operator === true;
export const useOrgs = () => useSuspenseQuery(queries.orgs()).data;
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

export const useReleases = (ref: ServiceRef, limit?: number) =>
  useSuspenseQuery(queries.releases(ref, limit)).data;
export const useRelease = (ref: ServiceRef, number: number) =>
  useSuspenseQuery(queries.release(ref, number)).data;
export const useVolumes = (ref: ServiceRef) =>
  useSuspenseQuery(queries.volumes(ref)).data;
export const useBackups = (ref: ServiceRef) =>
  useSuspenseQuery(queries.backups(ref)).data;
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

/**
 * A query for secret values while `enabled`. Hiding them drops them from
 * the cache too (a disabled query keeps its data), so values live in
 * memory only while on screen and revealing again is a fresh read, which
 * may ask to confirm (revealRequest).
 */
export function useRevealedQuery<T, K extends QueryKey>(
  options: UseQueryOptions<T, Error, T, K> & { queryKey: K },
  enabled: boolean,
) {
  const queryClient = useQueryClient();
  const { queryKey } = options;
  useEffect(() => {
    if (!enabled) queryClient.removeQueries({ queryKey, exact: true });
  }, [enabled, queryClient, queryKey]);
  return useQuery({ ...options, enabled });
}

/** Variables with values, fetched only while revealed. Needs admin. */
export const useRevealedVariables = (
  env: EnvRef,
  service: string | null,
  enabled: boolean,
) => useRevealedQuery(queries.variables(env, service, true), enabled);

/** Staged changes with old and new values, only while revealed. */
export const useRevealedChanges = (env: EnvRef, enabled: boolean) =>
  useRevealedQuery(queries.changes(env, true), enabled);
