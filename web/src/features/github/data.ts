import {
  queryOptions,
  useMutation,
  useQueryClient,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";

// Dock's GitHub App for the console's GitHub page (ADR 0030). Reading it
// asks GitHub for the App's installations now, so a refetch is "check
// again"; nothing polls. Creating the App and installing it continue on
// github.com, which sends the browser back to Dock.

export type GitHubStatus = components["schemas"]["GitHubStatus"];
export type GitHubInstallation = components["schemas"]["GitHubInstallation"];

export const githubKey = ["github"] as const;

export const githubQuery = () =>
  queryOptions({
    queryKey: githubKey,
    queryFn: () => body(api.GET("/api/v1/github")),
  });

export const useGitHub = () => useSuspenseQuery(githubQuery());

/** Opens a page in this tab: GitHub sends the browser back afterwards. */
const go = (link: { url: string }) => window.location.assign(link.url);

/** Starts creating the App: the answer is Dock's page that posts the manifest. */
export const useCreateApp = () =>
  useMutation({
    mutationFn: (owner: string) =>
      body(api.POST("/api/v1/github/app", { body: owner ? { owner } : {} })),
    onSuccess: go,
  });

/** Opens the App's install page on GitHub for an org. */
export const useInstall = () =>
  useMutation({
    mutationFn: (org: string) =>
      body(api.POST("/api/v1/github/installations", { body: { org } })),
    onSuccess: go,
  });

/** Assigns an installation to an org, or unassigns it (org ""). */
export function useAssign() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, org }: { id: number; org: string }) => {
      const path = { params: { path: { id } } };
      return org
        ? done(
            api.PUT("/api/v1/github/installations/{id}", {
              ...path,
              body: { org },
            }),
          )
        : done(api.DELETE("/api/v1/github/installations/{id}", path));
    },
    onSuccess: () => queryClient.invalidateQueries({ queryKey: githubKey }),
  });
}

/** Forgets the App. */
export function useRemoveApp() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => done(api.DELETE("/api/v1/github/app")),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: githubKey }),
  });
}
