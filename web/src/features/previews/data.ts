import { queryOptions } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { keys } from "../../data/queries";
import type { EnvRef } from "../../data/types";

// Preview environments per pull request (ADR 0056): a project's preview
// settings and open previews. live.ts refetches them on preview audit
// events and on releases and domains of preview environments.

type Schemas = components["schemas"];
export type Preview = Omit<Schemas["Preview"], "urls"> & {
  urls: Schemas["PreviewURL"][];
};
export type PreviewSettings = Omit<
  Schemas["PreviewSettings"],
  "copy_variables" | "seed_from_snapshot" | "previews"
> & {
  copy_variables: string[];
  seed_from_snapshot: string[];
  previews: Preview[];
};
export type PreviewRequest = Schemas["SetPreviewsRequest"];
export type PullRequestRef = Schemas["PullRequestRef"];

/** The default and the most open previews a project may have. */
export const PREVIEW_DEFAULT_MAX = 3;
export const PREVIEW_MAX = 20;

const toPreview = (p: Schemas["Preview"]): Preview => ({
  ...p,
  urls: p.urls ?? [],
});

const toSettings = (s: Schemas["PreviewSettings"]): PreviewSettings => ({
  ...s,
  copy_variables: s.copy_variables ?? [],
  seed_from_snapshot: s.seed_from_snapshot ?? [],
  previews: (s.previews ?? []).map(toPreview),
});

export const previewQueries = {
  /** Whether previews are on, their settings and the open previews. */
  settings: (project: string) =>
    queryOptions({
      queryKey: keys.previews(project),
      queryFn: () =>
        body(
          api.GET("/api/v1/projects/{project}/previews", {
            params: { path: { project } },
          }),
        ).then(toSettings),
    }),
};

/** What setting or closing previews changes on screen. */
const touched = (project: string) => [
  keys.previews(project),
  keys.overview(project),
  ["audit"],
];

/** Turns previews on, or changes their settings (admin, whole project). */
export const useSetPreviews = () =>
  useAction(
    ({ project, req }: { project: string; req: PreviewRequest }) =>
      body(
        api.PUT("/api/v1/projects/{project}/previews", {
          params: { path: { project } },
          body: req,
        }),
      ).then(toSettings),
    ({ project }) => touched(project),
  );

/** Turns previews off; open ones stay until their pull requests close. */
export const useDisablePreviews = () =>
  useAction(
    (project: string) =>
      body(
        api.DELETE("/api/v1/projects/{project}/previews", {
          params: { path: { project } },
        }),
      ).then(toSettings),
    (project) => touched(project),
  );

/** Closes a preview: its environment is archived and leaves the lists. */
export const useClosePreview = () =>
  useAction(
    (env: EnvRef) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/preview/close",
          {
            params: {
              path: { project: env.project, environment: env.environment },
            },
          },
        ),
      ).then(toPreview),
    (env) => [...touched(env.project), keys.servers],
  );

/** `dock environment previews ...` with the project flag. */
export const previewsCli = (rest: string, project: string) =>
  `dock environment previews ${rest} -p ${project}`;
