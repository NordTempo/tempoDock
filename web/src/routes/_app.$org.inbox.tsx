import { createFileRoute } from "@tanstack/react-router";
import { queries, useIsAdmin } from "../data/queries";
import { AlertLists } from "../features/alerts/AlertsPage";
import { alertQueries } from "../features/alerts/data";
import { AccessRequests } from "../features/requests/AccessRequests";
import { requestQueries } from "../features/requests/data";
import { Page, PageBar } from "../ui/kit";

// The organization's Inbox (ADR 0076): what waits on someone, the one
// list of it (the Overview says it in a line). Two groups by what you do
// with them: access requests waiting for your answer (ADR 0044, admins;
// ?request= opens one), then alerts, each with the button to its fix
// (ADR 0062); resolved ones fold under them. Where alerts go is under
// Settings, Alerts.
export const Route = createFileRoute("/_app/$org/inbox")({
  validateSearch: (s: Record<string, unknown>): { request?: number } => {
    const id = Number(s.request);
    return { request: Number.isInteger(id) && id > 0 ? id : undefined };
  },
  loader: async ({ context: { queryClient } }) => {
    // Admins' access requests load with the alerts, not after them.
    const session = await queryClient.ensureQueryData(queries.session());
    await Promise.all([
      queryClient.ensureQueryData(alertQueries.open("org")),
      queryClient.ensureQueryData(alertQueries.all("org")),
      session?.scope === "admin" &&
        queryClient.ensureQueryData(requestQueries.list()),
    ]);
  },
  component: Inbox,
});

function Inbox() {
  const admin = useIsAdmin();
  const { request } = Route.useSearch();
  return (
    <>
      <PageBar title="Inbox" />
      <Page>
        {admin && <AccessRequests selected={request} />}
        {/* Admins decide access requests above; their alerts would repeat them. */}
        <AlertLists
          scope="org"
          title="Alerts"
          skip={admin ? (a) => a.kind === "access_request" : undefined}
        />
      </Page>
    </>
  );
}
