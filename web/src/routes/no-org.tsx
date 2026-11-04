import { createFileRoute, redirect } from "@tanstack/react-router";
import { useLogout } from "../data/mutations";
import { queries } from "../data/queries";
import { Button, Page } from "../ui/kit";

// Where a signed-in person who belongs to no organization lands: the app
// shows one org's projects, so there is nothing to show them yet.
export const Route = createFileRoute("/no-org")({
  beforeLoad: async ({ context }) => {
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (!session) throw redirect({ to: "/login" });
    if (session.org) throw redirect({ to: "/" });
    // Not orgless: their org needs a second factor (ADR 0050).
    if (session.two_factor_required) throw redirect({ to: "/account" });
  },
  component: NoOrg,
});

function NoOrg() {
  const logout = useLogout();
  return (
    <Page width="max-w-[560px]" gap="gap-4">
      <h1 className="pt-10 text-xl font-semibold">No organization yet</h1>
      <p className="text-mut">
        Open the invite link an organization's admin sent you, or create one
        with <span className="font-mono text-fg">dock org create</span>.
      </p>
      <p>
        <Button small onClick={() => logout.mutate()}>
          Log out
        </Button>
      </p>
    </Page>
  );
}
