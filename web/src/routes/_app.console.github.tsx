import { createFileRoute } from "@tanstack/react-router";
import { githubQuery } from "../features/github/data";
import { GitHubPage } from "../features/github/GitHubPage";

// Dock's GitHub App: its webhook and installations (ADR 0030).
export const Route = createFileRoute("/_app/console/github")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(githubQuery()),
  component: GitHubPage,
});
