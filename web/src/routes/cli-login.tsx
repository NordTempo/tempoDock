import { useMutation, useQuery } from "@tanstack/react-query";
import { createFileRoute, redirect } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { api, body, done } from "../api/client";
import { queries, useSession } from "../data/queries";
import { ago, clock } from "../ui/format";
import { ErrorText } from "../ui/kit";

// Approval page for `dock login`: the CLI prints a link to here and a code
// the user types in. The link never carries the code, so nobody can send a
// signed-in user a link that approves someone else's login in one click.
// Signed-out visitors log in first and come back.
export const Route = createFileRoute("/cli-login")({
  beforeLoad: async ({ context, location }) => {
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (!session) {
      throw redirect({ to: "/login", search: { redirect: location.href } });
    }
  },
  component: CliLogin,
});

// userCode turns what was typed into "BCDF-GHJK", or null until it is a
// whole code. Letters as in core's userCodeAlphabet; checking them here
// keeps typos from spending the server's wrong-code allowance.
function userCode(typed: string): string | null {
  const s = typed.replace(/[\s-]/g, "").toUpperCase();
  return /^[BCDFGHJKLMNPQRSTVWXZ]{8}$/.test(s)
    ? `${s.slice(0, 4)}-${s.slice(4)}`
    : null;
}

function CliLogin() {
  const session = useSession();
  const [typed, setTyped] = useState("");
  const code = userCode(typed);
  const request = useQuery({
    queryKey: ["cli-login", code],
    queryFn: () =>
      body(
        api.GET("/api/v1/auth/cli/requests/{code}", {
          params: { path: { code: code ?? "" } },
        }),
      ),
    enabled: code !== null,
    // Wrong codes count towards a lockout; never retry one.
    retry: false,
  });
  const decide = useMutation({
    mutationFn: async ({
      code,
      approve,
    }: {
      code: string;
      approve: boolean;
    }) => {
      const decision = { body: { user_code: code } };
      await done(
        approve
          ? api.POST("/api/v1/auth/cli/approve", decision)
          : api.POST("/api/v1/auth/cli/deny", decision),
      );
      return approve;
    },
  });
  // Only a login found by the code typed right now can be decided.
  const l = code !== null ? request.data : undefined;

  if (decide.isSuccess) {
    return (
      <Shell>
        {decide.data ? (
          <p>
            <span className="text-ok">✓</span> The CLI on{" "}
            <span className="font-mono">{l?.hostname}</span> is signed in. You
            can close this tab.
          </p>
        ) : (
          <p>
            <span className="text-fail">✕</span> Denied. The CLI was not signed
            in. You can close this tab.
          </p>
        )}
      </Shell>
    );
  }

  // Nothing shows until a whole code is typed.
  let found: ReactNode = null;
  if (code !== null && request.isError) {
    found = <ErrorText error={request.error} />;
  } else if (code !== null && !l) {
    found = <p className="text-mut">Looking up…</p>;
  } else if (!l) {
    found = null;
  } else if (l.status !== "pending") {
    found = (
      <p className="text-mut">
        This login was already {l.status}. Run{" "}
        <code className="font-mono text-fg">dock login</code> again for a new
        code.
      </p>
    );
  } else {
    found = (
      <dl className="grid grid-cols-[max-content_1fr] gap-x-5 gap-y-1.5">
        <Field label="Client">
          {l.client_name} on <span className="font-mono">{l.hostname}</span>{" "}
          <span className="text-mut">{l.os}</span>
        </Field>
        <Field label="From">
          <span className="font-mono">{l.ip}</span>
        </Field>
        <Field label="Requested">
          {clock(l.created_at)}{" "}
          <span className="text-mut">
            {ago(l.created_at)}, code expires {clock(l.expires_at)}
          </span>
        </Field>
        <Field label="Access">
          <b className="font-semibold">{l.scope}</b>{" "}
          <span className="text-mut">same as you, {session?.name}</span>
        </Field>
        <Field label="Token">
          <span className="font-mono">{l.token_name}</span>{" "}
          <span className="text-mut">expires in {l.token_days} days</span>
        </Field>
      </dl>
    );
  }
  const decidable = code !== null && l?.status === "pending";

  return (
    <Shell>
      <label className="flex flex-col gap-1.5">
        <span>Code shown in your terminal</span>
        <input
          // biome-ignore lint/a11y/noAutofocus: typing the code is the first step here
          autoFocus
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          disabled={decide.isPending}
          placeholder="XXXX-XXXX"
          maxLength={12}
          autoComplete="off"
          autoCapitalize="characters"
          spellCheck={false}
          className="field px-3 py-2.5 font-mono text-[22px] uppercase tracking-[0.12em]"
        />
      </label>
      {/* Always rendered, so screen readers announce what the lookup finds;
          the negative margin cancels the page gap while it is empty. */}
      <div aria-live="polite" className="-mt-[22px]">
        {found && <div className="pt-[22px]">{found}</div>}
      </div>
      <div className="flex flex-wrap items-center gap-2.5">
        <button
          type="button"
          disabled={!decidable || decide.isPending}
          onClick={() => code && decide.mutate({ code, approve: true })}
          className="btn pri px-3.5 py-2 text-md"
        >
          Approve
        </button>
        <button
          type="button"
          disabled={!decidable || decide.isPending}
          onClick={() => code && decide.mutate({ code, approve: false })}
          className="btn px-3.5 py-2 text-md"
        >
          Deny
        </button>
        <ErrorText error={decide.error} />
      </div>
      <p className="text-xs text-mut">
        Only approve a login you just started yourself, with a code from your
        own terminal. Anyone with this CLI gets your access until the token
        expires or is revoked.
      </p>
    </Shell>
  );
}

function Shell({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-screen flex-col items-center justify-center px-4 py-10">
      <main className="flex w-full max-w-[440px] flex-col gap-[22px]">
        <div className="flex flex-col gap-1">
          <span className="font-mono text-lg font-bold">dock</span>
          <h1 className="text-xl font-semibold">Sign in the dock CLI?</h1>
          <span className="font-mono text-xs text-mut">
            {window.location.host}
          </span>
        </div>
        {children}
      </main>
    </div>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-mut">{label}</dt>
      <dd>{children}</dd>
    </>
  );
}
