import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import {
  useCreateUser,
  useDeleteUser,
  useSetUserDisabled,
} from "../data/mutations";
import { queries, useSession, useTokens, useUsers } from "../data/queries";
import type { Scope, User } from "../data/types";
import { day } from "../ui/format";
import { Button, ErrorText, Page, Scroll, TitleRow } from "../ui/kit";

export const Route = createFileRoute("/_app/users")({
  loader: ({ context }) =>
    Promise.all([
      context.queryClient.ensureQueryData(queries.users()),
      context.queryClient.ensureQueryData(queries.tokens()),
    ]),
  component: Users,
});

function Users() {
  const users = useUsers();
  const tokens = useTokens();
  const me = useSession();
  const made = (u: User) =>
    tokens.filter((t) => t.created_by === u.email || t.created_by === u.name)
      .length;

  return (
    <Page>
      <TitleRow
        title="Users"
        context="People who sign in. Roles match token scopes: read, deploy, admin."
      />
      <AddUser />
      <Scroll>
        <table>
          <thead>
            <tr>
              <th>User</th>
              <th>Email</th>
              <th>Role</th>
              <th>Status</th>
              <th>Tokens created</th>
              <th>Added</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <UserRow
                key={u.id}
                user={u}
                you={me?.email === u.email}
                tokens={made(u)}
              />
            ))}
          </tbody>
        </table>
      </Scroll>
    </Page>
  );
}

function UserRow({
  user: u,
  you,
  tokens,
}: {
  user: User;
  you: boolean;
  tokens: number;
}) {
  const setDisabled = useSetUserDisabled();
  const remove = useDeleteUser();
  const [confirming, setConfirming] = useState(false);
  const disabled = !!u.disabled_at;
  return (
    <tr className={disabled ? "text-faint" : undefined}>
      <td className="py-2.5">
        ○ <b className="font-medium">{u.name}</b>{" "}
        {you && <span className="text-mut">you</span>}
      </td>
      <td className="py-2.5">{u.email}</td>
      <td className="py-2.5">{u.role}</td>
      <td className="py-2.5">
        {disabled ? "disabled" : <span className="text-ok">● active</span>}
      </td>
      <td className="py-2.5 font-mono">{tokens}</td>
      <td className="py-2.5 text-mut">{day(u.created_at)}</td>
      <td className="w-full py-1.5 text-right">
        {!you && (
          <span className="inline-flex items-center gap-2">
            <Button
              small
              disabled={setDisabled.isPending}
              onClick={() =>
                setDisabled.mutate({ id: u.id, disabled: !disabled })
              }
            >
              {disabled ? "Enable" : "Disable"}
            </Button>
            {confirming ? (
              <>
                <Button
                  small
                  danger
                  disabled={remove.isPending}
                  onClick={() => remove.mutate(u.id)}
                >
                  Delete {u.name}
                </Button>
                <Button small onClick={() => setConfirming(false)}>
                  Cancel
                </Button>
              </>
            ) : (
              <Button small onClick={() => setConfirming(true)}>
                Delete
              </Button>
            )}
            <ErrorText error={setDisabled.error ?? remove.error} />
          </span>
        )}
      </td>
    </tr>
  );
}

const roles: Scope[] = ["deploy", "read", "admin"];

/** Adds a person with a first password they change after signing in. */
function AddUser() {
  const create = useCreateUser();
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Scope>("deploy");
  return (
    <form
      aria-label="Add user"
      className="flex flex-wrap items-center gap-2.5"
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate(
          { email: email.trim(), name: name.trim(), password, role },
          {
            onSuccess: () => {
              setEmail("");
              setName("");
              setPassword("");
            },
          },
        );
      }}
    >
      <span className="text-mut">Add</span>
      <input
        type="email"
        aria-label="Email"
        placeholder="name@company.com"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
        className="field min-w-0 flex-[1_1_220px] py-[7px]"
      />
      <input
        aria-label="Name"
        placeholder="name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        className="field w-36 py-[7px]"
      />
      <input
        type="password"
        aria-label="First password"
        placeholder="first password"
        autoComplete="new-password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        className="field w-44 py-[7px]"
      />
      <select
        aria-label="Role"
        value={role}
        onChange={(e) =>
          setRole(roles.find((r) => r === e.target.value) ?? "read")
        }
        className="field py-[7px]"
      >
        {roles.map((r) => (
          <option key={r}>{r}</option>
        ))}
      </select>
      <button
        type="submit"
        className="btn pri"
        disabled={create.isPending || !email || !name || !password}
      >
        Add user
      </button>
      <ErrorText error={create.error} />
    </form>
  );
}
