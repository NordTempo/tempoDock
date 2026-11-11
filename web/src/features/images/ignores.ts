import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { canDeploy } from "../../data/queries";
import type { Session } from "../../data/types";
import { imageKeys, type VulnIgnore } from "./data";

// Ignores (ADR 0074, phase 2): made from a finding on an image's page,
// listed there (Ignored) and on the org's Ignores page, removed from
// either. They apply when findings are read, so every image view, the
// overviews and the alerts read again after a change.

export type IgnoreRequest = components["schemas"]["IgnoreVulnRequest"];
export type IgnoreScope = IgnoreRequest["scope"];

/** What an ignore changes: every image view, overviews, alerts, audit. */
const touched = () => [imageKeys.all, ["project"], ["alerts"], ["audit"]];

export const useIgnoreVuln = () =>
  useAction(
    (req: IgnoreRequest) =>
      body(api.POST("/api/v1/org/vulnerabilities/ignores", { body: req })),
    touched,
    (_req, ig) => `Ignoring ${ig.vulnerability} in ${where(ig)}`,
  );

export const useRemoveIgnore = () =>
  useAction(
    (ig: VulnIgnore) =>
      done(
        api.DELETE("/api/v1/org/vulnerabilities/ignores/{id}", {
          params: { path: { id: ig.id } },
        }),
      ),
    touched,
    (ig) => `${ig.vulnerability} counts again in ${where(ig)}`,
  );

/** Where an ignore applies, in words: "web in production", "shop". */
export function where(ig: {
  scope: IgnoreScope;
  project?: string;
  environment?: string;
  service?: string;
}) {
  switch (ig.scope) {
    case "service":
      return `${ig.service} in ${ig.environment}`;
    case "project":
      return `every service of ${ig.project}`;
  }
  return "the whole organization";
}

/** Whether the session may make or remove an ignore there. */
export function mayIgnore(
  session: Session | null,
  ig: { scope: IgnoreScope; project?: string; environment?: string },
) {
  if (!session || session.scope === "read") return false;
  switch (ig.scope) {
    case "service":
      return canDeploy(session, {
        project: ig.project ?? "",
        environment: ig.environment ?? "",
      });
    case "project":
      return (
        !session.environment &&
        (!session.project || session.project === ig.project)
      );
  }
  return session.scope === "admin" && !session.project;
}

/** The CLI for an ignore: `dock vulns ignore CVE-… --package sharp …`. */
export function ignoreCli(req: IgnoreRequest) {
  return [
    `dock vulns ignore ${req.vulnerability}`,
    req.package && `--package ${req.package}`,
    req.scope === "service" &&
      `-p ${req.project} -e ${req.environment} -s ${req.service}`,
    req.scope === "project" && `--in project -p ${req.project}`,
    req.scope === "org" && "--in org",
    req.always
      ? "--always"
      : req.days && req.days !== 90 && `--days ${req.days}`,
    `--reason ${JSON.stringify(req.reason || "…")}`,
  ]
    .filter(Boolean)
    .join(" ");
}
