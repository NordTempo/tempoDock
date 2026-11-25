import {
  type QueryClient,
  queryOptions,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/action";
import { currentMe, keys } from "../../data/queries";

// Signing in through one of the platform's identity providers (ADR 0065):
// the sign-in page's buttons, an invite page signed out, and the step-up
// of a session that signed in through one.

type StartRequest = components["schemas"]["SSOStartRequest"];

/**
 * What the sign-in page offers: a button per provider, passwords, or on
 * the public demo (ADR 0100) the demo's shared account.
 */
export const signInMethods = queryOptions({
  queryKey: ["sign-in-methods"],
  queryFn: () => body(api.GET("/api/v1/auth/methods")),
  staleTime: Number.POSITIVE_INFINITY,
});

export const useSignInMethods = () => useQuery(signInMethods);

/**
 * Signs in to the public demo's shared account (ADR 0100), no password:
 * the sign-in page does it on its own, and its button again after a
 * sign-out. 503 while the demo is starting.
 */
export async function demoSignIn(queryClient: QueryClient) {
  await body(api.POST("/api/v1/auth/demo"));
  queryClient.setQueryData(keys.session, await currentMe());
}

/** demoSignIn as the sign-in page's button. */
export const useDemoSignIn = () => {
  const queryClient = useQueryClient();
  return useAction(
    () => demoSignIn(queryClient),
    () => [],
  );
};

/**
 * Starts signing in through a provider: the browser leaves for its page,
 * so the mutation stays pending until it does.
 */
export const useStartSSO = () =>
  useAction(
    async (req: StartRequest) => {
      const res = await body(api.POST("/api/v1/auth/sso/start", { body: req }));
      window.location.assign(res.url);
    },
    () => [],
  );

/** The page to land on after signing in again: this one. */
export const here = () => window.location.pathname + window.location.search;
