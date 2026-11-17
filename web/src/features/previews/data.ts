import { queryOptions } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { keys } from "../../data/queries";
import type { EnvRef, ServiceRef } from "../../data/types";

// Preview environments per pull request (ADR 0056, 0077): a project's
// previews (which services build pull requests, each base's numbers, the
// open previews), a service's and an environment's settings. live.ts
// refetches them on preview audit events and on releases and domains of
// preview environments.

type Schemas = components["schemas"];
export type Preview = Omit<Schemas["Preview"], "urls" | "databases"> & {
  urls: Schemas["PreviewURL"][];
  databases: Schemas["PreviewDatabase"][];
};
export type ServicePreviews = Omit<
  Schemas["ServicePreviews"],
  "copy_variables"
> & { copy_variables: string[] };
export type EnvironmentPreviews = Omit<
  Schemas["EnvironmentPreviews"],
  "copy_shared"
> & { copy_shared: string[] };
export type PreviewService = Omit<
  Schemas["PreviewService"],
  "copy_variables"
> & {
  copy_variables: string[];
};
export type PreviewEnvironment = Omit<
  Schemas["PreviewEnvironment"],
  "copy_shared"
> & { copy_shared: string[] };
export type PreviewSettings = Omit<
  Schemas["PreviewSettings"],
  | "copy_variables"
  | "seed_from_snapshot"
  | "previews"
  | "services"
  | "environments"
> & {
  copy_variables: string[];
  seed_from_snapshot: string[];
  previews: Preview[];
  services: PreviewService[];
  environments: PreviewEnvironment[];
};
export type PullRequestRef = Schemas["PullRequestRef"];
export type PreviewData = Schemas["ServicePreviews"]["data"];
export type PreviewDatabase = Schemas["PreviewDatabase"];

/** What a data setting starts a preview's volumes with, in words. */
export const previewDataText = (d: PreviewData) =>
  ({
    empty: "empty",
    snapshot: "newest snapshot",
    copy: "copy",
    masked: "masked copy",
  })[d];

/** The default and the most open previews a project may have. */
export const PREVIEW_DEFAULT_MAX = 3;
/** Minutes without requests before a preview sleeps, by default. */
export const SLEEP_DEFAULT = 30;
export const PREVIEW_MAX = 20;

const toPreview = (p: Schemas["Preview"]): Preview => ({
  ...p,
  urls: p.urls ?? [],
  databases: p.databases ?? [],
});

const toServicePreviews = (s: Schemas["ServicePreviews"]): ServicePreviews => ({
  ...s,
  copy_variables: s.copy_variables ?? [],
});
const toEnvironmentPreviews = (
  e: Schemas["EnvironmentPreviews"],
): EnvironmentPreviews => ({ ...e, copy_shared: e.copy_shared ?? [] });

const toSettings = (s: Schemas["PreviewSettings"]): PreviewSettings => ({
  ...s,
  copy_variables: s.copy_variables ?? [],
  seed_from_snapshot: s.seed_from_snapshot ?? [],
  previews: (s.previews ?? []).map(toPreview),
  services: (s.services ?? []).map((p) => ({
    ...p,
    copy_variables: p.copy_variables ?? [],
  })),
  environments: (s.environments ?? []).map((e) => ({
    ...e,
    copy_shared: e.copy_shared ?? [],
  })),
});

const servicePreviewsPath =
  "/api/v1/projects/{project}/environments/{environment}/services/{service}/previews";
const environmentPreviewsPath =
  "/api/v1/projects/{project}/environments/{environment}/previews";

export const previewQueries = {
  /** Who builds pull requests, each base's numbers, open previews. */
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
  /** What one service does in previews (under the project's key). */
  service: (ref: ServiceRef) =>
    queryOptions({
      queryKey: [
        ...keys.previews(ref.project),
        "service",
        ref.environment,
        ref.service,
      ],
      queryFn: () =>
        body(api.GET(servicePreviewsPath, { params: { path: ref } })).then(
          toServicePreviews,
        ),
    }),
  /** The numbers of previews cloned from an environment. */
  environment: (env: EnvRef) =>
    queryOptions({
      queryKey: [...keys.previews(env.project), "environment", env.environment],
      queryFn: () =>
        body(
          api.GET(environmentPreviewsPath, {
            params: {
              path: { project: env.project, environment: env.environment },
            },
          }),
        ).then(toEnvironmentPreviews),
    }),
};

/** What setting or closing previews changes on screen. */
const touched = (project: string) => [
  keys.previews(project),
  keys.overview(project),
  ["audit"],
];

/** Sets what a service does in previews (admin of its environment). */
export const useSetServicePreviews = () =>
  useAction(
    ({
      ref,
      req,
    }: {
      ref: ServiceRef;
      req: Schemas["SetServicePreviewsRequest"];
    }) =>
      body(
        api.PUT(servicePreviewsPath, { params: { path: ref }, body: req }),
      ).then(toServicePreviews),
    ({ ref }) => touched(ref.project),
  );

/** Sets the numbers of previews cloned from an environment (admin). */
export const useSetEnvironmentPreviews = () =>
  useAction(
    ({
      env,
      req,
    }: {
      env: EnvRef;
      req: Schemas["SetEnvironmentPreviewsRequest"];
    }) =>
      body(
        api.PUT(environmentPreviewsPath, {
          params: {
            path: { project: env.project, environment: env.environment },
          },
          body: req,
        }),
      ).then(toEnvironmentPreviews),
    ({ env }) => touched(env.project),
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

/**
 * Copies a preview's databases from its base again (ADR 0092): they are
 * emptied and filled, then its other services restart.
 */
export const useRefreshPreviewData = () =>
  useAction(
    (env: EnvRef) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/preview/refresh-data",
          {
            params: {
              path: { project: env.project, environment: env.environment },
            },
            body: {},
          },
        ),
      ).then(toPreview),
    (env) => touched(env.project),
  );

/** `dock environment previews ...` with the project flag. */
export const previewsCli = (rest: string, project: string) =>
  `dock environment previews ${rest} -p ${project}`;

/** `dock service previews ...` for a service. */
export const servicePreviewsCli = (rest: string, ref: ServiceRef) =>
  `dock service previews ${rest}${rest ? " " : ""}-s ${ref.service} -e ${ref.environment} -p ${ref.project}`;

/** Minutes as people say them: "30 min", "2h". */
export const minutes = (m: number) =>
  m % 60 === 0 ? `${m / 60}h` : `${m} min`;
