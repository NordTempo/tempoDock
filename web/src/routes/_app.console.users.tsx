import { createFileRoute, Link } from "@tanstack/react-router";
import { memo, type ReactNode, useState } from "react";
import {
  useCreateUser,
  useDeleteUser,
  useResetPassword,
  useSetUserDisabled,
} from "../data/mutations";
import { queries, useSession, useUsers } from "../data/queries";
import type { Scope, User } from "../data/types";
import { day, plural } from "../ui/format";
import {
  Button,
  Confirm,
  CopyButton,
  ErrorText,
  Glyph,
  Menu,
  menuItem,
  Page,
  Panel,
  TitleRow,
} from "../ui/kit";

// People who sign in to the UI. Nobody can disable, delete or reset
// themselves here.
export const Route = createFileRoute("/_app/console/users")({
  loader: ({ context: { queryClient } }) =>
    queryClient.ensureQueryData(queries.users()),
  component: Users,
});

/** A generated password, shown once. */
type Reset = { email: string; password: string };

function Users() {
  const users = useUsers();
  const me = useSession();
  const [reset, setReset] = useState<Reset>();
  const active = users.filter((u) => !u.disabled_at).length;

  return (
    <Page width="max-w-[1240px]">
      <div className="flex flex-wrap items-start gap-x-8 gap-y-7">
        <div className="flex min-w-0 flex-[999_1_640px] flex-col gap-6">
          <TitleRow
            title="Users"
            context={`People who sign in. ${active} active${users.length > active ? `, ${users.length - active} disabled` : ""}. Roles match token scopes.`}
          />
          <AddUser />
          {reset && (
            <section
              aria-labelledby="new-password"
              className="flex flex-col gap-2.5 rounded-panel border border-ok-edge bg-ok-bg/30 p-4"
            >
              <h2 id="new-password" className="font-semibold">
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
              <div className="flex flex-wrap items-center gap-2.5 rounded-ctl border border-line bg-side px-3 py-2.5">
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
          <Panel>
            <table>
              <thead>
                <tr>
                  <th>Person</th>
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
          </Panel>
          <p className="text-2xs text-faint">
            Your own account can't be disabled, deleted or reset here. Change
            your password with{" "}
            <span className="font-mono">dock me password</span>. Tokens for
            agents, CI and scripts are on{" "}
            <Link to="/console/tokens" className="text-info hover:underline">
              Tokens
            </Link>
            .
          </p>
        </div>
        <RoleGuide />
      </div>
    </Page>
  );
}

const roleText: Record<Scope, string> = {
  admin:
    "Everything: members, tokens, alerts, data access and servers. Reveals secret values.",
  deploy:
    "Deploy, roll back, restart, edit variables and settings. Values stay hidden.",
  read: "Status, deploys, logs and metrics. Read only.",
};

/** What each role may do, next to the members table (A4). */
function RoleGuide() {
  return (
    <aside
      aria-labelledby="roles"
      className="flex min-w-0 flex-[1_1_260px] flex-col gap-3"
    >
      <h2 id="roles" className="text-sm font-semibold">
        What each role can do
      </h2>
      <dl className="panel">
        {(["admin", "deploy", "read"] as const).map((role) => (
          <div
            key={role}
            className="border-b border-line px-3.5 py-3 last:border-0"
          >
            <dt className="font-mono">{role}</dt>
            <dd className="text-xs text-mut">{roleText[role]}</dd>
          </div>
        ))}
      </dl>
      <p className="text-2xs leading-relaxed text-faint">
        A token can be narrowed to one project or environment, and never exceeds
        the person who made it.
      </p>
    </aside>
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
      ? "Their dock login tokens are revoked; tokens they created by hand keep working until revoked on Tokens."
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
          A new one is generated and shown once. {sessions} Their dock login
          tokens are revoked.
        </span>
      </Confirm>
    ),
  };

  return (
    <>
      <tr
        className={`*:align-middle ${disabled ? "text-faint" : "hover:bg-hover"}`}
      >
        <td className="py-2.5">
          <span className="flex items-center gap-2.5">
            <span className="grid size-6 flex-none place-items-center rounded-full bg-edge text-2xs font-semibold text-fg">
              {u.name.slice(0, 1).toUpperCase()}
            </span>
            <span>
              <span className="font-medium">{u.name}</span>
              {you && <span className="text-faint"> (you)</span>}
              <span className="block text-2xs text-faint">{u.email}</span>
            </span>
          </span>
        </td>
        <td className="py-2.5 font-mono">{u.role}</td>
        <td className="py-2.5">
          {disabled && u.disabled_at ? (
            `disabled ${day(u.disabled_at)}`
          ) : (
            <span className="text-ok">
              <Glyph char="●" /> active
            </span>
          )}
        </td>
        <td className={`py-2.5 font-mono ${u.sessions ? "" : "text-faint"}`}>
          {u.sessions}
        </td>
        <td className={`py-2.5 font-mono ${u.tokens ? "" : "text-faint"}`}>
          {u.tokens}
        </td>
        <td className="py-2.5 text-faint">{day(u.created_at)}</td>
        <td className="w-full py-1.5 text-right">
          {!you && (
            // CLI equivalents show as tooltips here and in each confirm.
            <Menu
              label={`More for ${u.name}`}
              right
              className="inline-flex"
              button={
                <span className="rounded-ctl px-2 py-0.5 text-mut hover:bg-panel hover:text-fg">
                  ···
                </span>
              }
            >
              {(close) => (
                <>
                  {disabled ? (
                    <button
                      type="button"
                      className={menuItem}
                      title={`dock user enable ${u.email}`}
                      disabled={setDisabled.isPending}
                      onClick={() => {
                        close();
                        setDisabled.mutate({ id: u.id, disabled: false });
                      }}
                    >
                      Enable
                    </button>
                  ) : (
                    <button
                      type="button"
                      className={menuItem}
                      title={`dock user disable ${u.email}`}
                      onClick={() => {
                        close();
                        setAsking("disable");
                      }}
                    >
                      Disable
                    </button>
                  )}
                  <button
                    type="button"
                    className={menuItem}
                    title={`dock user reset-password ${u.email}`}
                    onClick={() => {
                      close();
                      setAsking("reset");
                    }}
                  >
                    Reset password
                  </button>
                  <button
                    type="button"
                    className={`${menuItem} text-fail`}
                    title={`dock user delete ${u.email}`}
                    onClick={() => {
                      close();
                      setAsking("delete");
                    }}
                  >
                    Delete
                  </button>
                </>
              )}
            </Menu>
          )}
          {!asking && <ErrorText error={setDisabled.error} />}
        </td>
      </tr>
      {asking && (
        <tr>
          <td colSpan={7} className="border-0 pb-3">
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
      aria-label="Add a member"
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
        Add member
      </button>
      <ErrorText error={create.error} />
    </form>
  );
}
