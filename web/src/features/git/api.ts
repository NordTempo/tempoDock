import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { keys } from "../../data/queries";
import type { ServiceRef } from "../../data/types";

// A service's GitHub source (ADR 0018), shared by Settings' Source section
// and the Deploy window.

type Schemas = components["schemas"];
export type GitConnection = Schemas["GitConnection"];
export type Commit = Schemas["Commit"];

export const gitPath =
  "/api/v1/projects/{project}/environments/{environment}/services/{service}/git";

/**
 * The connection, if any; release events refetch it while it is shown
 * and enabled.
 */
export const useGitSource = (ref: ServiceRef, enabled = true) =>
  useQuery({
    queryKey: keys.git(ref),
    queryFn: () => body(api.GET(gitPath, { params: { path: ref } })),
    enabled,
  });

/**
 * Queues a commit as if pushed: `ref` (a SHA, branch or tag), or the
 * connected branch's head when empty.
 */
export function useGitDeploy(ref: ServiceRef) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (gitRef: string) =>
      body(
        api.POST(`${gitPath}/deploy`, {
          params: { path: ref },
          body: gitRef ? { ref: gitRef } : {},
        }),
      ),
    // The queued push and last event changed.
    onSettled: () => client.invalidateQueries({ queryKey: keys.git(ref) }),
  });
}

/** "acme/shop@main in apps/web/". */
export const targetText = (c: GitConnection) =>
  `${c.repo}@${c.branch}${c.dir ? ` in ${c.dir}/` : ""}`;

export const shortSha = (sha: string) => sha.slice(0, 7);

/** "abc1234 fix checkout (ada)", as `dock git` prints a commit. */
export const commitText = (c: Commit) =>
  `${shortSha(c.sha)}${c.message ? ` ${c.message}` : ""}${c.author ? ` (${c.author})` : ""}`;

/** The `dock git deploy` line for a ref. */
export const gitDeployCli = (ref: ServiceRef, gitRef: string) =>
  `dock git deploy${gitRef ? ` --ref ${gitRef}` : ""} -s ${ref.service} -e ${ref.environment}`;
