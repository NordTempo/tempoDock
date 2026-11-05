import { createFileRoute, Link, redirect } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { RoutePending } from "../shell/Shell";
import { Page, PageBar } from "../ui/kit";

// The platform console: everything that runs Dock itself, for operators
// only (ADR 0027). Everyone else is sent back to their projects (the API
// refuses them too).
// The shell gives these pages the console sidebar and the amber bar.
export const Route = createFileRoute("/_app/console")({
  beforeLoad: async ({ context }) => {
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (!session?.operator) throw redirect({ to: "/" });
  },
  // Router looks for the nearest route with a notFoundComponent; without
  // one here, a console 404 would replace the whole shell with the root's.
  notFoundComponent: ConsoleNotFound,
  // A slow console page loads inside the shell.
  pendingComponent: RoutePending,
});

/** A console address with no page, inside the console's shell. */
function ConsoleNotFound() {
  return (
    <>
      <PageBar title="Not found" />
      <Page gap="gap-3">
        <p className="text-mut">Nothing in the console at this address.</p>
        <Link to="/console" className="btn pri self-start">
          Back to the overview
        </Link>
      </Page>
    </>
  );
}
