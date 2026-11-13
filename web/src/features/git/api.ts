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
 * The repositories the org can connect through Dock's GitHub App (ADR
 * 0031), for the connect form; fetched when the form opens.
 */
export const useGitHubRepos = (enabled: boolean) =>
  useQuery({
    queryKey: ["github", "repos"],
    queryFn: () => body(api.GET("/api/v1/github/repos")),
    enabled,
    staleTime: 60_000,
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

/** owner/name as the API's path parameters. */
const repoParams = (repo: string) => {
  const [owner = "", name = ""] = repo.split("/");
  return { owner, name };
};

/** A repository's branches, the default first; fetched while shown. */
export const useGitHubBranches = (repo: string, enabled = true) =>
  useQuery({
    queryKey: ["github", "branches", repo],
    queryFn: () =>
      body(
        api.GET("/api/v1/github/repos/{owner}/{name}/branches", {
          params: { path: repoParams(repo) },
        }),
      ),
    enabled: enabled && repo.includes("/"),
    staleTime: 60_000,
  });

/** A repository's directories at a branch, app-like ones first. */
export const useGitHubPaths = (repo: string, branch: string, enabled = true) =>
  useQuery({
    queryKey: ["github", "paths", repo, branch],
    queryFn: () =>
      body(
        api.GET("/api/v1/github/repos/{owner}/{name}/paths", {
          params: { path: repoParams(repo), query: { branch } },
        }),
      ),
    enabled: enabled && repo.includes("/") && !!branch,
    staleTime: 60_000,
  });
