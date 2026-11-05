import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, redirect } from "@tanstack/react-router";
import { useState } from "react";
import { queries, useLogin } from "../data/queries";
import { Entry, Field, Submit } from "../shell/Entry";

// Sign in. Signed-in people go on to where they were headed.
export const Route = createFileRoute("/login")({
  validateSearch: (s: Record<string, unknown>): { redirect?: string } => ({
    redirect:
      typeof s.redirect === "string" && s.redirect.startsWith("/")
        ? s.redirect
        : undefined,
  }),
  beforeLoad: async ({ context, search }) => {
    const me = await context.queryClient.ensureQueryData(queries.me());
    if (me) throw redirect({ href: search.redirect ?? "/" });
  },
  component: Login,
});

function Login() {
  const { redirect: to } = Route.useSearch();
  const navigate = Route.useNavigate();
  const login = useLogin();
  const setup = useQuery(queries.setup());
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  if (setup.data?.needed) {
    return (
      <Entry title="No users yet">
        <p className="text-(--g-dim)">
          The server's log has a link with a one-time code for creating the
          first admin. Open it, or run{" "}
          <code className="text-(--g-text)">tempobase admin create-user</code>.
        </p>
        <Link to="/setup" className="g-btn pri h-8 text-white!">
          I have the code
        </Link>
      </Entry>
    );
  }

  return (
    <Entry title="Sign in">
      <form
        className="flex flex-col gap-3.5"
        onSubmit={(e) => {
          e.preventDefault();
          login.mutate(
            { email: email.trim(), password },
            { onSuccess: () => navigate({ href: to ?? "/" }) },
          );
        }}
      >
        <Field
          label="Email"
          type="email"
          autoComplete="username"
          required
          autoFocus
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        <Field
          label="Password"
          type="password"
          autoComplete="current-password"
          required
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <Submit disabled={login.isPending} error={login.error}>
          Sign in
        </Submit>
      </form>
      <p className="text-xs text-(--g-mut)">
        Forgot your password? An admin can give you a reset link.
      </p>
    </Entry>
  );
}
