import { createFileRoute } from "@tanstack/react-router";
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
  component: EmailPage,
});
