import {
  queryOptions,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { api, body, done, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import {
  currentMe,
  keys,
  revealed,
  revealRequest,
  useOrgs,
} from "../../data/queries";
import type { OrgLockSettings, Scope } from "../../data/types";
import { useOrg } from "../../shell/location";

// An organization's settings (ADR 0027): the org, members, invites,
// variables, lock settings, registry credentials (ADR 0051) and single
// sign-on (ADR 0058) of the org the UI acts in.

/** What each role may do, in a few words: the account page and Members. */
export const roleNote: Record<Scope, string> = {
  read: "see status, releases, logs",
  deploy: "also deploy, restart, roll back, stage variables",
  admin: "everything in the organization, including members and tokens",
};

/**
 * The name of the organization the page acts in, from the caller's
 * organizations (any role may list them), or its slug if they lack it.
 */
export function useOrgName() {
  const slug = useOrg();
  return useOrgs().find((o) => o.slug === slug)?.name ?? slug;
}

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
      queryFn: (ctx) =>
        list(
          api.GET("/api/v1/org/variables", {
            params: { query: { reveal } },
            ...(reveal && revealRequest(ctx)),
          }),
        ),
      ...(reveal && revealed),
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
  /**
   * The open invite a link names, signed in or out (ADR 0060). A dead
   * link is a 404 that won't come back, so it isn't retried.
   */
  invite: (code: string) =>
    queryOptions({
      queryKey: ["invite", code],
      queryFn: () =>
        body(api.GET("/api/v1/invites/{code}", { params: { path: { code } } })),
      retry: false,
    }),
};

export const useRenameOrg = () =>
  useAction(
    (change: OrgChange) => body(api.PATCH("/api/v1/org", { body: change })),
    () => [orgKeys.org, keys.orgs, ["audit"]],
  );

/** A new name, slug or both for an org (ADR 0064). */
export type OrgChange = components["schemas"]["UpdateOrgRequest"];

/**
 * Requires (or stops requiring) a second factor of every member's
 * sign-in (ADR 0057). Turning it on needs the caller's own sign-in to
 * have used one; members without it lose the org until they add one.
 */
export const useSetOrgTwoFactor = () =>
  useAction(
    (require: boolean) =>
      body(api.PUT("/api/v1/org/two-factor", { body: { require } })),
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

/**
 * A new link for an open invite: invites the same person with the same
 * role again, then revokes the old invite (in that order, so a refused or
 * abandoned create leaves the old link working). Dock keeps only a hash of
 * an invite's code, so a link that was lost can't be shown again.
 */
export const useRenewInvite = () =>
  useAction(
    async (invite: { id: number; email: string; role: Scope }) => {
      const created = await body(
        api.POST("/api/v1/org/invites", {
          body: { email: invite.email, role: invite.role },
        }),
      );
      await done(
        api.DELETE("/api/v1/org/invites/{id}", {
          params: { path: { id: invite.id } },
        }),
      );
      return created;
    },
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

/**
 * Creates the account an invite names (its email, with this name and
 * password), accepts the invite and signs in (ADR 0060). Resolves once the
 * new session is in the cache, acting in the org just joined.
 */
export function useInviteSignUp() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({
      code,
      ...req
    }: { code: string } & components["schemas"]["InviteSignUpRequest"]) => {
      await body(
        api.POST("/api/v1/invites/{code}/signup", {
          params: { path: { code } },
          body: req,
        }),
      );
      queryClient.removeQueries({ queryKey: ["invite"] });
      queryClient.setQueryData(keys.session, await currentMe());
    },
  });
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
