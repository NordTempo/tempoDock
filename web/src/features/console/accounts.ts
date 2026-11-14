import { queryOptions, useSuspenseQuery } from "@tanstack/react-query";
import { api, body, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";

// User accounts as the platform's (ADR 0062): every account with the orgs
// it belongs to and owns, and its platform role (ADR 0080). Roles in an org
// are that org's, on its Members page.

export type Account = components["schemas"]["Account"];

export const accountsKey = ["console", "users"] as const;

export const accountsQuery = () =>
  queryOptions({
    queryKey: accountsKey,
    queryFn: () => list(api.GET("/api/v1/console/users")),
  });

export const useAccounts = () => useSuspenseQuery(accountsQuery()).data;

/** A platform role to give, or "none" to take one away (ADR 0080). */
export type PlatformRoleChoice = "operator" | "viewer" | "none";

export const useSetPlatformRole = () =>
  useAction(
    ({ id, role }: { id: number; role: PlatformRoleChoice }) =>
      body(
        api.PUT("/api/v1/console/users/{id}/platform-role", {
          params: { path: { id } },
          body: { role },
        }),
      ),
    () => [accountsKey, ["audit"]],
    (_v, a) =>
      a.platform_role
        ? `${a.email} is a platform ${a.platform_role}`
        : `${a.email} has no platform role`,
  );
