import { queryOptions, useQueryClient } from "@tanstack/react-query";
import { api, body, done, list } from "../../api/client";
import { useAction } from "../../data/mutations";
import { keys } from "../../data/queries";
import type { OrgLockSettings, Scope } from "../../data/types";

// An organization's settings (ADR 0027): the org, members, invites,
// variables, lock settings and registry credentials (ADR 0051) of the
// org the UI acts in.

const orgKeys = {
  org: ["org"],
  members: ["org", "members"],
  invites: ["org", "invites"],
  variables: (reveal: boolean) => ["org", "variables", reveal],
  lockSettings: ["org", "lock-settings"],
  // live.ts refetches it on org.registry_credential_* audit events.
  registryCredentials: ["org", "registry-credentials"],
};

export const orgQueries = {
  org: () =>
    queryOptions({
      queryKey: orgKeys.org,
      queryFn: () => body(api.GET("/api/v1/org")),
    }),
  members: () =>
    queryOptions({
      queryKey: orgKeys.members,
      queryFn: () => list(api.GET("/api/v1/org/members")),
    }),
  invites: () =>
    queryOptions({
      queryKey: orgKeys.invites,
      queryFn: () => list(api.GET("/api/v1/org/invites")),
    }),
  variables: (reveal = false) =>
    queryOptions({
      queryKey: orgKeys.variables(reveal),
      queryFn: () =>
        list(
          api.GET("/api/v1/org/variables", {
            params: { query: { reveal } },
          }),
        ),
    }),
  /** Locking new production environments, and when requests expire (ADR 0044). */
  lockSettings: () =>
    queryOptions({
      queryKey: orgKeys.lockSettings,
      queryFn: () => body(api.GET("/api/v1/org/lock-settings")),
    }),
  registryCredentials: () =>
    queryOptions({
      queryKey: orgKeys.registryCredentials,
      queryFn: () => list(api.GET("/api/v1/org/registry-credentials")),
    }),
  invite: (code: string) =>
    queryOptions({
      queryKey: ["invite", code],
      queryFn: () =>
        body(api.GET("/api/v1/invites/{code}", { params: { path: { code } } })),
    }),
};

export const useRenameOrg = () =>
  useAction(
    (name: string) => body(api.PATCH("/api/v1/org", { body: { name } })),
    () => [orgKeys.org, keys.orgs, ["audit"]],
  );

export const useDeleteOrg = () =>
  useAction(
    () => done(api.DELETE("/api/v1/org")),
    () => [keys.orgs],
  );

export const useLeaveOrg = () =>
  useAction(
    () => done(api.POST("/api/v1/org/leave")),
    () => [keys.orgs],
  );

export const useSetMemberRole = () =>
  useAction(
    ({ id, role }: { id: number; role: Scope }) =>
      body(
        api.PATCH("/api/v1/org/members/{id}", {
          params: { path: { id } },
          body: { role },
        }),
      ),
    () => [orgKeys.members, ["audit"]],
  );

export const useRemoveMember = () =>
  useAction(
    (id: number) =>
      done(
        api.DELETE("/api/v1/org/members/{id}", { params: { path: { id } } }),
      ),
    () => [orgKeys.members, ["audit"]],
  );

export const useCreateInvite = () =>
  useAction(
    (input: { email: string; role: Scope }) =>
      body(api.POST("/api/v1/org/invites", { body: input })),
    () => [orgKeys.invites, ["audit"]],
  );

export const useRevokeInvite = () =>
  useAction(
    (id: number) =>
      done(
        api.DELETE("/api/v1/org/invites/{id}", { params: { path: { id } } }),
      ),
    () => [orgKeys.invites, ["audit"]],
  );

/** Accepts an invite; the caller's orgs then include it. */
export function useAcceptInvite() {
  const queryClient = useQueryClient();
  return useAction(
    (code: string) =>
      body(
        api.POST("/api/v1/invites/{code}/accept", {
          params: { path: { code } },
        }),
      ),
    () => {
      queryClient.removeQueries({ queryKey: ["invite"] });
      return [keys.orgs];
    },
  );
}

export const useSetOrgVariable = () =>
  useAction(
    ({ key, value }: { key: string; value: string }) =>
      body(
        api.PUT("/api/v1/org/variables/{key}", {
          params: { path: { key } },
          body: { value },
        }),
      ),
    () => [["org", "variables"], ["audit"]],
  );

export const useDeleteOrgVariable = () =>
  useAction(
    (key: string) =>
      done(
        api.DELETE("/api/v1/org/variables/{key}", {
          params: { path: { key } },
        }),
      ),
    () => [["org", "variables"], ["audit"]],
  );

export const useRedeployOrgVariable = () =>
  useAction(
    (key: string) =>
      body(
        api.POST("/api/v1/org/variables/{key}/redeploy", {
          params: { path: { key } },
        }),
      ),
    () => [["audit"], ["overview"], ["releases"]],
  );

/** Saves the lock settings; a request_ttl_hours of 0 keeps the current one. */
export function useSetLockSettings() {
  const queryClient = useQueryClient();
  return useAction(
    async (settings: OrgLockSettings) => {
      const saved = await body(
        api.PUT("/api/v1/org/lock-settings", { body: settings }),
      );
      queryClient.setQueryData(orgKeys.lockSettings, saved);
      return saved;
    },
    () => [orgKeys.lockSettings, ["audit"]],
  );
}

export const useSetRegistryCredential = () =>
  useAction(
    ({
      host,
      username,
      password,
    }: {
      host: string;
      username: string;
      password: string;
    }) =>
      body(
        api.PUT("/api/v1/org/registry-credentials/{host}", {
          params: { path: { host } },
          body: { username, password },
        }),
      ),
    () => [orgKeys.registryCredentials, ["audit"]],
  );

export const useDeleteRegistryCredential = () =>
  useAction(
    (host: string) =>
      done(
        api.DELETE("/api/v1/org/registry-credentials/{host}", {
          params: { path: { host } },
        }),
      ),
    () => [orgKeys.registryCredentials, ["audit"]],
  );
