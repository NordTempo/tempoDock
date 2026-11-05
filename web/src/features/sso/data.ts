import { useMutation } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";

// Signing in through an organization's single sign-on (ADR 0058): from
// the sign-in page, the step-up dialog of an SSO session, and the notice
// of an organization that requires it.

export type SSOChoice = components["schemas"]["SSOChoice"];
type StartRequest = components["schemas"]["SSOStartRequest"];

/**
 * Starts a single sign-on. The browser leaves for the identity provider
 * (resolves null), or, when several organizations or providers sign the
 * email's domain in, resolves with them to choose from: start again
 * naming one. `only` keeps the choices of one organization and starts
 * its provider right away when it has just one.
 */
async function startSSO(
  req: StartRequest,
  only?: string,
): Promise<SSOChoice[] | null> {
  const res = await body(api.POST("/api/v1/auth/sso/start", { body: req }));
  if (res.url) {
    window.location.assign(res.url);
    return null;
  }
  const choices = (res.choices ?? []).filter((c) => !only || c.org === only);
  const [one] = choices;
  if (only && one && choices.length === 1) {
    return startSSO({ org: one.org, provider: one.provider, to: req.to });
  }
  return choices;
}

/** startSSO as a mutation: pending until the browser leaves. */
export const useStartSSO = () =>
  useMutation({
    mutationFn: ({ only, ...req }: StartRequest & { only?: string }) =>
      startSSO(req, only),
  });

/** The page to land on after signing in again: this one. */
export const here = () => window.location.pathname + window.location.search;
