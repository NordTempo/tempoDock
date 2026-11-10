import {
  type QueryClient,
  queryOptions,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { keys, servicePath } from "../../data/queries";
import type { Release, ServiceRef } from "../../data/types";

// Image vulnerability scanning (ADR 0074): the org's Images page, one
// image's page, a service's live image on its overview and a release's
// scan on its page. Scans change through `scan` events (applyScanEvent,
// from data/live.ts); nothing polls. In the initial bundle with live.ts,
// so it holds keys, queries and the event only.

type Schemas = components["schemas"];
export type ImageScan = Schemas["ImageScan"];
export type ImageList = Schemas["ImageList"];
export type ImageListItem = Schemas["ImageListItem"];
export type ImageUse = Schemas["ImageUse"];
export type ImageDetail = Schemas["ImageDetail"];
export type FixGroup = Schemas["FixGroup"];
export type Finding = Schemas["Finding"];
export type FixKind = FixGroup["kind"];

export const imageKeys = {
  all: ["images"] as const,
  /** The Images page: production only, or everywhere. */
  list: (production: boolean) => ["images", "list", production] as const,
  /** One image's page, by its scan ID. */
  image: (id: number) => ["images", "image", id] as const,
};

export const imageQueries = {
  list: (production: boolean) =>
    queryOptions({
      queryKey: imageKeys.list(production),
      queryFn: () =>
        body(
          api.GET("/api/v1/org/images", {
            params: { query: { production } },
          }),
        ),
    }),
  image: (id: number) =>
    queryOptions({
      queryKey: imageKeys.image(id),
      queryFn: () =>
        body(
          api.GET("/api/v1/org/images/{id}", {
            params: { path: { id } },
          }),
        ),
    }),
};

export const useImages = (production: boolean) =>
  useSuspenseQuery(imageQueries.list(production)).data;

export const useImage = (id: number) =>
  useSuspenseQuery(imageQueries.image(id)).data;

/** Queues a service's live image (or release N's) for a scan again. */
export const useScanService = () =>
  useAction(
    ({ ref, release }: { ref: ServiceRef; release?: number }) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/scan",
          { params: { path: servicePath(ref) }, body: { release } },
        ),
      ),
    // Its scan events patch the overview and releases as it moves on.
    () => [imageKeys.all, ["audit"]],
    (_vars, scan) => [`Scan of ${scan.image} queued`, "info"],
  );

/**
 * A scan changed (queued, started, scanned, failed). One event comes per
 * live service running the image; data/live.ts patches that service's
 * overview, and this the rest: the service's releases that run the image,
 * a cached image page (its findings refetch once there are new ones) and
 * the Images list, which refetches (it is small). An in-flight refetch is
 * kept, so the events of one change make one request.
 */
export function applyScanEvent(
  queryClient: QueryClient,
  ref: ServiceRef,
  scan: ImageScan,
) {
  queryClient.setQueriesData<Release>(
    {
      queryKey: keys.service(ref),
      predicate: ({ queryKey }) => queryKey[6] === "release",
    },
    (r) => (r?.scan?.id === scan.id ? { ...r, scan } : r),
  );
  const detail = queryClient.getQueryData<ImageDetail>(
    imageKeys.image(scan.id),
  );
  if (detail && detail.scan.scanned_at !== scan.scanned_at)
    void queryClient.invalidateQueries(
      { queryKey: imageKeys.image(scan.id) },
      { cancelRefetch: false },
    );
  else if (detail)
    queryClient.setQueryData<ImageDetail>(imageKeys.image(scan.id), {
      ...detail,
      scan,
    });
  void queryClient.invalidateQueries(
    { queryKey: ["images", "list"] },
    { cancelRefetch: false },
  );
}
