import type {
  DomainStaging,
  PendingChanges,
  ServiceView,
} from "../../data/types";

/**
 * Services that get a release when an environment's staged changes are
 * applied, as the API reports them (dependents through references too).
 * A service staged for archiving is archived instead, and a staged new
 * service gets its first release only if it runs from an image or a
 * template: a source service waits for `dock deploy` or a git push.
 */
export const affectedServices = (
  changes: PendingChanges,
  services: ServiceView[],
) =>
  changes.affected.flatMap((name) => {
    const view = services.find((s) => s.service.name === name);
    if (!view) return [];
    const { staged, image, template } = view.service;
    if (staged === "archive") return [];
    if (staged === "create" && !image && !template) return [];
    return [view];
  });

/** The release a service gets next: "web r62", "web r1" for a new one. */
export const nextRelease = (s: ServiceView) =>
  `${s.service.name} r${(s.latest_release?.number ?? 0) + 1}`;

/**
 * A staged domain in words: "shop.example.com", "domain (generated)" for
 * one still to be generated, with "redirects to X" for a redirect.
 */
export const stagedDomain = (d: DomainStaging) =>
  `${d.generated || !d.hostname ? "domain (generated)" : `domain ${d.hostname}`}${d.redirect_to ? ` redirects to ${d.redirect_to}` : ""}`;
