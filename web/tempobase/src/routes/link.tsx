import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { day } from "../../../src/ui/format";
import { tb } from "../api/backend";
import { useUseLink } from "../data/queries";
import { Entry, Field, minPassword, Submit } from "../shell/Entry";

// An invite or a password reset: /link#<code>, made by an admin. Opening
// it sets a name (invites) and a password, then signs in.
export const Route = createFileRoute("/link")({
  component: UseLink,
});

function UseLink() {
  const [code] = useState(() => location.hash.slice(1));
  const link = useQuery({
    queryKey: ["link", code],
    queryFn: () => tb().link(code),
    enabled: !!code,
    retry: false,
  });
  const use = useUseLink(code);
  const navigate = Route.useNavigate();
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");

  if (!code || link.error) {
    return (
      <Entry title="This link doesn't work">
        <p className="text-(--g-dim)">
          {link.error?.message ??
            "The address has no code. Copy the whole link you were given."}{" "}
          Links work once; ask an admin for a new one.
        </p>
        <Link to="/login">Sign in</Link>
      </Entry>
    );
  }
  if (!link.data) return <Entry title="Opening the link">{null}</Entry>;

  const invite = link.data.kind === "invite";
  return (
    <Entry
      title={invite ? "Join tempoBase" : "Set a new password"}
      context={`${link.data.email} · valid until ${day(link.data.expires_at)}`}
    >
      <form
        className="flex flex-col gap-3.5"
        onSubmit={(e) => {
          e.preventDefault();
          use.mutate(invite ? { name: name.trim(), password } : { password }, {
            onSuccess: () => {
              history.replaceState(null, "", location.pathname);
              navigate({ to: "/" });
            },
          });
        }}
      >
        {invite && (
          <Field
            label="Name"
            required
            autoComplete="name"
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        )}
        <Field
          label="Password"
          type="password"
          required
          minLength={minPassword}
          autoComplete="new-password"
          hint={`At least ${minPassword} characters`}
          autoFocus={!invite}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <Submit disabled={use.isPending} error={use.error}>
          {invite ? "Join" : "Set password"}
        </Submit>
      </form>
    </Entry>
  );
}
