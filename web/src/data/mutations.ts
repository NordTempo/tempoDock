import {
  type QueryClient,
  type QueryKey,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { type RegisteredRouter, useRouter } from "@tanstack/react-router";
import { api, body, done } from "../api/client";
import { placeholderApi } from "./placeholders";
import { envPath, keys, servicePath } from "./queries";
import type {
  DiscardSelection,
  EnvironmentKind,
  EnvRef,
  NewServer,
  NewToken,
  NewUser,
  Server,
  ServerPatch,
  ServiceRef,
  SettingsPatch,
} from "./types";

// Mutation hooks. Each one names the cache subtrees it touches; they are
// refetched once the call settles. Live events (./live.ts) cover what happens
// later, such as deploy progress, and changes made from the CLI.

function useAction<Vars, Data>(
  mutationFn: (vars: Vars) => Promise<Data>,
  touches: (vars: Vars) => QueryKey[],
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn,
    onSettled: (_data, _error, vars) =>
      Promise.all(
        touches(vars).map((queryKey) =>
          queryClient.invalidateQueries({ queryKey }),
        ),
      ),
  });
}

const serviceWide = (ref: ServiceRef): QueryKey[] => [
  keys.service(ref),
  keys.overview(ref.project),
  ["audit"],
];

/** Deploys the service's image setting again (source deploys need the CLI). */
export const useDeploy = () =>
  useAction(
    (ref: ServiceRef) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/releases",
          { params: { path: servicePath(ref) }, body: {} },
        ),
      ),
    serviceWide,
  );

export const useRollback = () =>
  useAction(
    ({ ref, to }: { ref: ServiceRef; to: number }) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/releases/{number}/rollback",
          { params: { path: { ...servicePath(ref), number: to } } },
        ),
      ),
    ({ ref }) => serviceWide(ref),
  );

export const useRestart = () =>
  useAction(
    (ref: ServiceRef) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/restart",
          { params: { path: servicePath(ref) } },
        ),
      ),
    serviceWide,
  );

// Staging touches the environment (changes, variables) and the overview,
// which counts staged changes per service.
const staging = (env: EnvRef): QueryKey[] => [
  keys.env(env),
  keys.overview(env.project),
  ["audit"],
];

type VariableEdit = {
  env: EnvRef;
  /** null for shared (environment-level) variables. */
  service: string | null;
  set?: Record<string, string>;
  unset?: string[];
};

function setVariables({ env, service, set, unset }: VariableEdit) {
  return body(
    api.PATCH(
      "/api/v1/projects/{project}/environments/{environment}/variables",
      {
        params: { path: envPath(env) },
        body: { set, unset, ...(service && { service }) },
      },
    ),
  );
}

/** Stages setting and unsetting variables. */
export const useStageVariables = () =>
  useAction(setVariables, ({ env }) => staging(env));

/** Stages service settings. */
export const useStageSettings = () =>
  useAction(
    ({ ref, patch }: { ref: ServiceRef; patch: SettingsPatch }) =>
      body(
        api.PATCH(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}",
          { params: { path: servicePath(ref) }, body: patch },
        ),
      ),
    ({ ref }) => staging(ref),
  );

/**
 * Drops staged changes: the selected ones (per-row undo), or all of them
 * without a selection.
 */
export const useDiscardChanges = () =>
  useAction(
    ({ env, selection }: { env: EnvRef; selection?: DiscardSelection }) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/changes/discard",
          { params: { path: envPath(env) }, body: selection },
        ),
      ),
    ({ env }) => staging(env),
  );

/** Applies staged changes; every affected running service gets a release. */
export const useApplyChanges = () =>
  useAction(
    (env: EnvRef) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/changes/apply",
          { params: { path: envPath(env) } },
        ),
      ),
    (env) => [keys.project(env.project), ["audit"]],
  );

export const useAddDomain = () =>
  useAction(
    ({ ref, hostname }: { ref: ServiceRef; hostname: string }) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/domains",
          { params: { path: servicePath(ref) }, body: { hostname } },
        ),
      ),
    ({ ref }) => serviceWide(ref),
  );

export const useGenerateDomain = () =>
  useAction(
    (ref: ServiceRef) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/domains/generate",
          { params: { path: servicePath(ref) } },
        ),
      ),
    serviceWide,
  );

export const useRemoveDomain = () =>
  useAction(
    ({ ref, hostname }: { ref: ServiceRef; hostname: string }) =>
      done(
        api.DELETE(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/domains/{hostname}",
          { params: { path: { ...servicePath(ref), hostname } } },
        ),
      ),
    ({ ref }) => serviceWide(ref),
  );

/** Stops and archives a service. Admin only; volumes are kept. */
export const useArchiveService = () =>
  useAction(
    (ref: ServiceRef) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/archive",
          { params: { path: servicePath(ref) } },
        ),
      ),
    (ref) => [keys.overview(ref.project), ["audit"]],
  );

/**
 * Archives a project: stops every service, removes routes, hides it from
 * lists. Admin only; volumes and history are kept.
 */
