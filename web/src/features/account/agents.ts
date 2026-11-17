import type { components } from "../../api/schema";

type Token = components["schemas"]["Token"];

/**
 * The tokens that are a person's agents now: their `dock login` tokens
 * that are neither revoked nor expired. A revoked login keeps its row and
 * its name (a new login from the same machine gets the same one), and its
 * settings answer 404, so the agent picker must not offer it.
 */
export const liveAgents = <
  T extends Pick<Token, "origin" | "revoked_at" | "expires_at">,
>(
  tokens: T[],
  now = Date.now(),
) =>
  tokens.filter(
    (t) =>
      t.origin === "cli_login" &&
      !t.revoked_at &&
      !(t.expires_at && Date.parse(t.expires_at) < now),
  );
