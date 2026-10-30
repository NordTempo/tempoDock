import { createFileRoute } from "@tanstack/react-router";
import { AnalyticsConsole } from "../features/analytics/AnalyticsConsole";
import {
  analyticsDefaultsQuery,
  analyticsStatusQuery,
} from "../features/analytics/data";

// Dock Analytics for operators: the store, its server and the site
// defaults (ADR 0037).
export const Route = createFileRoute("/_app/console/analytics")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(analyticsStatusQuery()),
      queryClient.ensureQueryData(analyticsDefaultsQuery()),
    ]),
  component: AnalyticsConsole,
});
