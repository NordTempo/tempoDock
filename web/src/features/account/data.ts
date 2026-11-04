import { queryOptions } from "@tanstack/react-query";
import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { keys } from "../../data/queries";

// Two-factor sign-in on the account page (ADR 0048): the caller's
// factors, setting up an authenticator app, recovery codes. Also read by
// the step-up dialog, which asks for the factor when there is one.

export type TwoFactor = components["schemas"]["TwoFactor"];
export type Factor = components["schemas"]["Factor"];
export type TOTPSetup = components["schemas"]["TOTPSetup"];

const twoFactorKey = ["two-factor"];

/** The caller's second factors and unused recovery codes (`dock me two-factor`). */
export const twoFactorQuery = () =>
  queryOptions({
    queryKey: twoFactorKey,
    queryFn: async () => {
      const tf = await body(api.GET("/api/v1/me/two-factor"));
      return { ...tf, factors: tf.factors ?? [] };
    },
  });

// Adding or removing a factor changes what the session counts as
// (Me.two_factor, two_factor_required), and the org may open up.
const factorsChanged = () => [twoFactorKey, keys.session, keys.orgs];

/** Starts adding an authenticator app: a new key, kept once confirmed. */
export const useStartTOTP = () =>
  useAction(
    () => body(api.POST("/api/v1/me/totp")),
    () => [],
  );

/**
 * Keeps the authenticator app with a code from it. The first factor also
 * resolves with recovery codes, shown once.
 */
export const useConfirmTOTP = () =>
  useAction(
    (req: components["schemas"]["AddTOTPRequest"]) =>
      body(api.POST("/api/v1/me/totp/confirm", { body: req })),
    factorsChanged,
  );

/** Removes a second factor; the last one turns two-factor sign-in off. */
export const useRemoveFactor = () =>
  useAction(
    (id: number) =>
      done(api.DELETE("/api/v1/me/factors/{id}", { params: { path: { id } } })),
    factorsChanged,
  );

/** New recovery codes, shown once; the old ones stop working. */
export const useNewRecoveryCodes = () =>
  useAction(
    async () => (await body(api.POST("/api/v1/me/recovery-codes"))).codes ?? [],
    () => [twoFactorKey],
  );
