import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import { keys } from "../../data/queries";
import type {
  DataEnvironmentKind,
  DataPolicyRule,
  DataPolicyScope,
  DataSubject,
  DataTokenAccess,
} from "./types";

// Dock's data access policy (ADR 0016 section 4) at one scope: the
// default, a project, or an environment.

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

/** The policy at one scope as query options (the panel's source uses it). */
export const policyQuery = (scope: PolicyScope) => ({
  queryKey: policyKey(scope),
  queryFn: () => getPolicy(scope),
});

/** GET the policy at one scope, with its effective rules. */
export const usePolicy = (scope: PolicyScope) => useQuery(policyQuery(scope));

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
 * the policy's tokens rule again. For TokenDataEditor's onSave.
 */
export function useSaveTokenData() {
  const client = useQueryClient();
  return async (id: number, data: DataTokenAccess) => {
    try {
      return await body(
        api.PATCH("/api/v1/tokens/{id}", {
          params: { path: { id } },
          body: { data },
        }),
      );
    } finally {
      await client.invalidateQueries({ queryKey: keys.tokens });
    }
  };
}

export const subjectNames: Record<DataSubject, string> = {
  "role:read": "read users",
  "role:deploy": "deploy users",
  tokens: "tokens",
};

// The built-in defaults (ADR 0016 section 4), what the default policy
// inherits.
export const builtIn = (subject: DataSubject, kind: DataEnvironmentKind) => ({
  level:
    subject === "role:deploy" && kind === "non_production"
      ? ("read" as const)
      : ("none" as const),
  sensitive: "masked" as const,
});

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
