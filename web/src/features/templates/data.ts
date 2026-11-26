import { queryOptions } from "@tanstack/react-query";
import { api, body, list } from "../../api/client";
import type { components } from "../../api/schema";
import { staging, useAction } from "../../data/mutations";
import { envPath } from "../../data/queries";
import type { EnvRef } from "../../data/types";

type Schemas = components["schemas"];
export type AppTemplate = Schemas["AppTemplate"];
export type TemplateInput = Schemas["TemplateInput"];
export type UseTemplate = Schemas["UseTemplateRequest"];

/**
 * The template catalog (ADR 0083): Dock's, which change only with a
 * release, and the organization's own, which live.ts refetches when one
 * is saved or removed (template.save, template.remove).
 */
export const appTemplates = () =>
  queryOptions({
    queryKey: ["app-templates"],
    queryFn: async () =>
      (await list(api.GET("/api/v1/app-templates"))).map((t) => ({
        ...t,
        inputs: t.inputs ?? [],
        services: t.services ?? [],
      })),
    staleTime: Number.POSITIVE_INFINITY,
  });

/**
 * Uses a template in an environment: creates its services and starts
 * their first deploys (with `detach`, settling once the services exist).
 * The environment's pages pick them up when the mutation settles.
 */
export const useUseTemplate = () =>
  useAction(
    ({
      env,
      template,
      ...req
    }: { env: EnvRef; template: string } & UseTemplate) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/app-templates/{template}",
          { params: { path: { ...envPath(env), template } }, body: req },
        ),
      ),
    ({ env }) => staging(env),
  );

/** Saves an environment's services as the organization's template. */
export const useSaveTemplateFrom = () =>
  useAction(
    ({ env, ...req }: { env: EnvRef } & Schemas["SaveTemplateFromRequest"]) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/app-templates",
          { params: { path: envPath(env) }, body: req },
        ),
      ),
    () => [appTemplates().queryKey],
  );

/** Removes an organization template; services made from it stay. */
export const useRemoveTemplate = () =>
  useAction(
    async (template: string) => {
      const { error } = await api.DELETE("/api/v1/app-templates/{template}", {
        params: { path: { template } },
      });
      if (error) throw error;
    },
    () => [appTemplates().queryKey],
  );
