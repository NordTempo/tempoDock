import { queryOptions, useSuspenseQuery } from "@tanstack/react-query";
import { api, list } from "../../api/client";
import { keys } from "../../data/queries";

/**
 * The caller's own tokens (GET /me/tokens), for their account page. Under
 * keys.tokens, so whatever refreshes the org's tokens refreshes these.
 */
export const ownTokensQuery = () =>
  queryOptions({
    queryKey: [...keys.tokens, "mine"],
    queryFn: () => list(api.GET("/api/v1/me/tokens")),
  });

export const useOwnTokens = () => useSuspenseQuery(ownTokensQuery()).data;