export const useArchiveProject = () =>
  useAction(
    (project: string) =>
      body(
        api.POST("/api/v1/projects/{project}/archive", {
          params: { path: { project } },
        }),
      ),
    (project) => [keys.projects, keys.project(project), ["audit"]],
  );

/** Resolves with the token and its secret, which is shown exactly once. */
export const useCreateToken = () =>
  useAction(
    (input: NewToken) => body(api.POST("/api/v1/tokens", { body: input })),
    () => [keys.tokens, ["audit"]],
  );

export const useRevokeToken = () =>
  useAction(
    (id: number) =>
      done(api.DELETE("/api/v1/tokens/{id}", { params: { path: { id } } })),
    () => [keys.tokens, ["audit"]],
  );

export const useCreateUser = () =>
  useAction(
    (input: NewUser) => body(api.POST("/api/v1/users", { body: input })),
    () => [keys.users, ["audit"]],
  );

/** Disabled users can't log in; tokens they created keep working. */
export const useSetUserDisabled = () =>
  useAction(
    ({ id, disabled }: { id: number; disabled: boolean }) =>
      body(
        disabled
          ? api.POST("/api/v1/users/{id}/disable", { params: { path: { id } } })
          : api.POST("/api/v1/users/{id}/enable", { params: { path: { id } } }),
      ),
    () => [keys.users, ["audit"]],
  );

export const useDeleteUser = () =>
  useAction(
    (id: number) =>
      done(api.DELETE("/api/v1/users/{id}", { params: { path: { id } } })),
    () => [keys.users, ["audit"]],
  );

/**
 * Generates a new password (resolved once, never shown again) and ends the
 * user's sessions. Not for your own account.
 */
export const useResetPassword = () =>
  useAction(
    (id: number) =>
      body(
        api.POST("/api/v1/users/{id}/reset-password", {
          params: { path: { id } },
          body: {},
        }),
      ),
    () => [keys.users, ["audit"]],
  );

/**
 * What a server accepts, whether it takes new placements and builds
 * images, and its base domain.
 */
export const useUpdateServer = () =>
  useAction(
    ({ name, patch }: { name: string; patch: ServerPatch }) =>
      placeholderApi.updateServer(name, patch),
    () => [keys.servers, ["audit"]],
  );

/**
 * Adds a server: resolves with its join token, shown exactly once, and the
 * install command to run on it. It is `joining` until it connects.
 */
export const useAddServer = () =>
  useAction(
    (input: NewServer) => placeholderApi.addServer(input),
    () => [keys.servers, ["audit"]],
  );

/**
 * Forgets a server's key and drops its link. Its apps keep running, but
 * nothing reaches it until it joins again.
 */
export const useRevokeServer = () =>
  useAction(
    (name: string) => placeholderApi.revokeServer(name),
    () => [keys.servers, ["audit"]],
  );

/**
 * Deletes a server with no environments placed on it. Only the list
 * refetches: the server's own page would 404 before the caller leaves it.
 */
export function useRemoveServer() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => placeholderApi.removeServer(name),
    onSuccess: (_data, name) =>
      queryClient.setQueryData<Server[]>(keys.servers, (list) =>
        list?.filter((s) => s.name !== name),
      ),
  });
}

/**
 * Deploys the source environment's active images to the target, one
 * service at a time. Resolves once the releases exist; release events
 * follow their progress.
 */
export const usePromote = () =>
  useAction(
    ({
      project,
      ...input
    }: {
      project: string;
      from: string;
      to: string;
      services?: string[];
    }) => placeholderApi.promote(project, input),
    ({ project }) => [keys.overview(project), ["audit"]],
  );

/** Pins a project's environments of a kind to a server before any exist. */
export const usePinPlacement = () =>
  useAction(
    ({
      project,
      kind,
      server,
    }: {
      project: string;
      kind: EnvironmentKind;
      server: string;
    }) =>
      body(
        api.PUT("/api/v1/projects/{project}/placements/{kind}", {
          params: { path: { project, kind } },
          body: { server },
        }),
      ),
    ({ project }) => [keys.placements(project), keys.servers, ["audit"]],
  );

export function useLogin() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (credentials: { email: string; password: string }) =>
      body(api.POST("/api/v1/auth/login", { body: credentials })),
    onSuccess: (session) => queryClient.setQueryData(keys.session, session),
  });
}

export function useLogout() {
  const queryClient = useQueryClient();
  const router = useRouter();
  return useMutation({
    mutationFn: () => done(api.POST("/api/v1/auth/logout")),
    onSuccess: () => signedOut(queryClient, router),
  });
}

/**
 * Leaves for the login page (which comes back to `redirect` afterwards) and
 * drops everything cached for the old session once its screens are gone.
 */
export async function signedOut(
  queryClient: QueryClient,
  router: RegisteredRouter,
  redirect?: string,
) {
  queryClient.setQueryData(keys.session, null);
  await router.navigate({ to: "/login", search: { redirect } });
  queryClient.removeQueries({
    predicate: (query) => query.queryKey[0] !== "session",
  });
}
