import {
  queryOptions,
  useQuery,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { api } from "./fixtures";
import type { AuditFilter, VariableTarget } from "./types";

// Query keys are hierarchical so one invalidation covers a whole subtree:
// ["services", id] refreshes the service and its releases, logs, domains...
export const keys = {
  session: ["session"],
  org: ["org"],
  projects: ["projects"],
  overview: (projectId: string) => ["projects", projectId, "overview"],
  service: (serviceId: string) => ["services", serviceId],
  releases: (serviceId: string) => ["services", serviceId, "releases"],
  logs: (serviceId: string) => ["services", serviceId, "logs"],
  domains: (serviceId: string) => ["services", serviceId, "domains"],
  variables: (t: VariableTarget) => [
    "environments",
    t.environmentId,
    "variables",
    t.serviceId,
  ],
  secrets: (t: VariableTarget) => [
    "environments",
    t.environmentId,
    "secrets",
    t.serviceId,
  ],
  pending: (environmentId: string) => [
    "environments",
    environmentId,
    "pending",
  ],
  servers: ["servers"],
  tokens: ["tokens"],
  users: ["users"],
  audit: (filter: AuditFilter) => ["audit", filter],
} as const;

// Query options double as route-loader prefetches:
//   loader: ({ context }) => context.queryClient.ensureQueryData(queries.projects())
// To move to the real API, swap each queryFn for a fetchClient call that maps
// the response onto the view-model type. Keys and hooks stay the same.
export const queries = {
  session: () => queryOptions({ queryKey: keys.session, queryFn: api.session }),
  org: () => queryOptions({ queryKey: keys.org, queryFn: api.org }),
  projects: () =>
    queryOptions({ queryKey: keys.projects, queryFn: api.projects }),
  overview: (projectId: string) =>
    queryOptions({
      queryKey: keys.overview(projectId),
      queryFn: () => api.projectOverview(projectId),
    }),
  service: (serviceId: string) =>
    queryOptions({
      queryKey: keys.service(serviceId),
      queryFn: () => api.service(serviceId),
    }),
  releases: (serviceId: string) =>
    queryOptions({
      queryKey: keys.releases(serviceId),
      queryFn: () => api.releases(serviceId),
    }),
  logs: (serviceId: string) =>
    queryOptions({
      queryKey: keys.logs(serviceId),
      queryFn: () => api.logs(serviceId),
      // Kept current by live events, never refetched on its own.
      staleTime: Number.POSITIVE_INFINITY,
    }),
  domains: (serviceId: string) =>
    queryOptions({
      queryKey: keys.domains(serviceId),
      queryFn: () => api.domains(serviceId),
    }),
  variables: (target: VariableTarget) =>
    queryOptions({
      queryKey: keys.variables(target),
      queryFn: () => api.variables(target),
    }),
  secrets: (target: VariableTarget) =>
    queryOptions({
      queryKey: keys.secrets(target),
      queryFn: () => api.secretValues(target),
      staleTime: 0,
      gcTime: 0,
    }),
  pending: (environmentId: string) =>
    queryOptions({
      queryKey: keys.pending(environmentId),
      queryFn: () => api.pendingChanges(environmentId),
    }),
  servers: () => queryOptions({ queryKey: keys.servers, queryFn: api.servers }),
  tokens: () => queryOptions({ queryKey: keys.tokens, queryFn: api.tokens }),
  users: () => queryOptions({ queryKey: keys.users, queryFn: api.users }),
  audit: (filter: AuditFilter = {}) =>
    queryOptions({
      queryKey: keys.audit(filter),
      queryFn: () => api.audit(filter),
    }),
};

// Suspense hooks: data is always defined. Route loaders prefetch, so these
// rarely suspend in practice.
export const useSession = () => useSuspenseQuery(queries.session()).data;
export const useOrg = () => useSuspenseQuery(queries.org()).data;
export const useProjects = () => useSuspenseQuery(queries.projects()).data;
export const useProjectOverview = (projectId: string) =>
  useSuspenseQuery(queries.overview(projectId)).data;
export const useService = (serviceId: string) =>
  useSuspenseQuery(queries.service(serviceId)).data;
export const useReleases = (serviceId: string) =>
  useSuspenseQuery(queries.releases(serviceId)).data;
export const useServiceLogs = (serviceId: string) =>
  useSuspenseQuery(queries.logs(serviceId)).data;
export const useDomains = (serviceId: string) =>
  useSuspenseQuery(queries.domains(serviceId)).data;
export const useVariables = (target: VariableTarget) =>
  useSuspenseQuery(queries.variables(target)).data;
export const usePendingChanges = (environmentId: string) =>
  useSuspenseQuery(queries.pending(environmentId)).data;
export const useServers = () => useSuspenseQuery(queries.servers()).data;
export const useTokens = () => useSuspenseQuery(queries.tokens()).data;
export const useUsers = () => useSuspenseQuery(queries.users()).data;
export const useAuditLog = (filter: AuditFilter = {}) =>
  useSuspenseQuery(queries.audit(filter)).data;

/** Secret values, fetched only while revealed. Needs admin. */
export const useSecretValues = (target: VariableTarget, enabled: boolean) =>
  useQuery({ ...queries.secrets(target), enabled });
