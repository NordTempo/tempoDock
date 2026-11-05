import { createFileRoute } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { orgQueries } from "../features/org/data";
import { InviteError, InvitePage } from "../features/org/InvitePage";

// An invite link, outside the app shell: the person invited may belong to
// no organization, or have no account, yet. The invite reads the same
// signed in or out (ADR 0060); the page offers what fits the visitor.
export const Route = createFileRoute("/invite/$code")({
  loader: ({ context: { queryClient }, params }) =>
    Promise.all([
      queryClient.ensureQueryData(queries.session()),
      queryClient.ensureQueryData(orgQueries.invite(params.code)),
    ]),
  component: Invite,
  // A wrong or dead link says so here, not as the app's "nothing here".
  errorComponent: ({ error }) => <InviteError error={error} />,
});

function Invite() {
  return <InvitePage code={Route.useParams().code} />;
}
