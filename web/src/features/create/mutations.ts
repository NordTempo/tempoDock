import { useQueryClient } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/action";
import { envPath, keys, queries, servicePath } from "../../data/queries";
import type { EnvironmentKind, EnvRef, Project } from "../../data/types";

type Schemas = components["schemas"];
export type NewService = Schemas["CreateServiceRequest"];
export type GitConnect = Schemas["GitConnectRequest"];
export type GitConnected = Schemas["GitConnectResult"];

/** Creates an organization with the caller as its admin. */
export function useCreateOrg() {
  return useAction(
    (req: { slug: string; name?: string }) =>
      body(api.POST("/api/v1/orgs", { body: req })),
    () => [keys.orgs],
  );
}

/**
 * Creates a project (and its production environment). Its overview is
 * loaded before it joins the project list, so the sidebar tree and the
 * page it navigates to don't wait on it.
 */
export function useCreateProject() {
  const queryClient = useQueryClient();
  return useAction(
    async (name: string) => {
      const project = await body(
        api.POST("/api/v1/projects", { body: { name } }),
      );
      await queryClient.fetchQuery(queries.overview(project.name));
      queryClient.setQueryData<Project[]>(
        keys.projects,
        (old) => old && [...old.filter((p) => p.id !== project.id), project],
      );
      return project;
    },
    () => [keys.projects, keys.servers, ["audit"]],
  );
}

/** Creates an empty environment; the server places it. */
export const useCreateEnvironment = () =>
  useAction(
    ({
      project,
      name,
      kind,
    }: {
      project: string;
      name: string;
      kind: EnvironmentKind;
    }) =>
      body(
        api.POST("/api/v1/projects/{project}/environments", {
          params: { path: { project } },
          body: { name, kind },
        }),
      ),
    ({ project }) => [keys.project(project), keys.servers, ["audit"]],
  );

/**
 * Creates a service, or a database from a template (which deploys right
 * away). Resolves with the new service.
 */
export const useCreateService = () =>
  useAction(
    ({ env, ...req }: { env: EnvRef } & NewService) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services",
          { params: { path: envPath(env) }, body: req },
        ),
      ),
    ({ env }) => [keys.overview(env.project), keys.env(env), ["audit"]],
  );

/** Connects a service to a GitHub repository (ADR 0018). */
export const useConnectGit = () =>
  useAction(
    ({ env, service, ...req }: { env: EnvRef; service: string } & GitConnect) =>
      body(
        api.PUT(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/git",
          { params: { path: servicePath({ ...env, service }) }, body: req },
        ),
      ),
    ({ env, service }) => [keys.git({ ...env, service }), ["audit"]],
  );
