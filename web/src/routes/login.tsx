import { useQuery } from "@tanstack/react-query";
import { createFileRoute, redirect, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { api, body } from "../api/client";
import { useLogin } from "../data/mutations";
import { queries } from "../data/queries";
import { ErrorText } from "../ui/kit";

export const Route = createFileRoute("/login")({
  validateSearch: (search: Record<string, unknown>): { redirect?: string } =>
    typeof search.redirect === "string" ? { redirect: search.redirect } : {},
  beforeLoad: async ({ context }) => {
    const session = await context.queryClient.ensureQueryData(
      queries.session(),
    );
    if (session) throw redirect({ to: "/" });
  },
  component: Login,
});

const field = "field px-3 py-2.5 text-md";

function Login() {
  const { redirect: back } = Route.useSearch();
  const navigate = useNavigate();
  const login = useLogin();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const health = useQuery({
    queryKey: ["health"],
    queryFn: () => body(api.GET("/api/v1/health")),
    retry: false,
  });
  const host = window.location.host;

  return (
    <div className="flex min-h-screen flex-col items-center justify-center px-4 py-10">
      <main className="flex w-full max-w-[360px] flex-col gap-[22px]">
        <div className="flex flex-col gap-1">
          <span className="font-mono text-lg font-bold">dock</span>
          <h1 className="text-xl font-semibold">Sign in</h1>
          <span className="font-mono text-xs text-mut">{host}</span>
        </div>
        <form
          className="flex flex-col gap-3.5"
          onSubmit={(e) => {
            e.preventDefault();
            login.mutate(
              { email, password },
              {
                // Same-origin paths only, never an absolute URL from the query.
                onSuccess: () =>
                  navigate({ href: back?.startsWith("/") ? back : "/" }),
              },
            );
          }}
        >
          <label className="flex flex-col gap-1.5">
            <span>Email</span>
            <input
              // biome-ignore lint/a11y/noAutofocus: the only thing to do on this page
              autoFocus
              type="email"
              autoComplete="username"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              className={field}
            />
          </label>
          <label className="flex flex-col gap-1.5">
            <span>Password</span>
            <input
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              className={field}
            />
          </label>
          <button
            type="submit"
            disabled={login.isPending}
            className="btn pri justify-center px-3.5 py-2.5 text-md"
          >
            Sign in <span className="font-mono text-2xs opacity-70">↵</span>
          </button>
          <ErrorText error={login.error} />
        </form>
        <div className="flex flex-col gap-1 border-t border-line pt-4">
          <span className="text-mut">From a terminal</span>
          <code className="font-mono">dock login {window.location.origin}</code>
        </div>
        <p className="text-xs text-mut">
          {health.isSuccess ? (
            <>
              <span className="text-ok">●</span> control plane online ·{" "}
              {health.data.version}
            </>
          ) : health.isError ? (
            <>
              <span className="text-fail">●</span> control plane not reachable
            </>
          ) : (
            " "
          )}
        </p>
      </main>
    </div>
  );
}
