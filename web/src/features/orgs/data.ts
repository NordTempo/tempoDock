import {
  queryOptions,
  useQuery,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { api, body, done, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";

// The platform console's organizations (ADR 0062): every org with its
// people, size and use, for operators, and what they may do to one
// without being a member, including creating one and inviting its people. org.* audit events and disk readings refetch
// the list (data/live.ts); nothing polls.

type Schemas = components["schemas"];
export type ConsoleOrg = Schemas["OperatorOrg"];
export type OrgUsage = Schemas["OrgUsage"];
export type Member = Schemas["User"];
export type Role = Schemas["OperatorMemberRequest"]["role"];

export const consoleOrgsKey = ["console", "orgs"] as const;
const membersKey = (org: string) => [...consoleOrgsKey, org, "members"];
const invitesKey = (org: string) => [...consoleOrgsKey, org, "invites"];

/**
 * Every org. The Organizations page asks for fresh disk readings
 * (readDisks: volumes); the overview takes the last ones.
 */
export const consoleOrgsQuery = (readDisks = false) =>
  queryOptions({
    queryKey: [...consoleOrgsKey, { readDisks }],
    queryFn: () =>
      list(
        api.GET("/api/v1/console/orgs", {
          params: { query: { read_disks: readDisks } },
        }),
      ),
  });

export const useConsoleOrgs = (readDisks = false) =>
  useSuspenseQuery(consoleOrgsQuery(readDisks)).data;

/** An org's members, loaded when its members are opened. */
export const useConsoleMembers = (org: string) =>
  useQuery({
    queryKey: membersKey(org),
    queryFn: () =>
      list(
        api.GET("/api/v1/console/orgs/{org}/members", {
          params: { path: { org } },
        }),
      ),
  });

/** An org's open invites, loaded when its members are opened. */
export const useConsoleInvites = (org: string) =>
  useQuery({
    queryKey: invitesKey(org),
    queryFn: () =>
      list(
        api.GET("/api/v1/console/orgs/{org}/invites", {
          params: { path: { org } },
        }),
      ),
  });

const path = (org: string) => ({ params: { path: { org } } });
const touches = (org: string) => [
  consoleOrgsKey,
  membersKey(org),
  invitesKey(org),
  ["audit"],
];

/** Creates an org with nobody in it, for the operator to invite people into. */
export const useCreateConsoleOrg = () =>
  useAction(
    (req: Schemas["CreateOrgRequest"]) =>
      body(api.POST("/api/v1/console/orgs", { body: req })),
    ({ slug }) => touches(slug),
    (_, o) => `Created ${o.slug}`,
  );

/** Invites an email into an org; the link's code comes back once. */
export const useCreateConsoleInvite = () =>
  useAction(
    ({ org, ...req }: { org: string } & Schemas["CreateInviteRequest"]) =>
      body(
        api.POST("/api/v1/console/orgs/{org}/invites", {
          ...path(org),
          body: req,
        }),
      ),
    ({ org }) => touches(org),
  );

export const useRevokeConsoleInvite = () =>
  useAction(
    ({ org, id }: { org: string; id: number }) =>
      done(
        api.DELETE("/api/v1/console/orgs/{org}/invites/{id}", {
          params: { path: { org, id } },
        }),
      ),
    ({ org }) => touches(org),
  );

export const useRenameConsoleOrg = () =>
  useAction(
    ({ org, ...change }: { org: string } & Schemas["UpdateOrgRequest"]) =>
      body(
        api.PATCH("/api/v1/console/orgs/{org}", {
          ...path(org),
          body: change,
        }),
      ),
    // The org list too: an operator's own orgs may have moved.
    ({ org }) => [...touches(org), ["orgs"]],
    (_, o) => `Saved ${o.slug}`,
  );

export const useSuspendConsoleOrg = () =>
  useAction(
    ({ org, suspend }: { org: string; suspend: boolean }) =>
      body(
        suspend
          ? api.POST("/api/v1/console/orgs/{org}/suspend", path(org))
          : api.POST("/api/v1/console/orgs/{org}/unsuspend", path(org)),
      ),
    ({ org }) => touches(org),
    ({ org, suspend }) => `${suspend ? "Suspended" : "Unsuspended"} ${org}`,
  );

export const useStopConsoleOrg = () =>
  useAction(
    (org: string) =>
      body(api.POST("/api/v1/console/orgs/{org}/stop", path(org))),
    (org) => touches(org),
    (org, out) =>
      out.failed?.length
        ? [
            `Stopped ${out.stopped} of ${org}'s services; ${out.failed.length} didn't stop`,
            "info",
          ]
        : `Stopped ${out.stopped} services of ${org}`,
  );

export const useDeleteConsoleOrg = () =>
  useAction(
    (org: string) =>
      done(
        api.DELETE("/api/v1/console/orgs/{org}", {
          params: { path: { org }, query: { confirm: org } },
        }),
      ),
    (org) => touches(org),
    (org) => `Deleted ${org}`,
  );

export const useJoinConsoleOrg = () =>
  useAction(
    ({ org, hours }: { org: string; hours: number }) =>
      body(
        api.POST("/api/v1/console/orgs/{org}/join", {
          ...path(org),
          body: { hours },
        }),
      ),
    ({ org }) => [...touches(org), ["orgs"]],
  );

export const useAddConsoleMember = () =>
  useAction(
    ({ org, email, role }: { org: string; email: string; role: Role }) =>
      body(
        api.POST("/api/v1/console/orgs/{org}/members", {
          ...path(org),
          body: { email, role },
        }),
      ),
    ({ org }) => touches(org),
    ({ email, role }) => `Added ${email} as ${role}`,
  );

export const useSetConsoleMemberRole = () =>
  useAction(
    ({ org, user, role }: { org: string; user: number; role: Role }) =>
      body(
        api.PUT("/api/v1/console/orgs/{org}/members/{user}", {
          params: { path: { org, user } },
          body: { role },
        }),
      ),
    ({ org }) => touches(org),
  );

export const useRemoveConsoleMember = () =>
  useAction(
    ({ org, user }: { org: string; user: number }) =>
      done(
        api.DELETE("/api/v1/console/orgs/{org}/members/{user}", {
          params: { path: { org, user } },
        }),
      ),
    ({ org }) => touches(org),
  );
