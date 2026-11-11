import { createFileRoute } from "@tanstack/react-router";
import { useIsAdmin } from "../data/queries";
import { AlertLists } from "../features/alerts/AlertsPage";
import { alertQueries } from "../features/alerts/data";
import { AccessRequests } from "../features/requests/AccessRequests";
import { Page, PageBar } from "../ui/kit";

// The organization's Inbox (ADR 0076): what waits on someone, in one
// list. Access requests to decide (ADR 0044, admins; ?request= opens one),
// then open alerts and the last resolved (ADR 0062). Where alerts go is
// under Settings, Alerts.
export const Route = createFileRoute("/_app/$org/inbox")({
  validateSearch: (s: Record<string, unknown>): { request?: number } => {
    const id = Number(s.request);
    return Number.isInteger(id) && id > 0 ? { request: id } : {};
  },
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(alertQueries.open("org")),
      queryClient.ensureQueryData(alertQueries.all("org")),
    ]),
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
          skip={admin ? (a) => a.kind === "access_request" : undefined}
        />
      </Page>
    </>
  );
}
