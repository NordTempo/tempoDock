import { queryOptions, useSuspenseQuery } from "@tanstack/react-query";
import { api, body, done, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";

// Groups and grants (ADR 0080): an org's groups with their members and
// role grants, one person's one-off grants, the built-in roles, and a
// person's effective access.

export type Group = components["schemas"]["Group"];
export type Grant = components["schemas"]["Grant"];
export type GrantRequest = components["schemas"]["GrantRequest"];
export type AreaRole = components["schemas"]["AreaRole"];
export type EffectiveAccess = components["schemas"]["EffectiveAccess"];
export type Role = components["schemas"]["Role"];
export type RoleRequest = components["schemas"]["RoleRequest"];

const keys = {
  groups: ["access", "groups"] as const,
  grants: ["access", "grants"] as const,
  roles: ["access", "roles"] as const,
  customRoles: ["access", "custom-roles"] as const,
  access: (who: string) => ["access", "effective", who] as const,
};
/** Every query a change to groups or grants can make stale. */
const touched = () => [["access"], ["org", "members"], ["session"], ["audit"]];

export const accessQueries = {
  groups: () =>
    queryOptions({
      queryKey: keys.groups,
      queryFn: () => list(api.GET("/api/v1/groups")),
    }),
  grants: () =>
    queryOptions({
      queryKey: keys.grants,
      queryFn: () => list(api.GET("/api/v1/grants")),
    }),
  roles: () =>
    queryOptions({
      queryKey: keys.roles,
      queryFn: () => list(api.GET("/api/v1/roles")),
      staleTime: Number.POSITIVE_INFINITY, // built in: they change with Dock
    }),
  /** The org's own roles. */
  customRoles: () =>
    queryOptions({
      queryKey: keys.customRoles,
      queryFn: () => list(api.GET("/api/v1/custom-roles")),
    }),
  /** who "" is the caller. */
  access: (who: string) =>
    queryOptions({
      queryKey: keys.access(who),
      queryFn: () =>
        who
          ? body(
              api.GET("/api/v1/access/{member}", {
                params: { path: { member: who } },
              }),
            )
          : body(api.GET("/api/v1/access")),
    }),
};

export const useGroups = () => useSuspenseQuery(accessQueries.groups()).data;
export const useGrants = () => useSuspenseQuery(accessQueries.grants()).data;
export const useRoles = () => useSuspenseQuery(accessQueries.roles()).data;
export const useCustomRoles = () =>
  useSuspenseQuery(accessQueries.customRoles()).data;

/** A role key's display name, like "Deploy edit" for deploy:edit. */
export function useRoleName() {
  const roles = useRoles();
  return (key: string) => {
    for (const r of roles)
      for (const l of r.levels ?? [])
        if (l.role === key) return l.name ? `${r.name} ${l.name}` : r.name;
    return key;
  };
}

const group = (name: string) => ({ params: { path: { group: name } } });
const role = (name: string) => ({ params: { path: { role: name } } });

/** Makes a role, or changes the one named was. */
export const useSaveRole = () =>
  useAction(
    ({ was, role: r }: { was?: string; role: RoleRequest }) =>
      body(
        was
          ? api.PUT("/api/v1/custom-roles/{role}", { ...role(was), body: r })
          : api.POST("/api/v1/custom-roles", { body: r }),
      ),
    touched,
    (v) => (v.was ? `Saved role ${v.role.name}` : `Made role ${v.role.name}`),
  );

export const useDeleteRole = () =>
  useAction(
    (name: string) =>
      done(api.DELETE("/api/v1/custom-roles/{role}", role(name))),
    touched,
    (name) => `Removed role ${name}`,
  );

export const useCreateGroup = () =>
  useAction(
    (name: string) => body(api.POST("/api/v1/groups", { body: { name } })),
    touched,
    (name) => `Created group ${name}`,
  );

export const useRenameGroup = () =>
  useAction(
    ({ group: g, name }: { group: string; name: string }) =>
      body(
        api.PATCH("/api/v1/groups/{group}", { ...group(g), body: { name } }),
      ),
    touched,
    (v) => `Renamed to ${v.name}`,
  );

export const useDeleteGroup = () =>
  useAction(
    (g: string) => done(api.DELETE("/api/v1/groups/{group}", group(g))),
    touched,
    (g) => `Removed group ${g}`,
  );

export const useGroupMember = () =>
  useAction(
    ({
      group: g,
      member,
      add,
    }: {
      group: string;
      member: string;
      add: boolean;
    }) =>
      body(
        (add ? api.PUT : api.DELETE)(
          "/api/v1/groups/{group}/members/{member}",
          {
            params: { path: { group: g, member } },
          },
        ),
      ),
    touched,
    (v) =>
      v.add
        ? `Added ${v.member} to ${v.group}`
        : `Took ${v.member} out of ${v.group}`,
  );

export const useGrantGroup = () =>
  useAction(
    ({ group: g, grant }: { group: string; grant: GrantRequest }) =>
      body(
        api.POST("/api/v1/groups/{group}/grants", { ...group(g), body: grant }),
      ),
    touched,
    (v) => `Gave ${v.group} ${v.grant.role}`,
  );

export const useGrantMember = () =>
  useAction(
    ({ member, grant }: { member: string; grant: GrantRequest }) =>
      body(
        api.POST("/api/v1/members/{member}/grants", {
          params: { path: { member } },
          body: grant,
        }),
      ),
    touched,
    (v) => `Gave ${v.member} ${v.grant.role}`,
  );

export const useRevokeGrant = () =>
  useAction(
    (id: number) =>
      done(api.DELETE("/api/v1/grants/{id}", { params: { path: { id } } })),
    touched,
    () => "Ended the grant",
  );

/** A grant's place in words: "the org", "shop", "shop/staging (other)". */
export function grantPlace(g: Grant) {
  let place = g.environment
    ? `${g.project}/${g.environment}`
    : (g.project ?? "the org");
  if (g.kinds?.length) place += ` (${g.kinds.join(", ")})`;
  return place;
}

/** The CLI flags of a grant's place. */
export function grantFlags(g: GrantRequest) {
  return [
    g.project && `-p ${g.project}`,
    g.environment && `-e ${g.environment}`,
    g.kinds?.length && `--kinds ${g.kinds.join(",")}`,
    g.for_hours && `--for ${g.for_hours}h`,
  ]
    .filter(Boolean)
    .join(" ");
}
