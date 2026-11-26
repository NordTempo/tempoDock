import { queryOptions, useQuery } from "@tanstack/react-query";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/action";

// The console's Features page (ADR 0101): the optional features with what
// each takes, and their switches. Memory is the last minute's sample and
// disk each server's last disk reading, so the list is read again a minute
// later at most; a switch reads it again at once.

export type Feature = components["schemas"]["Feature"];
export type FeatureKey = Feature["key"];

/** The Features page's query; live.ts refetches it when a feature or a
 * server's disk changes. */
export const featuresKey = ["features"] as const;

export const featuresQuery = () =>
  queryOptions({
    queryKey: featuresKey,
    queryFn: () => body(api.GET("/api/v1/features")),
    staleTime: 60_000,
  });

export const useFeatures = () => useQuery(featuresQuery());

/** What a switch asks for: on, or off keeping or removing the data. */
export type FeatureSwitch = {
  key: FeatureKey;
  enabled: boolean;
  removeData: boolean;
};

/**
 * Turns a feature on or off through its own endpoint, and reads the
 * features and that feature's own page again.
 */
export const useSwitchFeature = () =>
  useAction(
    async ({ key, enabled, removeData }: FeatureSwitch) => {
      switch (key) {
        case "scanning":
          await body(
            api.PUT("/api/v1/scanning", {
              body: { enabled, remove_database: removeData },
            }),
          );
          return;
        case "analytics":
          await body(
            api.PUT("/api/v1/analytics/enabled", {
              body: { enabled, remove_data: removeData },
            }),
          );
          return;
        case "tracing":
          await body(api.PUT("/api/v1/tracing", { body: { enabled } }));
      }
    },
    () => [featuresKey, ["analytics"], ["scanning"], ["audit"]],
  );
