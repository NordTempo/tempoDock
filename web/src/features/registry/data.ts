import {
  queryOptions,
  useMutation,
  useQueryClient,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";

// The platform registry for the console's Registry page. registry.* audit
// events refetch it (data/live.ts), so a prune's end shows without polling.

export type Registry = components["schemas"]["Registry"];

export const registryKey = ["registry"] as const;

export const registryQuery = () =>
  queryOptions({
    queryKey: registryKey,
    queryFn: () => body(api.GET("/api/v1/registry")),
  });

export const useRegistry = () => useSuspenseQuery(registryQuery()).data;

/** Starts a prune; the answer (pruning) replaces the cached registry. */
export function usePruneRegistry() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => body(api.POST("/api/v1/registry/prune")),
    onSuccess: (registry) => queryClient.setQueryData(registryKey, registry),
  });
}
