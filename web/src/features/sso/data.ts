import { useQuery } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/action";

// Signing in through one of the platform's identity providers (ADR 0065):
// the sign-in page's buttons, an invite page signed out, and the step-up
// of a session that signed in through one.

type StartRequest = components["schemas"]["SSOStartRequest"];

/** What the sign-in page offers: a button per provider, and passwords. */
export const useSignInMethods = () =>
  useQuery({
    queryKey: ["sign-in-methods"],
    queryFn: () => body(api.GET("/api/v1/auth/methods")),
    staleTime: Number.POSITIVE_INFINITY,
  });

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
