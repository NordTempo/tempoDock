import { createFileRoute, redirect } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { orgQueries } from "../features/org/data";
import { InviteError, InvitePage } from "../features/org/InvitePage";

// An invite link, outside the app shell: the person invited may belong to
// no organization yet. Signed-out visitors log in first and come back
// (the API shows an invite only to a signed-in person); the sign-in page
// says why they are there.
export const Route = createFileRoute("/invite/$code")({
  beforeLoad: async ({ context, location }) => {
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (!session) {
      throw redirect({ to: "/login", search: { redirect: location.href } });
    }
  },
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(orgQueries.invite(params.code)),
  component: Invite,
  // A wrong or dead link says so here, not as the app's "nothing here".
  errorComponent: ({ error }) => <InviteError error={error} />,
});

function Invite() {
  return <InvitePage code={Route.useParams().code} />;
}
