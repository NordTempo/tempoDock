import { useSuspenseQuery } from "@tanstack/react-query";
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
import {
  platformSecurityQuery,
  useSetPlatformSecurity,
} from "../features/console/security";
import { roleNote } from "../features/org/data";
import { useOrg } from "../shell/location";
import { CliHint } from "../ui/CliHint";
import { day, plural } from "../ui/format";
import {
  Act,
  Button,
  Check,
  Confirm,
  CopyButton,
  ErrorText,
  Glyph,
  Page,
  PageBar,
  Panel,
} from "../ui/kit";
import { notify } from "../ui/notice";

// People who sign in to the UI. Nobody can disable, delete or reset
// themselves here.
export const Route = createFileRoute("/_app/console/users")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(queries.users()),
      queryClient.ensureQueryData(platformSecurityQuery()),
    ]),
  component: Users,
});

/** A generated password, shown once. */
type Reset = { email: string; password: string };

function Users() {
  const users = useUsers();
  const me = useSession();
  const org = useOrg();
  const [reset, setReset] = useState<Reset>();
  const [adding, setAdding] = useState(false);
  const active = users.filter((u) => !u.disabled_at).length;

  return (
    <>
      <PageBar
        title="Users"
        after={
          <span className="text-xs text-faint">
            {active} active
            {users.length > active && ` · ${users.length - active} disabled`}
          </span>
        }
        actions={
          !adding && (
            <Button primary onClick={() => setAdding(true)}>
              Add user
            </Button>
          )
        }
      />
      <Page width="max-w-[1240px]">
        <div className="flex flex-wrap items-start gap-x-8 gap-y-7">
          <div className="flex min-w-0 flex-[999_1_640px] flex-col gap-6">
            {adding && <AddUser onClose={() => setAdding(false)} />}
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
                    It is shown only this once. Their sessions ended; they sign
                    in with it next.
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
                    <th title="Whether they sign in with a second factor">
                      Two-factor
                    </th>
                    <th title="Signed-in browser sessions and usable tokens they created">
                      Access
                    </th>
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
              your password on{" "}
              <Link to="/account" className="text-info hover:underline">
                your account
              </Link>
              . Tokens for agents, CI and scripts are in{" "}
              <Link
                to="/$org/settings/tokens"
                params={{ org }}
                className="text-info hover:underline"
              >
                the organization's settings
              </Link>
              .
            </p>
            <OperatorsTwoFactor />
          </div>
          <RoleGuide />
        </div>
      </Page>
    </>
  );
}

/**
 * The platform's sign-in policy (ADR 0057): whether operators run the
 * platform only after signing in with a second factor.
 */
function OperatorsTwoFactor() {
  const { operators_two_factor: on } = useSuspenseQuery(
    platformSecurityQuery(),
  ).data;
  const set = useSetPlatformSecurity();
  return (
    <div className="flex flex-col gap-1 border-t border-line pt-4">
      <CliHint
        cli={`dock security operators-two-factor ${on ? "off" : "on"}`}
        className="act"
      >
        <Check
          label="Require two-factor for operators"
          checked={on}
          disabled={set.isPending}
          onChange={(require) => set.mutate(require)}
        />
      </CliHint>
      <span className="text-xs text-mut">
        Without one, operators keep their organizations but not the console.
      </span>
      <ErrorText error={set.error} />
    </div>
  );
}

/**
 * What each role can do, beside the users table: the same words as the
 * organization's Members page (roleNote), plus what operators add.
 */
