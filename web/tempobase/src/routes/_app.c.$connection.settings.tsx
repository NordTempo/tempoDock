import { useQuery } from "@tanstack/react-query";
import { createFileRoute, redirect } from "@tanstack/react-router";
import { type ReactNode, useMemo, useState } from "react";
import {
  DataSourceContext,
  useDataInfo,
  useDataSetup,
} from "../../../src/features/data/api";
import { Btn, ErrorText, Head } from "../../../src/features/data/kit";
import { PolicyEditor } from "../../../src/features/data/PolicyEditor";
import { StatusView } from "../../../src/features/data/StatusView";
import type { Connection } from "../api/types";
import { builtIn, fromText, subjectNames } from "../data/policy";
import {
  queries,
  useDeleteConnection,
  usePolicy,
  useSavePolicy,
  useUpdateConnection,
} from "../data/queries";
import { connectionSource } from "../data/source";
import {
  ConnectionFields,
  type ConnectionForm,
} from "../shell/ConnectionFields";

// A connection's settings (admins): URL, name and production, data access
// setup, its own policy rules, and removing it.
export const Route = createFileRoute("/_app/c/$connection/settings")({
  beforeLoad: async ({ context }) => {
    const me = await context.queryClient.ensureQueryData(queries.me());
    if (me?.role !== "admin") throw redirect({ to: "/" });
  },
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(queries.connection(params.connection)),
  component: Settings,
});

function Settings() {
  const { connection } = Route.useParams();
  const c = useQuery(queries.connection(connection)).data;
  const source = useMemo(() => connectionSource(connection), [connection]);
  if (!c) return null;
  return (
    <DataSourceContext value={source}>
      <Head crumb={connection} title="Settings" />
      <main className="flex max-w-[960px] flex-col gap-7 px-5 py-6">
        <Section title="Connection">
          <Edit key={c.name} c={c} />
        </Section>
        <Section title="Data access">
          <Access />
        </Section>
        <Section title="Rules for this connection">
          <ConnectionPolicy c={c} />
        </Section>
        <Section title="Remove">
          <Remove name={c.name} />
        </Section>
      </main>
    </DataSourceContext>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <h2 className="text-[13px] font-semibold">{title}</h2>
      {children}
    </section>
  );
}

/** URL (blank keeps it), name and production. */
function Edit({ c }: { c: Connection }) {
  const navigate = Route.useNavigate();
  const update = useUpdateConnection(c.name);
  const initial: ConnectionForm = {
    url: "",
    name: c.name,
    production: c.production,
    ca_pem: "",
  };
  const [form, setForm] = useState(initial);
  const dirty = JSON.stringify(form) !== JSON.stringify(initial);
  return (
    <form
      className="flex flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        update.mutate(
          {
            ...(form.url.trim() && { url: form.url.trim() }),
            ...(form.ca_pem && { ca_pem: form.ca_pem }),
            ...(form.name !== c.name && { name: form.name }),
            ...(form.production !== c.production && {
              production: form.production,
            }),
          },
          {
            onSuccess: (next) => {
              setForm({
                ...initial,
                name: next.name,
                production: next.production,
              });
              if (next.name !== c.name) {
                navigate({
                  to: "/c/$connection/settings",
                  params: { connection: next.name },
                });
              }
            },
          },
        );
      }}
    >
      <ConnectionFields
        key={c.url}
        form={form}
        onChange={setForm}
        currentUrl={`${c.url}${c.custom_ca ? " · custom CA" : ""}`}
        urlHint="Blank keeps the current URL. A new one applies at once; if it points at another database, set up data access again."
      />
      {/* Save and Discard show once something changed. */}
      {(dirty || update.error) && (
        <div className="flex items-center gap-2">
          <Btn primary type="submit" disabled={!dirty || update.isPending}>
            Save
          </Btn>
          <Btn disabled={!dirty} onClick={() => setForm(initial)}>
            Discard
          </Btn>
          <ErrorText error={update.error} />
        </div>
      )}
    </form>
  );
}

/** Setup state; when ready, setting up again rotates the passwords. */
function Access() {
  const info = useDataInfo();
  const setup = useDataSetup();
  if (info.error) return <ErrorText error={info.error} />;
  if (!info.data) return <p className="text-(--g-mut)">Connecting</p>;
  if (info.data.status !== "ready") {
    return (
      <div className="-mx-5">
        <StatusView info={info.data} />
      </div>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-3">
      <span>
        <span className="text-(--g-ok)">Ready</span>
        <span className="text-(--g-dim)">
          {" "}
          · {info.data.engine} {info.data.server_version}
        </span>
      </span>
      <Btn disabled={setup.isPending} onClick={() => setup.mutate()}>
        {setup.isPending ? "Rotating" : "Rotate role passwords"}
      </Btn>
      <ErrorText error={setup.error} />
    </div>
  );
}

function ConnectionPolicy({ c }: { c: Connection }) {
  const policy = usePolicy(c.name);
  const inherited = usePolicy();
  const save = useSavePolicy(c.name);
  if (policy.error) return <ErrorText error={policy.error} />;
  if (!policy.data || !inherited.data) {
    return <p className="text-(--g-mut)">Loading the policy</p>;
  }
  return (
    <PolicyEditor
      key={c.name}
      policy={policy.data}
      inherited={inherited.data}
      shownKinds={[c.production ? "production" : "non_production"]}
      kindColumn="Kind"
      editable
      save={save}
      subjectNames={subjectNames}
      fromText={(from) => fromText(from, c.name)}
      builtIn={builtIn}
    />
  );
}

/** Removing forgets tempoBase's state; the roles stay in the database. */
function Remove({ name }: { name: string }) {
  const remove = useDeleteConnection();
  const navigate = Route.useNavigate();
  const [confirm, setConfirm] = useState("");
  return (
    <div className="flex flex-col gap-3">
      <p className="max-w-[720px] text-(--g-mut)">
        Forgets the connection with its setup, sensitive marks, saved queries,
        changesets, rules and agent limits. Activity stays. The roles stay in
        the database; drop them there as its owner:
      </p>
      <pre className="w-fit rounded-lg bg-(--g-raise) px-3 py-2 font-mono text-xs text-(--g-code)">
        {`DROP ROLE tempobase_reader_masked;
DROP ROLE tempobase_writer;
DROP ROLE tempobase_reader;`}
      </pre>
      <form
        className="flex flex-wrap items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          remove.mutate(name, { onSuccess: () => navigate({ to: "/" }) });
        }}
      >
        <input
          className="g-field h-7 w-56 font-mono placeholder:font-sans"
          placeholder={`type ${name} to confirm`}
          aria-label={`Type ${name} to remove it`}
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
        />
        <Btn
          danger
          type="submit"
          disabled={confirm !== name || remove.isPending}
        >
          Remove {name}
        </Btn>
        <ErrorText error={remove.error} />
      </form>
    </div>
  );
}
