import { createFileRoute, redirect } from "@tanstack/react-router";
import { useState } from "react";
import { Btn, ErrorText, Head, Kbd } from "../../../src/features/data/kit";
import {
  queries,
  useCreateConnection,
  useTestConnection,
} from "../data/queries";
import {
  ConnectionFields,
  type ConnectionForm,
  suggestName,
} from "../shell/ConnectionFields";

// Add a connection (admins): paste the URL, test it, then save.
export const Route = createFileRoute("/_app/connections/new")({
  beforeLoad: async ({ context }) => {
    const me = await context.queryClient.ensureQueryData(queries.me());
    if (me?.role !== "admin") throw redirect({ to: "/" });
  },
  component: NewConnection,
});

function NewConnection() {
  const navigate = Route.useNavigate();
  const test = useTestConnection();
  const create = useCreateConnection();
  const [form, setForm] = useState<ConnectionForm>({
    url: "",
    name: "",
    production: false,
    ca_pem: "",
  });
  // The test result belongs to the URL and CA it was run with.
  const [tested, setTested] = useState("");
  const probe = `${form.url}\n${form.ca_pem}`;
  const passed = test.data?.ok && tested === probe;
  const name = form.name || suggestName(form.url);

  const runTest = () => {
    setTested(probe);
    test.mutate({ url: form.url.trim(), ca_pem: form.ca_pem || undefined });
  };

  return (
    <>
      <Head crumb="Connections" title="Add connection" />
      <main className="flex max-w-[640px] flex-col gap-5 px-5 py-6">
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (!passed) return runTest();
            create.mutate(
              {
                name,
                url: form.url.trim(),
                production: form.production,
                ca_pem: form.ca_pem || undefined,
              },
              {
                onSuccess: (c) =>
                  navigate({
                    to: "/c/$connection",
                    params: { connection: c.name },
                  }),
              },
            );
          }}
        >
          <ConnectionFields
            form={form}
            onChange={(next) => setForm(next)}
            namePlaceholder={suggestName(form.url) || "from the database name"}
          />
          <TestResult tested={tested === probe} test={test} />
          <div className="flex flex-wrap items-center gap-2">
            {/* Enter tests until the test passes, then saves. */}
            <Btn
              type={passed ? "button" : "submit"}
              disabled={!form.url.trim() || test.isPending}
              title={form.url.trim() ? undefined : "Enter a URL first"}
              onClick={passed ? runTest : undefined}
            >
              {test.isPending ? "Testing" : "Test"}
              {!passed && <Kbd>↵</Kbd>}
            </Btn>
            <Btn
              primary
              type={passed ? "submit" : "button"}
              disabled={!passed || !name || create.isPending}
              title={passed ? undefined : "Test the connection first"}
            >
              Save {passed && <Kbd>↵</Kbd>}
            </Btn>
            <ErrorText error={create.error} />
          </div>
        </form>
      </main>
    </>
  );
}

/** What the last test found, for the URL in the form. */
function TestResult({
  tested,
  test,
}: {
  tested: boolean;
  test: ReturnType<typeof useTestConnection>;
}) {
  if (!tested) return null;
  if (test.error) return <ErrorText error={test.error} />;
  const r = test.data;
  if (!r) return null;
  if (!r.ok) {
    return <p className="text-(--g-fail)">{r.error ?? "The test failed."}</p>;
  }
  return (
    <p className="text-(--g-dim)">
      <span className="text-(--g-ok)">Connected</span> · Postgres{" "}
      {r.server_version}
      {r.can_set_up ? (
        " · this user can set up data access"
      ) : (
        <span className="text-(--g-warn)">
          {" "}
          · this user can't create roles, so setup will fail. Use the owner or a
          user with CREATEROLE.
        </span>
      )}
    </p>
  );
}
