import {
  queryOptions,
  useQueryClient,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/action";

// Dock's own address for the console's Address page (ADR 0029). Reading
// it checks the public address's DNS and certificate on the server, so
// "Check again" is a refetch; nothing polls.

export type Address = components["schemas"]["Address"];
export type Wildcard = components["schemas"]["Wildcard"];

export const addressKey = ["address"] as const;

export const addressQuery = () =>
  queryOptions({
    queryKey: addressKey,
    queryFn: () => body(api.GET("/api/v1/address")),
  });

export const useAddress = () => useSuspenseQuery(addressQuery());

/** Sets or moves the public address; the answer replaces the cache. */
export function useSetAddress() {
  const queryClient = useQueryClient();
  return useAction(
    async (hostname: string) => {
      const answer = await body(
        api.PUT("/api/v1/address", { body: { hostname } }),
      );
      queryClient.setQueryData(addressKey, answer);
      return answer;
    },
    () => [],
  );
}

/** Removes the public address; the private one stays. */
export function useRemoveAddress() {
  const queryClient = useQueryClient();
  return useAction(
    async () => {
      const answer = await body(api.DELETE("/api/v1/address"));
      queryClient.setQueryData(addressKey, answer);
      return answer;
    },
    () => [],
  );
}

/**
 * Tries failing wildcard certificates again (ADR 0090), with every other
 * failed Cloudflare change: `dock cloudflare retry`. The worker runs in
 * the background, so the address is read again once it was asked.
 */
export const useRetryWildcards = () =>
  useAction(
    () => body(api.POST("/api/v1/cloudflare/retry")),
    () => [addressKey],
  );
