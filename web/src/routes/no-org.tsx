import { createFileRoute, redirect } from "@tanstack/react-router";
import { useLogout } from "../data/mutations";
import { queries, useSession } from "../data/queries";
import { Button } from "../ui/kit";

// Where a signed-in person who belongs to no organization lands: the app
// shows one org's projects, so there is nothing to show them yet.
export const Route = createFileRoute("/no-org")({
  beforeLoad: async ({ context }) => {
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (!session) throw redirect({ to: "/login" });
    if (session.org) throw redirect({ to: "/" });
    // Not orgless: their org needs a second factor (ADR 0057) or single
    // sign-on (ADR 0058).
    if (session.two_factor_required || session.sso_required) {
      throw redirect({ to: "/account" });
    }
  },
  component: NoOrg,
});

function NoOrg() {
  const logout = useLogout();
  const session = useSession();
  return (
    <div className="flex min-h-screen flex-col items-center px-4 pt-[16vh] pb-10">
      <main className="flex w-full max-w-[420px] flex-col gap-[22px]">
        <div className="flex flex-col gap-1">
          <span className="font-mono text-lg font-bold">dock</span>
          <h1 className="text-xl font-semibold">
            You're not in an organization yet
          </h1>
          <span className="font-mono text-xs text-mut">
            {session?.email ?? session?.name}
          </span>
        </div>
        <p>
          Ask an admin to invite you, then open the link they send. To start
          your own, run{" "}
          <span className="font-mono">dock org create &lt;slug&gt;</span>.
        </p>
        <div>
          <Button disabled={logout.isPending} onClick={() => logout.mutate()}>
            Sign out
          </Button>
        </div>
      </main>
    </div>
  );
}
