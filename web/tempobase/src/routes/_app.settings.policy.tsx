import { createFileRoute, Link } from "@tanstack/react-router";
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
      <Head title="Access">
        <span className="text-xs text-(--g-mut)">
          What viewers, editors and agents may do with the data. Admins always
          may.
        </span>
      </Head>
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
        <section className="flex max-w-[760px] flex-col gap-2 text-(--g-mut)">
          <p>
            Rules here apply to every connection; a connection's settings can
            override them for it. A connection where someone's level is none is
            hidden from them.
          </p>
          <PolicyLegend />
          <p>
            An agent gets the lowest of: this policy's agents rule or its own
            limit, and what its owner may do now. On production its changes are
            always proposals an admin approves.
            {admin && (
              <>
                {" "}
                People and their roles are on{" "}
                <Link to="/settings/people">People</Link>.
              </>
            )}
          </p>
        </section>
      </main>
    </>
  );
}
