import { createFileRoute, redirect, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { useLogin } from "../data/mutations";
import { queries } from "../data/queries";
import { Button, ErrorText, inputClass } from "../ui/kit";

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

function Login() {
  const { redirect: back } = Route.useSearch();
  const navigate = useNavigate();
  const login = useLogin();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  return (
    <main className="px-5 pt-[18vh]">
      <form
        className="mx-auto flex w-[260px] flex-col gap-3"
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
        <h1 className="pb-2 font-semibold">Tempo Dock</h1>
        <label className="flex flex-col gap-0.5 text-dim">
          <span className="text-2xs">Email</span>
          <input
            // biome-ignore lint/a11y/noAutofocus: the only thing to do on this page
            autoFocus
            type="email"
            autoComplete="username"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            className={`${inputClass} text-white`}
          />
        </label>
        <label className="flex flex-col gap-0.5 text-dim">
          <span className="text-2xs">Password</span>
          <input
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className={`${inputClass} text-white`}
          />
        </label>
        <div className="flex items-center gap-3 pt-2">
          <Button primary type="submit" disabled={login.isPending}>
            Log in
          </Button>
          <ErrorText error={login.error} />
        </div>
      </form>
    </main>
  );
}
