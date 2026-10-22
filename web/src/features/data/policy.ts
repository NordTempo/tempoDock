import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import { keys } from "../../data/queries";
import type { TokenDataAccess } from "../../data/types";
import type {
  DataEnvironmentKind,
  DataLevel,
  DataPolicyRule,
  DataPolicyScope,
  DataSensitivity,
  DataSubject,
} from "./types";

// The data access policy (ADR 0016 section 4) at one scope: the default,
// a project, or an environment.

/** Which policy: none for the default, a project, or one environment. */
export type PolicyScope = { project?: string; environment?: string };

/** GET the policy at one scope. */
function getPolicy({ project, environment }: PolicyScope) {
  if (!project) return body(api.GET("/api/v1/data-policy"));
  if (!environment) {
    return body(
      api.GET("/api/v1/projects/{project}/data-policy", {
        params: { path: { project } },
      }),
    );
  }
  return body(
    api.GET(
      "/api/v1/projects/{project}/environments/{environment}/data-policy",
      {
        params: { path: { project, environment } },
      },
    ),
  );
}

/** PUT the rules set at one scope. */
function putPolicy(
  { project, environment }: PolicyScope,
  rules: DataPolicyRule[],
) {
  if (!project)
    return body(api.PUT("/api/v1/data-policy", { body: { rules } }));
  if (!environment) {
    return body(
      api.PUT("/api/v1/projects/{project}/data-policy", {
        params: { path: { project } },
        body: { rules },
      }),
    );
  }
  return body(
    api.PUT(
      "/api/v1/projects/{project}/environments/{environment}/data-policy",
      {
        params: { path: { project, environment } },
        body: { rules },
      },
    ),
  );
}

const policyKey = (s: PolicyScope) => [
  ...keys.dataPolicy,
  s.project ?? "",
  s.environment ?? "",
];

/** GET the policy at one scope, with its effective rules. */
export const usePolicy = (scope: PolicyScope) =>
  useQuery({
    queryKey: policyKey(scope),
    queryFn: () => getPolicy(scope),
  });

/**
 * PUT the rules set at one scope (admin). More specific scopes inherit
 * from it and everyone's access may change, so both refetch.
 */
export function useSavePolicy(scope: PolicyScope) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (rules: DataPolicyRule[]) => putPolicy(scope, rules),
    onSuccess: (policy) => client.setQueryData(policyKey(scope), policy),
    onSettled: () =>
      Promise.all([
        client.invalidateQueries({ queryKey: keys.dataPolicy }),
        client.invalidateQueries({
          queryKey: ["data"],
          predicate: (q) => q.queryKey[4] === "info",
        }),
      ]),
  });
}

/**
 * PATCH /tokens/{id} (admin): a token's own data access, or null to follow
 * the policy's tokens rule again.
 */
export function useSetTokenData() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ id, data }: { id: number; data: TokenDataAccess | null }) =>
      body(
        api.PATCH("/api/v1/tokens/{id}", {
          params: { path: { id } },
          body: { data },
        }),
      ),
    onSettled: () => client.invalidateQueries({ queryKey: keys.tokens }),
  });
}

export const levels: DataLevel[] = [
  "none",
  "schema",
  "read",
  "propose",
  "write",
];
export const sensitivities: DataSensitivity[] = ["masked", "visible"];
export const subjects: DataSubject[] = ["role:read", "role:deploy", "tokens"];
export const kinds: DataEnvironmentKind[] = ["non_production", "production"];

/** What each level allows, in a few words. */
export const levelNotes: Record<DataLevel, string> = {
  none: "nothing",
  schema: "tables and columns, no rows",
  read: "browse rows, read-only SQL",
  propose: "also draft changes an admin approves",
  write: "also commit their own changes",
};

export const subjectNames: Record<DataSubject, string> = {
  "role:read": "read users",
  "role:deploy": "deploy users",
  tokens: "tokens",
};

export const kindNames: Record<DataEnvironmentKind, string> = {
  non_production: "non-production",
  production: "production",
};

/** Where an effective rule comes from, as a phrase after "from". */
export function fromText(
  from: DataPolicyScope | undefined,
  scope: PolicyScope,
) {
  switch (from) {
    case "environment":
      return `environment ${scope.environment}`;
    case "project":
      return `project ${scope.project}`;
    case "default":
      return "the default policy";
    default:
      return "the built-in defaults";
  }
}
