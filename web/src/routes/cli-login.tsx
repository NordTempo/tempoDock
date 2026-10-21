import { useMutation, useQuery } from "@tanstack/react-query";
import { createFileRoute, redirect } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { api, body, done } from "../api/client";
import { queries, useSession } from "../data/queries";
import { ago, clock } from "../ui/format";
import { ErrorText } from "../ui/kit";

// Approval page for `dock login`: the CLI prints a link to here with its
// user code. Signed-out visitors log in first and come back.
export const Route = createFileRoute("/cli-login")({
  validateSearch: (search: Record<string, unknown>): { code?: string } =>
    typeof search.code === "string" ? { code: search.code } : {},
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

function CliLogin() {
  const { code = "" } = Route.useSearch();
  const session = useSession();
  const request = useQuery({
    queryKey: ["cli-login", code],
    queryFn: () =>
      body(
        api.GET("/api/v1/auth/cli/requests/{code}", {
          params: { path: { code } },
        }),
      ),
    enabled: code !== "",
    // Wrong codes count towards a lockout; never retry one.
    retry: false,
  });
  const decide = useMutation({
    mutationFn: async (approve: boolean) => {
      const decision = { body: { user_code: code } };
      await done(
        approve
          ? api.POST("/api/v1/auth/cli/approve", decision)
          : api.POST("/api/v1/auth/cli/deny", decision),
      );
      return approve;
    },
  });
  const l = request.data;

  let content: ReactNode;
  if (!code) {
    content = (
      <p className="text-mut">
        This link has no login code. Run{" "}
        <code className="font-mono text-white">dock login</code> again and open
        the link it prints.
      </p>
    );
  } else if (decide.isSuccess) {
    content = decide.data ? (
      <p>
        <span className="text-ok">✓</span> The CLI on{" "}
        <span className="font-mono">{l?.hostname}</span> is signed in. You can
        close this tab.
      </p>
    ) : (
      <p>
        <span className="text-fail">✗</span> Denied. The CLI was not signed in.
        You can close this tab.
      </p>
    );
  } else if (request.isError) {
    content = (
      <div className="flex flex-col gap-2">
        <ErrorText error={request.error} />
        <p className="text-mut">
          Run <code className="font-mono text-white">dock login</code> again and
          open the new link.
        </p>
      </div>
    );
  } else if (!l) {
    content = <p className="text-mut">Loading…</p>;
  } else if (l.status !== "pending") {
    content = (
      <p className="text-mut">
        This login was already {l.status}. You can close this tab.
      </p>
    );
  } else {
    content = (
      <form
        className="flex flex-col gap-[22px]"
        onSubmit={(e) => {
          e.preventDefault();
          decide.mutate(true);
        }}
      >
        <div className="flex flex-col gap-1.5">
          <span className="font-mono text-[28px] font-semibold tracking-[0.12em]">
            {l.user_code}
          </span>
          <span>Check that this matches the code in your terminal.</span>
        </div>
        <dl className="grid grid-cols-[max-content_1fr] gap-x-5 gap-y-1.5">
          <Field label="client">
            {l.client_name} on <span className="font-mono">{l.hostname}</span>{" "}
            <span className="text-mut">{l.os}</span>
          </Field>
          <Field label="from">
            <span className="font-mono">{l.ip}</span>
          </Field>
          <Field label="requested">
            {clock(l.created_at)}{" "}
            <span className="text-mut">
              {ago(l.created_at)}, link expires {clock(l.expires_at)}
            </span>
          </Field>
          <Field label="access">
            <b className="font-semibold">{l.scope}</b>{" "}
            <span className="text-mut">same as you, {session?.name}</span>
          </Field>
          <Field label="token">
            <span className="font-mono">{l.token_name}</span>{" "}
            <span className="text-mut">expires in {l.token_days} days</span>
          </Field>
        </dl>
        <div className="flex flex-wrap items-center gap-2.5">
          <button
            type="submit"
            // biome-ignore lint/a11y/noAutofocus: approving is the one action here
            autoFocus
            disabled={decide.isPending}
            className="btn pri px-3.5 py-2 text-md"
          >
            Approve <span className="font-mono text-2xs opacity-70">↵</span>
          </button>
          <button
            type="button"
            disabled={decide.isPending}
            onClick={() => decide.mutate(false)}
            className="btn px-3.5 py-2 text-md"
          >
            Deny
          </button>
          <ErrorText error={decide.error} />
        </div>
        <p className="text-xs text-mut">
          Only approve a login you just started. Anyone with this CLI gets your
          access until the token expires or is revoked.
        </p>
      </form>
    );
  }

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
        {content}
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
