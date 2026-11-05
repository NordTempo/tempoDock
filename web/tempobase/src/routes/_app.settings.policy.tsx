import { createFileRoute } from "@tanstack/react-router";
import { ErrorText, Head } from "../../../src/features/data/kit";
import {
  PolicyEditor,
  PolicyLegend,
} from "../../../src/features/data/PolicyEditor";
import { kinds } from "../../../src/features/data/policy";
import { builtIn, fromText, subjectNames } from "../data/policy";
import { useIsAdmin, usePolicy, useSavePolicy } from "../data/queries";

// Access (G8): the default data access policy for every connection.
// Everyone may read it; admins change it. A connection's own rules are on
// its settings page.
export const Route = createFileRoute("/_app/settings/policy")({
  component: Access,
});

function Access() {
  const admin = useIsAdmin();
  const policy = usePolicy();
  const save = useSavePolicy();
  return (
    <>
      <Head title="Access" />
      <main className="flex max-w-[960px] flex-col gap-6 px-5 py-6">
        {policy.error ? (
          <ErrorText error={policy.error} />
        ) : !policy.data ? (
          <p className="text-(--g-mut)">Loading the policy</p>
        ) : (
          <PolicyEditor
            policy={policy.data}
            shownKinds={kinds}
            kindColumn="Connections"
            editable={admin}
            save={save}
            subjectNames={subjectNames}
            fromText={(from) => fromText(from)}
            builtIn={builtIn}
          />
        )}
        <section className="flex max-w-[760px] flex-col gap-2 text-(--g-dim)">
          <p>
            These rules apply to every connection unless its settings override
            them; admins always may do everything, and a connection is hidden
            from anyone whose level is none. An agent gets the lower of its rule
            and its owner's access, and on production its changes are proposals
            an admin approves.
          </p>
          <PolicyLegend />
        </section>
      </main>
    </>
  );
}
