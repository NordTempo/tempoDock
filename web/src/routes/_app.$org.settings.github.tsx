import { createFileRoute } from "@tanstack/react-router";
import { githubOrgQuery } from "../features/github/data";
import { OrgGitHubPage } from "../features/github/OrgGitHubPage";

// The org's GitHub installations (ADR 0062).
export const Route = createFileRoute("/_app/$org/settings/github")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(githubOrgQuery()),
  component: OrgGitHubPage,
});
