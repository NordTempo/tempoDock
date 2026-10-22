import { createFileRoute, redirect } from "@tanstack/react-router";
import { useState } from "react";
import { queries, useSetup } from "../data/queries";
import { Entry, Field, minPassword, Submit } from "../shell/Entry";

// The first admin: /setup#code=<code> from the server's log. The code
// stays in the fragment, out of proxy logs.
export const Route = createFileRoute("/setup")({
  beforeLoad: async ({ context }) => {
    const { needed } = await context.queryClient.fetchQuery(queries.setup());
    if (!needed) throw redirect({ to: "/login" });
  },
  component: Setup,
});

/** The code from "#code=...", or "". */
const fragmentCode = () =>
  new URLSearchParams(location.hash.slice(1)).get("code") ?? "";

function Setup() {
  const navigate = Route.useNavigate();
  const setup = useSetup();
  const [code, setCode] = useState(fragmentCode);
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");

  return (
    <Entry
      title="Create the first admin"
      context="Admins add connections, invite people and set who sees what."
    >
      <form
        className="flex flex-col gap-3.5"
        onSubmit={(e) => {
          e.preventDefault();
          setup.mutate(
            {
              code: code.trim(),
              name: name.trim(),
              email: email.trim(),
              password,
            },
            { onSuccess: () => navigate({ to: "/" }) },
          );
        }}
      >
        {!fragmentCode() && (
          <Field
            label="Setup code"
            hint="From the server's log: no users yet: open .../setup#code=..."
            required
            autoComplete="off"
            value={code}
            onChange={(e) => setCode(e.target.value)}
          />
        )}
        <Field
          label="Name"
          required
          autoComplete="name"
          autoFocus
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <Field
          label="Email"
          type="email"
          required
          autoComplete="username"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        <Field
          label="Password"
          type="password"
          required
          minLength={minPassword}
          autoComplete="new-password"
          hint={`At least ${minPassword} characters`}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <Submit disabled={setup.isPending} error={setup.error}>
          Create admin
        </Submit>
      </form>
    </Entry>
  );
}
