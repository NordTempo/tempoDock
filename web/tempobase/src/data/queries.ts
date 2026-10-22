import {
  queryOptions,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  useSuspenseQuery,
} from "@tanstack/react-query";
import type { PolicyRule } from "../../../src/features/data/policy";
import { isUnauthorized, tb } from "../api/backend";
import type {
  AuditQuery,
  CreateConnectionRequest,
  CreateTokenRequest,
  Me,
  Role,
  UpdateConnectionRequest,
  UpdateTokenRequest,
  UpdateUserRequest,
} from "../api/types";

// tempoBase's queries and mutations. Nothing polls: lists refetch after
// the mutations that change them, and the changeset list when the window
// regains focus (set on the connection route).

export const keys = {
  me: ["me"],
  setup: ["setup"],
  version: ["version"],
  connections: ["connections"],
  connection: (name: string) => ["connections", name],
  users: ["users"],
  invites: ["invites"],
  tokens: ["tokens"],
  /** "" for the default policy. */
  policy: (connection = "") => ["policy", connection],
  audit: (q: AuditQuery) => ["audit", q],
  auditDetail: (id: number) => ["audit-detail", id],
  /** The data panel's queries for one connection. */
  data: (connection: string) => ["data", connection],
};

export const queries = {
  /** The caller, or null when signed out. */
  me: () =>
    queryOptions({
      queryKey: keys.me,
      queryFn: async (): Promise<Me | null> => {
        try {
          return await tb().me();
        } catch (e) {
          if (isUnauthorized(e)) return null;
          throw e;
        }
      },
      staleTime: Number.POSITIVE_INFINITY,
    }),
  setup: () =>
    queryOptions({
      queryKey: keys.setup,
      queryFn: () => tb().setupNeeded(),
    }),
  // Opening the list always shows current pending counts: it's one cheap
  // read, never polled.
  connections: () =>
    queryOptions({
      queryKey: keys.connections,
      queryFn: () => tb().connections(),
      staleTime: 0,
    }),
  connection: (name: string) =>
    queryOptions({
      queryKey: keys.connection(name),
      queryFn: () => tb().connection(name),
    }),
};

/** The signed-in caller; the _app route makes sure there is one. */
export function useMe() {
  const me = useSuspenseQuery(queries.me()).data;
  if (!me) throw new Error("Signed out");
  return me;
}

export const useIsAdmin = () => useMe().role === "admin";

/** Signs in (login, setup, a link): the caller becomes the new "me". */
function useSignIn<In>(fn: (input: In) => Promise<Me>) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: (me) => {
      client.clear();
      client.setQueryData(keys.me, me);
    },
  });
}

export const useLogin = () =>
  useSignIn((req: { email: string; password: string }) => tb().login(req));

export const useSetup = () =>
  useSignIn(
    (req: { code: string; email: string; name: string; password: string }) =>
      tb().setup(req),
  );

export const useUseLink = (code: string) =>
  useSignIn((req: { name?: string; password: string }) =>
    tb().useLink(code, req),
  );

export function useLogout() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: () => tb().logout(),
    onSuccess: () => {
      client.clear();
      client.setQueryData(keys.me, null);
    },
  });
}

/** A mutation that refetches some lists when it settles. */
function useChange<In, Out>(
  fn: (input: In) => Promise<Out>,
  refetch: (input: In) => readonly (readonly unknown[])[],
) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSettled: (_out, _error, input) =>
      Promise.all(
        refetch(input).map((queryKey) =>
          client.invalidateQueries({ queryKey }),
        ),
      ),
  });
}

export const useUsers = () =>
  useQuery({ queryKey: keys.users, queryFn: () => tb().users() });
export const useInvites = () =>
  useQuery({ queryKey: keys.invites, queryFn: () => tb().invites() });
export const useTokens = () =>
  useQuery({ queryKey: keys.tokens, queryFn: () => tb().tokens() });
export const usePolicy = (connection?: string) =>
  useQuery({
    queryKey: keys.policy(connection),
    queryFn: () => tb().policy(connection),
  });

/** The audit log, newest first, in pages of `limit` before an id. */
export const useAudit = (q: AuditQuery) =>
  useInfiniteQuery({
    queryKey: keys.audit(q),
    queryFn: ({ pageParam }) => tb().audit({ ...q, before: pageParam }),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (page) =>
      q.limit && page.length >= q.limit ? page.at(-1)?.id : undefined,
  });

export const useAuditDetail = (id: number | undefined) =>
  useQuery({
    queryKey: keys.auditDetail(id ?? 0),
    queryFn: () => tb().auditDetail(id ?? 0),
    enabled: id !== undefined,
    staleTime: Number.POSITIVE_INFINITY,
  });

export const useUpdateUser = () =>
  useChange(
    ({
      id,
      ...req
    }: UpdateUserRequest & {
      id: number;
    }) => tb().updateUser(id, req),
    () => [keys.users],
  );
export const useDeleteUser = () =>
  useChange(
    (id: number) => tb().deleteUser(id),
    () => [keys.users],
  );
export const useResetLink = () =>
  useMutation({ mutationFn: (id: number) => tb().resetLink(id) });
export const useCreateInvite = () =>
  useChange(
    (req: { email: string; role: Role }) => tb().createInvite(req),
    () => [keys.invites],
  );
export const useDeleteInvite = () =>
  useChange(
    (id: number) => tb().deleteInvite(id),
    () => [keys.invites],
  );

export const useCreateToken = () =>
  useChange(
    (req: CreateTokenRequest) => tb().createToken(req),
    () => [keys.tokens],
  );
export const useUpdateToken = () =>
  useChange(
    ({ id, ...req }: UpdateTokenRequest & { id: number }) =>
      tb().updateToken(id, req),
    () => [keys.tokens],
  );
export const useDeleteToken = () =>
  useChange(
    (id: number) => tb().deleteToken(id),
    () => [keys.tokens],
  );

export const useChangePassword = () =>
  useMutation({
    mutationFn: (req: { current_password: string; new_password: string }) =>
      tb().changePassword(req),
  });

export const useTestConnection = () =>
  useMutation({
    mutationFn: (req: { url: string; ca_pem?: string }) =>
      tb().testConnection(req),
  });
export const useCreateConnection = () =>
  useChange(
    (req: CreateConnectionRequest) => tb().createConnection(req),
    () => [keys.connections],
  );
/** PATCH a connection; a rename or new URL changes what the panel reads. */
export const useUpdateConnection = (name: string) =>
  useChange(
    (req: UpdateConnectionRequest) => tb().updateConnection(name, req),
    () => [keys.connections, keys.data(name), keys.policy(name)],
  );
export const useDeleteConnection = () =>
  useChange(
    (name: string) => tb().deleteConnection(name),
    (name) => [keys.connections, keys.data(name)],
  );

/**
 * PUT the rules at one scope. Everyone's access may change, so the
 * policies, the connection list and the panels' info refetch.
 */
export function useSavePolicy(connection?: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (rules: PolicyRule[]) => tb().setPolicy(rules, connection),
    onSuccess: (policy) => client.setQueryData(keys.policy(connection), policy),
    onSettled: () =>
      Promise.all([
        client.invalidateQueries({ queryKey: ["policy"] }),
        client.invalidateQueries({ queryKey: keys.connections }),
        client.invalidateQueries({
          queryKey: ["data"],
          predicate: (q) => q.queryKey[2] === "info",
        }),
      ]),
  });
}
