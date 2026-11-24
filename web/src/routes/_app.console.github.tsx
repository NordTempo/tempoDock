import { createFileRoute } from "@tanstack/react-router";
import { ConsoleTabs } from "../features/console/ConsoleTabs";
import { githubQuery } from "../features/github/data";
import { GitHubPage } from "../features/github/GitHubPage";

// Dock's GitHub App: its webhook and installations (ADR 0031).
export const Route = createFileRoute("/_app/console/github")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(githubQuery()),
  component: Tabbed,
});

/** The page under its sidebar entry's tab row (ConsoleTabs). */
function Tabbed() {
  return (
    <ConsoleTabs>
      <GitHubPage />
    </ConsoleTabs>
  );
}
