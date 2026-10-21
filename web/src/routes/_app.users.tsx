import { createFileRoute } from "@tanstack/react-router";
import { memo, type ReactNode, useState } from "react";
import {
  useCreateUser,
  useDeleteUser,
  useResetPassword,
  useSetUserDisabled,
} from "../data/mutations";
import { queries, useIsAdmin, useSession, useUsers } from "../data/queries";
import type { Scope, User } from "../data/types";
import { day, plural } from "../ui/format";
import {
  Button,
  Confirm,
  CopyButton,
  ErrorText,
  Page,
  Scroll,
  TitleRow,
} from "../ui/kit";

// People who sign in to the UI. Managing them is for admins (the API
// refuses everyone else), and nobody can disable, delete or reset
// themselves here.
export const Route = createFileRoute("/_app/users")({
  loader: async ({ context: { queryClient } }) => {
    const session = await queryClient.ensureQueryData(queries.session());
    if (session?.scope === "admin") {
      await queryClient.ensureQueryData(queries.users());
    }
  },
  component: UsersPage,
});

function UsersPage() {
  if (!useIsAdmin()) {
    return (
      <Page>
        <TitleRow title="Users" />
        <p className="text-mut">Only admins manage users.</p>
      </Page>
    );
  }
  return <Users />;
}

/** A generated password, shown once. */
type Reset = { email: string; password: string };

function Users() {
  const users = useUsers();
  const me = useSession();
  const [reset, setReset] = useState<Reset>();
  const active = users.filter((u) => !u.disabled_at).length;

  return (
    <Page>
      <TitleRow
        title="Users"
        context={`${active} active${users.length > active ? ` · ${users.length - active} disabled` : ""}. Roles match token scopes: read, deploy, admin.`}
      />
      <AddUser />
      {reset && (
        <section
          aria-labelledby="new-password"
          className="flex flex-col gap-2.5 border border-ok p-4"
        >
          <h2 id="new-password" className="text-[15px] font-semibold">
            <span className="text-ok">✓</span> New password for{" "}
            <span className="font-mono font-medium">{reset.email}</span>
          </h2>
          <p>
            <b className="font-semibold">Copy it now.</b>{" "}
            <span className="text-mut">
              It is shown only this once. Their sessions ended; they sign in
              with it next.
            </span>
          </p>
          <div className="flex flex-wrap items-center gap-2.5 border border-line bg-[#050505] px-3 py-2.5">
            <code className="min-w-0 flex-1 overflow-x-auto font-mono whitespace-nowrap">
              {reset.password}
            </code>
            <CopyButton text={reset.password} primary />
          </div>
          <div>
            <Button small onClick={() => setReset(undefined)}>
              I've passed it on
            </Button>
          </div>
        </section>
      )}
      <Scroll>
        <table>
          <thead>
            <tr>
              <th>User</th>
              <th>Email</th>
              <th>Role</th>
              <th>Status</th>
              <th title="Signed-in browser sessions">Sessions</th>
              <th title="Usable tokens they created">Tokens</th>
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
                onReset={setReset}
              />
            ))}
          </tbody>
        </table>
      </Scroll>
    </Page>
  );
}

type Asking = "disable" | "delete" | "reset" | null;

