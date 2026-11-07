import { queryOptions, useSuspenseQuery } from "@tanstack/react-query";
import { api, body, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";

// User accounts as the platform's (ADR 0062): every account with the orgs
// it belongs to and owns, and who operates the platform. Roles in an org
// are that org's, on its Members page.

export type Account = components["schemas"]["Account"];

export const accountsKey = ["console", "users"] as const;

export const accountsQuery = () =>
  queryOptions({
    queryKey: accountsKey,
    queryFn: () => list(api.GET("/api/v1/console/users")),
  });

export const useAccounts = () => useSuspenseQuery(accountsQuery()).data;

export const useSetOperator = () =>
  useAction(
    ({ id, operator }: { id: number; operator: boolean }) =>
      body(
        api.PUT("/api/v1/console/users/{id}/operator", {
          params: { path: { id } },
          body: { operator },
        }),
      ),
    () => [accountsKey, ["audit"]],
    (_v, a) =>
      a.operator
        ? `${a.email} operates the platform`
        : `${a.email} no longer operates the platform`,
  );
