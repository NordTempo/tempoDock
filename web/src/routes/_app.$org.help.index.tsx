import { createFileRoute, redirect } from "@tanstack/react-router";
import type { TopicId } from "../features/help/topics";

// /help opens Getting around; the old one-page help's anchors
// (#apply, #releases, ...) open the topic that took their place.
const anchors: Record<string, TopicId> = {
  projects: "start",
  releases: "deploying",
  apply: "services",
  locks: "access",
  domains: "domains",
  data: "backups",
  access: "access",
  images: "images",
  connect: "data",
  servers: "servers",
  cli: "cli",
};

export const Route = createFileRoute("/_app/$org/help/")({
  beforeLoad: ({ params, location }) => {
    throw redirect({
      to: "/$org/help/$topic",
      params: { org: params.org, topic: anchors[location.hash] ?? "start" },
      replace: true,
    });
  },
});