function RoleGuide() {
  return (
    <aside
      aria-labelledby="roles"
      className="flex max-w-[360px] min-w-0 flex-[1_1_280px] flex-col gap-3"
    >
      <h2 id="roles" className="font-semibold">
        What each role can do
      </h2>
      <dl className="panel">
        {(["read", "deploy", "admin"] as const).map((role) => (
          <div
            key={role}
            className="border-b border-soft px-3.5 py-3 last:border-b-0"
          >
            <dt className="font-medium">{role}</dt>
            <dd className="text-xs text-mut">{roleNote[role]}</dd>
          </div>
        ))}
        <div className="px-3.5 py-3">
          <dt className="font-medium text-warn">operator</dt>
          <dd className="text-xs text-mut">
            also runs this console: servers, users, backups and updates
          </dd>
        </div>
      </dl>
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
  // A password to set instead of a generated one (--password).
  const [password, setPassword] = useState("");
  const disabled = !!u.disabled_at;
  const close = () => {
    setDisabled.reset();
    remove.reset();
    resetPassword.reset();
    setPassword("");
    setAsking(null);
  };
  // Live sessions and usable tokens, as on Members: "2 sessions · 1 token".
  const access = [
    u.sessions > 0 && plural(u.sessions, "session"),
    u.tokens > 0 && plural(u.tokens, "token"),
  ]
    .filter(Boolean)
    .join(" · ");
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
          setDisabled.mutate(
            { id: u.id, disabled: true },
            {
              onSuccess: () => {
                close();
                notify(`${u.name} disabled`);
              },
            },
          )
        }
        onCancel={close}
      >
        <b>{u.email}</b> can't sign in. {sessions} {tokens} Enable them again
        any time.
      </Confirm>
    ),
    delete: (
      <Confirm
        danger
        action={`Delete ${u.name}`}
        cli={`dock user delete ${u.email}`}
        pending={remove.isPending}
        error={remove.error}
        onConfirm={() =>
          remove.mutate(u.id, {
            onSuccess: () => {
              close();
              notify(`${u.name} deleted`);
            },
          })
        }
        onCancel={close}
      >
        The account of <b>{u.email}</b> is gone and they can't sign in.{" "}
        {sessions} Their audit history stays. {tokens}
      </Confirm>
    ),
    reset: (
      <Confirm
        action="Reset password"
        title={`Reset the password of ${u.email}?`}
        cli={`dock user reset-password ${u.email}${password ? " --password …" : ""}`}
        pending={resetPassword.isPending}
        error={resetPassword.error}
        onConfirm={() =>
          resetPassword.mutate(
            { id: u.id, password },
            {
              onSuccess: (r) => {
                close();
                if (r.password)
                  onReset({ email: u.email, password: r.password });
              },
            },
          )
        }
        onCancel={close}
      >
        {password
          ? "They sign in with the one typed here."
          : "A new one is generated and shown once."}{" "}
        {sessions} Their dock login tokens are revoked.
        <input
          type="password"
          aria-label="New password, optional"
          placeholder="or type one, 12+ characters"
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          className="field mt-2 block w-64 py-[5px]"
        />
      </Confirm>
    ),
  };

  return (
    <tr
      className={`*:align-middle ${disabled ? "text-faint" : "hover:bg-hover"}`}
    >
      <td className="py-2.5">
        {/* The confirm is a modal; this cell only holds it. */}
        {asking && (
          <span className="whitespace-normal">{confirms[asking]}</span>
        )}
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
      <td className="py-2.5">
        {u.role}
        {u.operator && <span className="text-warn"> · operator</span>}
      </td>
      <td className="py-2.5">
        {disabled && u.disabled_at ? (
          `disabled ${day(u.disabled_at)}`
        ) : (
          <span className="text-ok">
            <Glyph char="●" /> active
          </span>
        )}
      </td>
      <td className="py-2.5">
        {u.two_factor ? (
          <span className="text-ok">
            <Glyph char="●" /> on
          </span>
        ) : (
          <span className="text-faint">
            <Glyph char="○" /> off
          </span>
        )}
      </td>
      <td className={`py-2.5 ${access ? "text-mut" : "text-faint"}`}>
        {access || "none"}
      </td>
      <td className="py-2.5 text-faint">{day(u.created_at)}</td>
      <td className="w-full py-1.5 text-right">
        {!you && (
          // Text buttons, so three fit beside the role guide; each title is
          // its CLI equivalent, and each confirm shows it too.
          <span className="inline-flex gap-3">
            {disabled ? (
              <button
                type="button"
                className="lnk"
                title={`dock user enable ${u.email}`}
                disabled={setDisabled.isPending}
                onClick={() =>
                  setDisabled.mutate(
                    { id: u.id, disabled: false },
                    { onSuccess: () => notify(`${u.name} enabled`) },
                  )
                }
              >
                Enable
              </button>
            ) : (
              <button
                type="button"
                className="lnk"
                title={`dock user disable ${u.email}`}
                onClick={() => setAsking("disable")}
              >
                Disable
              </button>
            )}
            <button
              type="button"
              className="lnk"
              title={`dock user reset-password ${u.email}`}
              onClick={() => setAsking("reset")}
            >
              Reset password
            </button>
            <button
              type="button"
              className="lnk text-fail"
              title={`dock user delete ${u.email}`}
              onClick={() => setAsking("delete")}
            >
              Delete
            </button>
          </span>
        )}
        {!asking && <ErrorText error={setDisabled.error} />}
      </td>
    </tr>
  );
});

const roles: Scope[] = ["read", "deploy", "admin"];

/**
 * Adds a person with a first password they change after signing in;
 * opened from the bar's Add user.
 */
function AddUser({ onClose }: { onClose: () => void }) {
  const create = useCreateUser();
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Scope>("deploy");
  const [operator, setOperator] = useState(false);
  const label = "flex flex-col gap-1.5 text-xs text-mut";
  return (
    <form
      aria-label="Add a user"
      className="flex flex-col gap-3 border-b border-line pb-5"
      onKeyDown={(e) => e.key === "Escape" && onClose()}
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate(
          { email: email.trim(), name: name.trim(), password, role, operator },
          {
            onSuccess: () => {
              notify(`${name.trim()} added`);
              onClose();
            },
          },
        );
      }}
    >
      <div className="flex flex-wrap items-end gap-3">
        <label className={`${label} min-w-0 flex-[2_1_220px]`}>
          Email
          <input
            type="email"
            placeholder="name@company.com"
            // biome-ignore lint/a11y/noAutofocus: opened by "Add user"
            autoFocus
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            className="field py-[7px] text-sm text-fg"
          />
        </label>
        <label className={`${label} min-w-0 flex-[1_1_150px]`}>
          Name
          <input
            placeholder="Ada Lovelace"
            value={name}
            onChange={(e) => setName(e.target.value)}
            className="field py-[7px] text-sm text-fg"
          />
        </label>
        <label className={`${label} min-w-0 flex-[1_1_180px]`}>
          Password, 12+ characters
          <input
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="field py-[7px] text-sm text-fg"
          />
        </label>
        <label className={label}>
          Role
          <select
            value={role}
            onChange={(e) =>
              setRole(roles.find((r) => r === e.target.value) ?? "read")
            }
            className="py-[7px] text-sm text-fg"
          >
            {roles.map((r) => (
              <option key={r}>{r}</option>
            ))}
          </select>
        </label>
      </div>
      <Check
        label="Operator: also runs this console"
        checked={operator}
        onChange={setOperator}
      />
      <div className="flex flex-wrap items-center gap-2.5">
        <Act
          primary
          type="submit"
          cli={`dock user create --email ${email.trim() || "<email>"} --name ${JSON.stringify(name.trim() || "<name>")} --role ${role}${operator ? " --operator" : ""}`}
          disabled={create.isPending}
        >
          Add user
        </Act>
        <Button onClick={onClose}>Cancel</Button>
        <ErrorText error={create.error} />
      </div>
    </form>
  );
}
