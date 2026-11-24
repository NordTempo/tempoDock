import { createFileRoute } from "@tanstack/react-router";
import { ConsoleTabs } from "../features/console/ConsoleTabs";
import { EmailPage } from "../features/console/EmailPage";
import { emailQuery, outboxQuery } from "../features/console/email";

// Dock's own email (ADR 0079): the SMTP server invites and other mail go
// through, and the latest mail. Operators only.
export const Route = createFileRoute("/_app/console/email")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(emailQuery()),
      queryClient.ensureQueryData(outboxQuery()),
    ]),
  component: Tabbed,
});

/** The page under its sidebar entry's tab row (ConsoleTabs). */
function Tabbed() {
  return (
    <ConsoleTabs>
      <EmailPage />
    </ConsoleTabs>
  );
}
