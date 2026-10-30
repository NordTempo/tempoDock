import {
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { api, body, done, list } from "../../api/client";
import type { components } from "../../api/schema";
import { keys } from "../../data/queries";

// Cloudflare connections (ADR 0036): API tokens Dock runs tunnels and
// routes domains with. cloudflare.* audit events refetch the list
// (data/live.ts).

type Schemas = components["schemas"];
export type Cloudflare = Schemas["Cloudflare"];
export type CloudflareZone = Schemas["CloudflareZone"];

export const cloudflareKey = ["cloudflare"] as const;
export const cloudflareZonesKey = ["cloudflare", "zones"] as const;

export const cloudflareQuery = () =>
  queryOptions({
    queryKey: cloudflareKey,
    queryFn: () => list(api.GET("/api/v1/cloudflare")),
  });

export const useCloudflare = () => useSuspenseQuery(cloudflareQuery()).data;

/** The zones domains can be added from, for the Add domain dialog. */
export const useCloudflareZones = () =>
  useQuery({
    queryKey: cloudflareZonesKey,
    queryFn: () => list(api.GET("/api/v1/cloudflare/zones")),
  });

/** A connection mutation: refetches the connections, zones and servers. */
function useCloudflareAction<Vars, Data>(
  mutationFn: (vars: Vars) => Promise<Data>,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn,
    onSettled: () =>
      Promise.all(
        [cloudflareKey, keys.servers, ["audit"]].map((queryKey) =>
          queryClient.invalidateQueries({ queryKey }),
        ),
      ),
  });
}

export const useAddCloudflare = () =>
  useCloudflareAction((input: Schemas["CreateCloudflareRequest"]) =>
    body(api.POST("/api/v1/cloudflare", { body: input })),
  );

export const useUpdateCloudflare = () =>
  useCloudflareAction(
    ({
      name,
      change,
    }: {
      name: string;
      change: Schemas["UpdateCloudflareRequest"];
    }) =>
      body(
        api.PATCH("/api/v1/cloudflare/{cloudflare}", {
          params: { path: { cloudflare: name } },
          body: change,
        }),
      ),
  );

export const useRefreshCloudflare = () =>
  useCloudflareAction((name: string) =>
    body(
      api.POST("/api/v1/cloudflare/{cloudflare}/refresh", {
        params: { path: { cloudflare: name } },
      }),
    ),
  );

export const useRemoveCloudflare = () =>
  useCloudflareAction((name: string) =>
    done(
      api.DELETE("/api/v1/cloudflare/{cloudflare}", {
        params: { path: { cloudflare: name } },
      }),
    ),
  );

export const useSetServerTunnel = () =>
  useCloudflareAction(
    ({
      server,
      action,
      cloudflare,
    }: {
      server: string;
      action: "create" | "adopt";
      cloudflare?: string;
    }) =>
      body(
        api.POST("/api/v1/servers/{server}/tunnel", {
          params: { path: { server } },
          body: { action, ...(cloudflare && { cloudflare }) },
        }),
      ),
  );

/** Tries every failed tunnel and domain in Cloudflare again. */
export const useRetryCloudflare = () =>
  useCloudflareAction(() => body(api.POST("/api/v1/cloudflare/retry")));
