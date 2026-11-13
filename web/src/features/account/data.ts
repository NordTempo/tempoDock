import { queryOptions, useQueryClient } from "@tanstack/react-query";
import { api, body, done } from "../../api/client";
import type { components } from "../../api/schema";
import { createPasskey } from "../../api/webauthn";
import { useAction } from "../../data/mutations";
import { keys } from "../../data/queries";

// The account page's own data: the caller's display name, and two-factor
// sign-in (ADR 0057): the caller's factors, adding passkeys, setting up an
// authenticator app, recovery codes. Two-factor is also read by the
// step-up dialog, which asks for a factor when there is one.

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

/**
 * Adds a passkey named name: a challenge, the browser creates the
 * passkey, the daemon keeps it. The first factor also resolves with
 * recovery codes, shown once.
 */
export const useAddPasskey = () =>
  useAction(async (name: string) => {
    const challenge = await body(api.POST("/api/v1/me/passkeys/options"));
    const passkey = await createPasskey(challenge);
    return body(api.POST("/api/v1/me/passkeys", { body: { name, passkey } }));
  }, factorsChanged);

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

/**
 * Changes the caller's display name (`dock me name`); the answer is who
 * they are now, put straight into the session.
 */
export function useUpdateName() {
  const queryClient = useQueryClient();
  return useAction(
    async (name: string) => {
      const me = await body(api.PATCH("/api/v1/me", { body: { name } }));
      queryClient.setQueryData(keys.session, me);
      return me;
    },
    () => [["audit"]],
  );
}

const myEmailKey = ["me", "email"];

/** The mail the caller may turn off (`dock me email`, ADR 0079). */
export const myEmailQuery = () =>
  queryOptions({
    queryKey: myEmailKey,
    queryFn: () => body(api.GET("/api/v1/me/email")),
  });

/** Turns access request mail on or off. */
export const useSetMyEmail = () =>
  useAction(
    (req: components["schemas"]["MyEmail"]) =>
      body(api.PUT("/api/v1/me/email", { body: req })),
    () => [myEmailKey],
    (req) => `Access request emails ${req.access_requests ? "on" : "off"}`,
  );
