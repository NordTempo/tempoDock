import { createFileRoute, redirect } from "@tanstack/react-router";
import { queries } from "../data/queries";
import { Page } from "../ui/kit";

// "/" goes to the organization the caller acts in (the last one used).
export const Route = createFileRoute("/_app/")({
  beforeLoad: async ({ context }) => {
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (session?.org) {
      throw redirect({ to: "/$org", params: { org: session.org } });
    }
  },
  component: NoOrg,
});

function NoOrg() {
  return (
    <Page width="max-w-[720px]">
      <h1 className="pt-10 text-xl font-semibold">No organization yet</h1>
      <p className="text-mut">
        Ask an organization's admin to add you, or create one with{" "}
        <span className="font-mono text-fg">dock org create</span>.
      </p>
    </Page>
  );
}
