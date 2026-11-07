import { queryOptions } from "@tanstack/react-query";
import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";

// The platform's sign-in (ADR 0065, `dock sso`): its identity providers
// and two switches, password sign-in and invite only. Operators only.

export type Provider = components["schemas"]["SSOProvider"];

const providersKey = ["platform", "sso-providers"];
const settingsKey = ["platform", "sign-in"];
// The public sign-in page's buttons (features/sso/data.ts).
const methodsKey = ["sign-in-methods"];

export const ssoProvidersQuery = () =>
  queryOptions({
    queryKey: providersKey,
    queryFn: () => body(api.GET("/api/v1/platform/sso-providers")),
  });

export const signInSettingsQuery = () =>
  queryOptions({
    queryKey: settingsKey,
    queryFn: () => body(api.GET("/api/v1/platform/sign-in")),
  });

/** Adds a provider; an OpenID Connect issuer must answer discovery. */
export const useCreateSSOProvider = () =>
  useAction(
    (req: components["schemas"]["CreateSSOProviderRequest"]) =>
      body(api.POST("/api/v1/platform/sso-providers", { body: req })),
    () => [providersKey, methodsKey, ["audit"]],
    (_, p) => `Added ${p.label}`,
  );

/** Changes a provider; fields left out (a blank client secret) stay. */
export const useUpdateSSOProvider = () =>
  useAction(
    ({
      name,
      ...req
    }: components["schemas"]["UpdateSSOProviderRequest"] & { name: string }) =>
      body(
        api.PATCH("/api/v1/platform/sso-providers/{name}", {
          params: { path: { name } },
          body: req,
        }),
      ),
    () => [providersKey, methodsKey, ["audit"]],
    (_, p) => `Saved ${p.label}`,
  );

/** Removes a provider, its identities and its sessions. */
export const useDeleteSSOProvider = () =>
  useAction(
    (name: string) =>
      done(
        api.DELETE("/api/v1/platform/sso-providers/{name}", {
          params: { path: { name } },
        }),
      ),
    () => [providersKey, methodsKey, ["audit"]],
    (name) => `Removed ${name}`,
  );

/** Changes password sign-in or invite only; what is left out stays. */
export const useSetSignInSettings = () =>
  useAction(
    (req: components["schemas"]["SignInSettingsRequest"]) =>
      body(api.PATCH("/api/v1/platform/sign-in", { body: req })),
    () => [settingsKey, methodsKey, ["audit"]],
  );
