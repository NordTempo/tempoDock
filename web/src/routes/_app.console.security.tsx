import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { ConsoleTabs } from "../features/console/ConsoleTabs";
import { hostChecksQuery } from "../features/security/data";
import { SecurityPage } from "../features/security/SecurityPage";

// Host checks (ADR 0109): every server's security findings in one list,
// a tab beside Servers. Operators and platform viewers only.
export const Route = createFileRoute("/_app/console/security")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(queries.session()),
      queryClient.ensureQueryData(queries.servers()),
      queryClient.ensureQueryData(hostChecksQuery()),
    ]),
  component: Tabbed,
});

/** The page under its sidebar entry's tab row (ConsoleTabs). */
function Tabbed() {
  return (
    <ConsoleTabs>
      <SecurityPage />
    </ConsoleTabs>
  );
}