const UserRow = memo(function UserRow({
  user: u,
  you,
  onReset,
}: {
  user: User;
  you: boolean;
  onReset: (reset: Reset) => void;
}) {
  const setDisabled = useSetUserDisabled();
  const remove = useDeleteUser();
  const resetPassword = useResetPassword();
  const [asking, setAsking] = useState<Asking>(null);
  const disabled = !!u.disabled_at;
  const close = () => {
    setDisabled.reset();
    remove.reset();
    resetPassword.reset();
    setAsking(null);
  };
  const sessions = u.sessions
    ? `Their ${plural(u.sessions, "session")} end now.`
    : "They have no open sessions.";
  const tokens =
    u.tokens > 0
      ? `The ${plural(u.tokens, "token")} they created keep working until revoked on Tokens.`
      : "They have no tokens.";

  const confirms: Record<Exclude<Asking, null>, ReactNode> = {
    disable: (
      <Confirm
        danger
        action={`Disable ${u.name}`}
        cli={`dock user disable ${u.email}`}
        pending={setDisabled.isPending}
        error={setDisabled.error}
        onConfirm={() =>
          setDisabled.mutate({ id: u.id, disabled: true }, { onSuccess: close })
        }
        onCancel={close}
      >
        Disable <b className="font-semibold">{u.email}</b>?{" "}
        <span className="text-mut">
          They can't sign in. {sessions} {tokens} Enable them again any time.
        </span>
      </Confirm>
    ),
    delete: (
      <Confirm
        danger
        action={`Delete ${u.name}`}
        cli={`dock user delete ${u.email}`}
        pending={remove.isPending}
        error={remove.error}
        onConfirm={() => remove.mutate(u.id, { onSuccess: close })}
        onCancel={close}
      >
        Delete <b className="font-semibold">{u.email}</b>?{" "}
        <span className="text-mut">
          The account is gone and they can't sign in. {sessions} Their audit
          history stays. {tokens}
        </span>
      </Confirm>
    ),
    reset: (
      <Confirm
        action="Reset password"
        cli={`dock user reset-password ${u.email}`}
        pending={resetPassword.isPending}
        error={resetPassword.error}
        onConfirm={() =>
          resetPassword.mutate(u.id, {
            onSuccess: (r) => {
              close();
              if (r.password) onReset({ email: u.email, password: r.password });
            },
          })
        }
        onCancel={close}
      >
        Reset the password of <b className="font-semibold">{u.email}</b>?{" "}
        <span className="text-mut">
          A new one is generated and shown once. {sessions}
        </span>
      </Confirm>
    ),
  };

  return (
    <>
      <tr className={disabled ? "text-faint" : undefined}>
        <td className="py-2.5">
          ○ <b className="font-medium">{u.name}</b>{" "}
          {you && <span className="text-mut">you</span>}
        </td>
        <td className="py-2.5">{u.email}</td>
        <td className="py-2.5">{u.role}</td>
        <td className="py-2.5">
          {disabled && u.disabled_at ? (
            `disabled ${day(u.disabled_at)}`
          ) : (
            <span className="text-ok">● active</span>
          )}
        </td>
        <td className={`py-2.5 font-mono ${u.sessions ? "" : "text-mut"}`}>
          {u.sessions}
        </td>
        <td className={`py-2.5 font-mono ${u.tokens ? "" : "text-mut"}`}>
          {u.tokens}
        </td>
        <td className="py-2.5 text-mut">{day(u.created_at)}</td>
        <td className="w-full py-1.5 text-right">
          {you ? (
            <span className="text-xs text-mut">
              Your own account can't be disabled, deleted or reset here.
            </span>
          ) : (
            // CLI equivalents show as tooltips here and in each confirm.
            <span className="inline-flex items-center gap-2">
              {disabled ? (
                <Button
                  small
                  title={`dock user enable ${u.email}`}
                  disabled={setDisabled.isPending}
                  onClick={() =>
                    setDisabled.mutate({ id: u.id, disabled: false })
                  }
                >
                  Enable
                </Button>
              ) : (
                <Button
                  small
                  title={`dock user disable ${u.email}`}
                  onClick={() => setAsking("disable")}
                >
                  Disable
                </Button>
              )}
              <Button
                small
                title={`dock user reset-password ${u.email}`}
                onClick={() => setAsking("reset")}
              >
                Reset password
              </Button>
              <Button
                small
                title={`dock user delete ${u.email}`}
                onClick={() => setAsking("delete")}
              >
                Delete
              </Button>
            </span>
          )}
          {!asking && <ErrorText error={setDisabled.error} />}
        </td>
      </tr>
      {asking && (
        <tr>
          <td colSpan={8} className="border-0 pb-3">
            {confirms[asking]}
          </td>
        </tr>
      )}
    </>
  );
});

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
        placeholder="first password, 12+ characters"
        autoComplete="new-password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        className="field w-56 py-[7px]"
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
