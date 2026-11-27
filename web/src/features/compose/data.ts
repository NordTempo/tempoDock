import { queryOptions, useSuspenseQuery } from "@tanstack/react-query";
import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { envPath, keys } from "../../data/queries";
import type { EnvRef } from "../../data/types";

// The Compose file an environment follows (ADR 0110). Syncs run on
// pushes in the background; each is audited (compose.sync), and live.ts
// refetches the environment's subtree from that event, this query and
// its staged changes included. Nothing polls.

export type ComposeSource = components["schemas"]["ComposeSource"];
export type ComposeSourceRequest =
  components["schemas"]["ComposeSourceRequest"];

const PATH = "/api/v1/projects/{project}/environments/{environment}/compose";

const composeKey = (e: EnvRef) => [...keys.env(e), "compose"];

export const composeQueries = {
  source: (e: EnvRef) =>
    queryOptions({
      queryKey: composeKey(e),
      queryFn: () => body(api.GET(PATH, { params: { path: envPath(e) } })),
    }),
};

/** The followed Compose file, or null. */
export const useComposeSource = (e: EnvRef) =>
  useSuspenseQuery(composeQueries.source(e)).data.source ?? null;

// A sync stages changes: the source, the environment's changes and the
// overview's counts move with it.
const touched = (e: EnvRef) => [
  composeKey(e),
  [...keys.env(e), "changes"],
  keys.overview(e.project),
  ["audit"],
];

/** Follows a Compose file (or changes what is followed) and syncs it. */
export const useFollowCompose = (e: EnvRef) =>
  useAction(
    (input: ComposeSourceRequest) =>
      body(api.PUT(PATH, { params: { path: envPath(e) }, body: input })),
    () => touched(e),
  );

/** Syncs the followed file at its branch's head now. */
export const useSyncCompose = (e: EnvRef) =>
  useAction(
    () => body(api.POST(`${PATH}/sync`, { params: { path: envPath(e) } })),
    () => touched(e),
  );

/** Stops following; staged changes stay. */
export const useUnfollowCompose = (e: EnvRef) =>
  useAction(
    () => done(api.DELETE(PATH, { params: { path: envPath(e) } })),
    () => [composeKey(e), ["audit"]],
  );
