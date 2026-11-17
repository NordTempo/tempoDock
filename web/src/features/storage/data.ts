import { queryOptions, useSuspenseQuery } from "@tanstack/react-query";
import { api, body, done, list } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/action";
import { registryKey } from "../registry/data";

// Storage connections (ADR 0034): S3-compatible buckets defined once and
// used by what keeps data in object storage. storage.* audit events
// refetch the list (data/live.ts).

type Schemas = components["schemas"];
export type Storage = Schemas["Storage"];
export type NewStorage = Schemas["CreateStorageRequest"];
export type StorageChange = Schemas["UpdateStorageRequest"];

export const storageKey = ["storage"] as const;

export const storageQuery = () =>
  queryOptions({
    queryKey: storageKey,
    queryFn: () => list(api.GET("/api/v1/storage")),
  });

export const useStorage = () => useSuspenseQuery(storageQuery()).data;

/** A storage mutation: refetches the list and the registry, which names it. */
const useStorageAction = <Vars, Data>(
  mutationFn: (vars: Vars) => Promise<Data>,
) => useAction(mutationFn, () => [storageKey, registryKey, ["audit"]]);

export const useAddStorage = () =>
  useStorageAction((input: NewStorage) =>
    body(api.POST("/api/v1/storage", { body: input })),
  );

export const useUpdateStorage = () =>
  useStorageAction(
    ({ name, change }: { name: string; change: StorageChange }) =>
      body(
        api.PATCH("/api/v1/storage/{storage}", {
          params: { path: { storage: name } },
          body: change,
        }),
      ),
  );

export const useRemoveStorage = () =>
  useStorageAction((name: string) =>
    done(
      api.DELETE("/api/v1/storage/{storage}", {
        params: { path: { storage: name } },
      }),
    ),
  );
