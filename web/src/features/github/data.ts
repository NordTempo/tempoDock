import { queryOptions, useSuspenseQuery } from "@tanstack/react-query";
import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/action";

// GitHub Apps for the console's GitHub page and the org's GitHub settings
// (ADR 0031, 0066, 0105): the platform's Apps and each org's own. Reading
// them asks GitHub for the Apps' installations now, so a refetch is "check
// again"; nothing polls. Creating an App and installing it continue on
// github.com, which sends the browser back to Dock.

export type GitHubStatus = components["schemas"]["GitHubStatus"];
export type GitHubAppStatus = components["schemas"]["GitHubAppStatus"];
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

/**
 * Starts creating an App, the platform's or the org's own: the answer is
 * Dock's page that posts the manifest.
 */
export const useCreateApp = (platform: boolean) =>
  useAction(
    async (req: components["schemas"]["GitHubAppRequest"]) =>
      go(
        await body(
          api.POST("/api/v1/github/apps", {
            params: { query: { platform } },
            body: req,
          }),
        ),
      ),
    () => [],
  );

/** Gives a platform App made before ADR 0066 its client secret, so installs are checked. */
export const useSetClientSecret = () =>
  useAction(
    ({ slug, secret }: { slug: string; secret: string }) =>
      done(
        api.PUT("/api/v1/github/apps/{slug}/client-secret", {
          params: { path: { slug } },
          body: { client_secret: secret },
        }),
      ),
    () => [githubKey, ["audit"]],
  );

/**
 * Opens an App's install page on GitHub, for an org (empty: the caller's
 * own).
 */
export const useInstall = () =>
  useAction(
    async (req: components["schemas"]["GitHubInstallRequest"]) =>
      go(await body(api.POST("/api/v1/github/installations", { body: req }))),
    () => [],
  );

/** Assigns a platform App's installation to an org, or unassigns it (org ""). */
export const useAssign = () =>
  useAction(
    ({ id, org, app }: { id: number; org: string; app?: string }) => {
      const path = { params: { path: { id } } };
      return org
        ? done(
            api.PUT("/api/v1/github/installations/{id}", {
              ...path,
              body: { org, app },
            }),
          )
        : done(api.DELETE("/api/v1/github/installations/{id}", path));
    },
    () => [githubKey, ["audit"]],
  );

/** Forgets an App. */
export const useRemoveApp = () =>
  useAction(
    (slug: string) =>
      done(
        api.DELETE("/api/v1/github/apps/{slug}", {
          params: { path: { slug } },
        }),
      ),
    () => [githubKey, ["audit"]],
  );

/** The org's view of GitHub: its own Apps and its installations of the platform's. */
export const githubOrgQuery = () =>
  queryOptions({
    queryKey: [...githubKey, "org"],
    queryFn: () => body(api.GET("/api/v1/github/org")),
  });
