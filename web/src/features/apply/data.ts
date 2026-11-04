import { api, body } from "../../api/client";
import { useAction } from "../../data/mutations";
import { keys } from "../../data/queries";
import type { ApplyRequest, ComposeImportRequest } from "../../data/types";

// Project files (`dock apply FILE`, ADR 0049): plan one against the live
// project, or stage it; and Compose files converted into one. Only the
// Apply file dialog and New project's From Compose use these.

type FileRequest = ApplyRequest & { project: string };

/** The plan for a file: what staging it would do. Reads only. */
export const usePlanFile = () =>
  useAction(
    ({ project, ...request }: FileRequest) =>
      body(
        api.POST("/api/v1/projects/{project}/plan", {
          params: { path: { project } },
          body: request,
        }),
      ),
    () => [],
  );

/**
 * Stages a file in each of its environments (creating missing ones);
 * nothing goes live until an environment's changes are applied.
 */
export const useStageFile = () =>
  useAction(
    ({ project, ...request }: FileRequest) =>
      body(
        api.POST("/api/v1/projects/{project}/apply", {
          params: { path: { project } },
          body: request,
        }),
      ),
    // The project's tree holds its overview, services and staged changes.
    ({ project }) => [keys.project(project), ["audit"]],
  );

/**
 * Converts a Compose file into a project file and plans it; with `apply`
 * it creates the project, stages the file with the secrets' values and
 * applies it (`dock import compose --apply`).
 */
export const useImportCompose = () =>
  useAction(
    ({ project, ...request }: ComposeImportRequest & { project: string }) =>
      body(
        api.POST("/api/v1/projects/{project}/import/compose", {
          params: { path: { project } },
          body: request,
        }),
      ),
    ({ project, apply }) =>
      apply
        ? [keys.projects, keys.project(project), keys.servers, ["audit"]]
        : [],
  );
