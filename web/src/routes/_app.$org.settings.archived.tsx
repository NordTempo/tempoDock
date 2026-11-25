import { createFileRoute } from "@tanstack/react-router";
import { ArchivedPage } from "../features/org/ArchivedPage";

// Settings › Archived projects (ADR 0078, 0099): rename or delete them.
export const Route = createFileRoute("/_app/$org/settings/archived")({
  component: ArchivedPage,
});
