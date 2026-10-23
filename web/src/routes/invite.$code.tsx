import { createFileRoute, redirect } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { orgQueries } from "../features/org/data";
import { InvitePage } from "../features/org/InvitePage";

// An invite link, outside the app shell: the person invited may belong to
// no organization yet. Signed-out visitors log in first and come back.
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
});

function Invite() {
  return <InvitePage code={Route.useParams().code} />;
}
