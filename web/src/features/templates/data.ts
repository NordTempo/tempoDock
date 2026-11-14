import {
  queryOptions,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { api, body, list } from "../../api/client";
import type { components } from "../../api/schema";
import { envPath, keys } from "../../data/queries";
import type { EnvRef } from "../../data/types";

type Schemas = components["schemas"];
export type AppTemplate = Schemas["AppTemplate"];
export type TemplateInput = Schemas["TemplateInput"];
export type UseTemplate = Schemas["UseTemplateRequest"];

/** The template catalog (ADR 0082); it changes only with a Dock release. */
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
 * their first deploys. The environment's pages pick them up when the
 * mutation settles.
 */
export function useUseTemplate() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
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
    onSettled: (_d, _e, { env }) =>
      Promise.all(
        [keys.overview(env.project), keys.env(env), ["audit"]].map((queryKey) =>
          queryClient.invalidateQueries({ queryKey }),
        ),
      ),
  });
}
