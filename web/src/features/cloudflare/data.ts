import {
  type QueryClient,
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
// routes domains with. The platform's (ADR 0062: Dock's address and
// server tunnels, and zones it shares with orgs) are the console's; an
// org's own are its settings'. cloudflare.* audit events refetch the
// lists (data/live.ts). Redirects (ADR 0068) are the level's too; their
// cloudflare_redirect events patch the list in place.

type Schemas = components["schemas"];
export type Cloudflare = Schemas["Cloudflare"];
export type CloudflareZone = Schemas["CloudflareZone"];
export type Redirect = Schemas["CloudflareRedirect"];

/** Whose connections: the platform's or the current org's. */
export type CloudflareScope = "platform" | "org";
const scopeQuery = (scope: CloudflareScope) =>
  scope === "platform" ? { platform: true } : {};

export const cloudflareKey = ["cloudflare"] as const;
export const cloudflareZonesKey = ["cloudflare", "zones"] as const;

export const cloudflareQuery = (scope: CloudflareScope = "platform") =>
  queryOptions({
    queryKey: [...cloudflareKey, scope],
    queryFn: () =>
      list(
        api.GET("/api/v1/cloudflare", {
          params: { query: scopeQuery(scope) },
        }),
      ),
  });

export const useCloudflare = (scope: CloudflareScope) =>
  useSuspenseQuery(cloudflareQuery(scope)).data;

/** A level's redirects, by hostname. */
export const redirectsQuery = (scope: CloudflareScope) =>
  queryOptions({
    queryKey: [...cloudflareKey, "redirects", scope],
    queryFn: () =>
      list(
        api.GET("/api/v1/cloudflare/redirects", {
          params: { query: scopeQuery(scope) },
        }),
      ),
  });

export const useRedirects = (scope: CloudflareScope) =>
  useSuspenseQuery(redirectsQuery(scope)).data;

/**
 * Applies a cloudflare_redirect event: the redirect, whose hostname is
 * unique across levels, is replaced in (or with removed, dropped from)
 * whichever cached list has it. New ones come with their audit event's
 * refetch.
 */
export function applyRedirectEvent(
  queryClient: QueryClient,
  redirect: Redirect,
  removed: boolean,
) {
  queryClient.setQueriesData<Redirect[]>(
    { queryKey: [...cloudflareKey, "redirects"] },
    (old) =>
      old?.some((r) => r.hostname === redirect.hostname)
        ? removed
          ? old.filter((r) => r.hostname !== redirect.hostname)
          : old.map((r) => (r.hostname === redirect.hostname ? redirect : r))
        : old,
  );
}

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

export const useAddCloudflare = (scope: CloudflareScope) =>
  useCloudflareAction((input: Schemas["CreateCloudflareRequest"]) =>
    body(
      api.POST("/api/v1/cloudflare", {
        params: { query: scopeQuery(scope) },
        body: input,
      }),
    ),
  );

export const useUpdateCloudflare = (scope: CloudflareScope) =>
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
          params: { path: { cloudflare: name }, query: scopeQuery(scope) },
          body: change,
        }),
      ),
  );

export const useRefreshCloudflare = (scope: CloudflareScope) =>
  useCloudflareAction((name: string) =>
    body(
      api.POST("/api/v1/cloudflare/{cloudflare}/refresh", {
        params: { path: { cloudflare: name }, query: scopeQuery(scope) },
      }),
    ),
  );

/** Removes a connection; force removes its redirects with it. */
export const useRemoveCloudflare = (scope: CloudflareScope) =>
  useCloudflareAction(({ name, force }: { name: string; force?: boolean }) =>
    done(
      api.DELETE("/api/v1/cloudflare/{cloudflare}", {
        params: {
          path: { cloudflare: name },
          query: { ...scopeQuery(scope), ...(force && { force: true }) },
        },
      }),
    ),
  );

export const useAddRedirect = (scope: CloudflareScope) =>
  useCloudflareAction((input: Schemas["CreateCloudflareRedirectRequest"]) =>
    body(
      api.POST("/api/v1/cloudflare/redirects", {
        params: { query: scopeQuery(scope) },
        body: input,
      }),
    ),
  );

export const useUpdateRedirect = (scope: CloudflareScope) =>
  useCloudflareAction(
    ({
      hostname,
      change,
    }: {
      hostname: string;
      change: Schemas["UpdateCloudflareRedirectRequest"];
    }) =>
      body(
        api.PATCH("/api/v1/cloudflare/redirects/{hostname}", {
          params: { path: { hostname }, query: scopeQuery(scope) },
          body: change,
        }),
      ),
  );

export const useRemoveRedirect = (scope: CloudflareScope) =>
  useCloudflareAction((hostname: string) =>
    done(
      api.DELETE("/api/v1/cloudflare/redirects/{hostname}", {
        params: { path: { hostname }, query: scopeQuery(scope) },
      }),
    ),
  );

export const useRetryRedirect = (scope: CloudflareScope) =>
  useCloudflareAction((hostname: string) =>
    body(
      api.POST("/api/v1/cloudflare/redirects/{hostname}/retry", {
        params: { path: { hostname }, query: scopeQuery(scope) },
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
