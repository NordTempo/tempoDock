import {
  type QueryKey,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { api } from "./fixtures";
import { keys } from "./queries";
import type { NewChange, NewToken } from "./types";

// Mutation hooks. Each one names the cache subtrees it touches; they are
// refetched once the call settles. Live events (./live.ts) cover changes that
// happen later, such as deploy progress.

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

const serviceWide = (serviceId: string): QueryKey[] => [
  keys.service(serviceId),
  keys.projects,
  ["audit"],
];

export const useDeploy = () =>
  useAction((serviceId: string) => api.deploy(serviceId), serviceWide);

export const useRollback = () =>
  useAction(
    ({ serviceId, to }: { serviceId: string; to: number }) =>
      api.rollback(serviceId, to),
    ({ serviceId }) => serviceWide(serviceId),
  );

export const useRestart = () =>
  useAction((serviceId: string) => api.restart(serviceId), serviceWide);

export const usePromote = () =>
  useAction(
    (v: { projectId: string; from: string; to: string }) =>
      api.promote(v.projectId, v.from, v.to),
    () => [["services"], keys.projects, ["audit"]],
  );

type StageInput<K extends NewChange["kind"]> = {
  environmentId: string;
  change: Extract<NewChange, { kind: K }>;
};

export const useStageVariable = () =>
  useAction(
    ({
      environmentId,
      change,
    }: StageInput<"variable.set" | "variable.delete">) =>
      api.stageChange(environmentId, change),
    ({ environmentId }) => [keys.pending(environmentId)],
  );

export const useStageSetting = () =>
  useAction(
    ({ environmentId, change }: StageInput<"setting">) =>
      api.stageChange(environmentId, change),
    ({ environmentId }) => [keys.pending(environmentId)],
  );

type ChangeSet = { environmentId: string; changeIds: string[] };

export const useDiscardChanges = () =>
  useAction(
    ({ environmentId, changeIds }: ChangeSet) =>
      api.discardChanges(environmentId, changeIds),
    ({ environmentId }) => [keys.pending(environmentId)],
  );

/** Applies staged changes; every affected service gets a new release. */
export const useApplyChanges = () =>
  useAction(
    ({ environmentId, changeIds }: ChangeSet) =>
      api.applyChanges(environmentId, changeIds),
    ({ environmentId }) => [
      ["environments", environmentId],
      ["services"],
      keys.projects,
      ["audit"],
    ],
  );

const domainWide = (serviceId: string): QueryKey[] => [
  keys.domains(serviceId),
  keys.service(serviceId),
  keys.projects,
  ["audit"],
];

export const useAddDomain = () =>
  useAction(
    ({ serviceId, hostname }: { serviceId: string; hostname: string }) =>
      api.addDomain(serviceId, hostname),
    ({ serviceId }) => domainWide(serviceId),
  );

export const useGenerateDomain = () =>
  useAction((serviceId: string) => api.generateDomain(serviceId), domainWide);

export const useRemoveDomain = () =>
  useAction(
    ({ domainId }: { serviceId: string; domainId: string }) =>
      api.removeDomain(domainId),
    ({ serviceId }) => domainWide(serviceId),
  );

/** Stops and archives a service. Admin only; purged after 7 days. */
export const useArchiveService = () =>
  useAction(
    (serviceId: string) => api.archiveService(serviceId),
    () => [keys.projects, ["audit"]],
  );

export const usePruneBuildCache = () =>
  useAction(
    (serverId: string) => api.pruneBuildCache(serverId),
    () => [keys.servers, keys.projects, ["audit"]],
  );

/** Resolves with the token and its secret, which is shown exactly once. */
export const useCreateToken = () =>
  useAction(
    (input: NewToken) => api.createToken(input),
    () => [keys.tokens, keys.org, ["audit"]],
  );

export const useRevokeToken = () =>
  useAction(
    (tokenId: string) => api.revokeToken(tokenId),
    () => [keys.tokens, keys.org, ["audit"]],
  );

export function useLogin() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      username,
      password,
    }: {
      username: string;
      password: string;
    }) => api.login(username, password),
    onSuccess: (session) => queryClient.setQueryData(keys.session, session),
  });
}

export function useLogout() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: api.logout,
    onSuccess: () => {
      queryClient.clear();
      queryClient.setQueryData(keys.session, null);
    },
  });
}
