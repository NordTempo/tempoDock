import { queryOptions, useSuspenseQuery } from "@tanstack/react-query";
import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/action";

// Dock's GitHub App for the console's GitHub page (ADR 0031, 0066). Reading it
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
  useAction(
    async (req: components["schemas"]["GitHubAppRequest"]) =>
      go(await body(api.POST("/api/v1/github/app", { body: req }))),
    () => [],
  );

/** Gives an App made before ADR 0066 its client secret, so installs are checked. */
export const useSetClientSecret = () =>
  useAction(
    (client_secret: string) =>
      done(
        api.PUT("/api/v1/github/app/client-secret", {
          body: { client_secret },
        }),
      ),
    () => [githubKey, ["audit"]],
  );

/** Opens the App's install page on GitHub for an org. */
export const useInstall = () =>
  useAction(
    async (org: string) =>
      go(
        await body(api.POST("/api/v1/github/installations", { body: { org } })),
      ),
    () => [],
  );

/** Assigns an installation to an org, or unassigns it (org ""). */
export const useAssign = () =>
  useAction(
    ({ id, org }: { id: number; org: string }) => {
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
    () => [githubKey, ["audit"]],
  );

/** Forgets the App. */
export const useRemoveApp = () =>
  useAction(
    () => done(api.DELETE("/api/v1/github/app")),
    () => [githubKey, ["audit"]],
  );

/** The org's view of GitHub: the platform's App and its own installations. */
export const githubOrgQuery = () =>
  queryOptions({
    queryKey: [...githubKey, "org"],
    queryFn: () => body(api.GET("/api/v1/github/org")),
  });

/** Opens the App's install page on GitHub for the caller's own org. */
export const useInstallForOrg = () =>
  useAction(
    async () =>
      go(await body(api.POST("/api/v1/github/installations", { body: {} }))),
    () => [],
  );
