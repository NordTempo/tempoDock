import { createFileRoute } from "@tanstack/react-router";
import { FeaturesPage } from "../features/console/FeaturesPage";
import { featuresQuery } from "../features/console/features";

// The optional features and their switches (ADR 0101).
export const Route = createFileRoute("/_app/console/features")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(featuresQuery()),
  component: FeaturesPage,
});
