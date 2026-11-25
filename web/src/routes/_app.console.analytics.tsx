import { createFileRoute } from "@tanstack/react-router";
import { AnalyticsConsole } from "../features/analytics/AnalyticsConsole";
import {
  analyticsDefaultsQuery,
  analyticsStatusQuery,
  tracingStatusQuery,
} from "../features/analytics/loaders";
import { ConsoleTabs } from "../features/console/ConsoleTabs";

// Dock Analytics for operators: the store, its server and the site
// defaults (ADR 0037), and tracing's trace store beside it.
export const Route = createFileRoute("/_app/console/analytics")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(analyticsStatusQuery()),
      queryClient.ensureQueryData(analyticsDefaultsQuery()),
      queryClient.ensureQueryData(tracingStatusQuery()),
    ]),
  component: Tabbed,
});

/** The page under its sidebar entry's tab row (ConsoleTabs). */
function Tabbed() {
  return (
    <ConsoleTabs>
      <AnalyticsConsole />
    </ConsoleTabs>
  );
}
