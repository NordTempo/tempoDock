import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { memo, type ReactNode, useState } from "react";
import {
  useCreateUser,
  useDeleteUser,
  useResetPassword,
  useSetUserDisabled,
} from "../data/mutations";
import { useSession } from "../data/queries";
import type { Scope } from "../data/types";
import {
  type Account,
  accountsQuery,
  useAccounts,
  useSetOperator,
} from "../features/console/accounts";
import {
  platformSecurityQuery,
  useSetPlatformSecurity,
} from "../features/console/security";
import { Saved, useValidation } from "../features/create/form";
import { PersonCell, TwoFactorState } from "../features/org/people";
import { consoleOrgsQuery, useConsoleOrgs } from "../features/orgs/data";
import { CliHint } from "../ui/CliHint";
import { day } from "../ui/format";
import { Icon } from "../ui/icons";
import {
  Act,
  Button,
  Check,
  Confirm,
  CopyButton,
  ErrorText,
  Glyph,
  Menu,
  menuItem,
  Page,
  PageBar,
  Panel,
} from "../ui/kit";
import { notify } from "../ui/notice";

// User accounts, the platform's (ADR 0062): every account, the orgs it
// belongs to and owns, and who operates the platform. Roles in an org are
// that org's own (its Members page). Nobody can disable, delete or reset
// themselves here.
export const Route = createFileRoute("/_app/console/users")({
  loader: ({ context: { queryClient } }) =>
    Promise.all([
      queryClient.ensureQueryData(accountsQuery()),
      queryClient.ensureQueryData(consoleOrgsQuery()),
      queryClient.ensureQueryData(platformSecurityQuery()),
    ]),
  component: Users,
});

/** A generated password, shown once. */
type Reset = { email: string; password: string };

function Users() {
  const users = useAccounts();
  const me = useSession();
  const [reset, setReset] = useState<Reset>();
  const [adding, setAdding] = useState(false);

  return (
    <>
      <PageBar
        title="Users"
        actions={
          !adding && (
            <Button primary onClick={() => setAdding(true)}>
              Add user
            </Button>
          )
        }
      />
      <Page>
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
                    <th>Organizations</th>
                    <th>Status</th>
                    <th title="Whether they sign in with a second factor">
                      Two-factor
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
              <Link
                to="/account"
                className="text-info underline decoration-info/50 underline-offset-[3px] hover:decoration-info"
              >
                your account
              </Link>
              . Tokens for agents, CI and scripts belong to each organization,
              in its settings.
            </p>
            <OperatorsTwoFactor />
          </div>
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
      <span className="flex flex-wrap items-center gap-2">
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
        <Saved ok={set.isSuccess} />
      </span>
      <span className="text-xs text-mut">
        Without one, operators keep their organizations but not the console.
      </span>
      <ErrorText error={set.error} />
    </div>
  );
}

type Asking = "disable" | "delete" | "reset" | "operator" | null;

const UserRow = memo(function UserRow({
  user: u,
  you,
  onReset,
}: {
  user: Account;
  you: boolean;
  onReset: (reset: Reset) => void;
}) {
  const setOperator = useSetOperator();
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
  const sessions = "Their sessions end now.";
  const tokens =
    "Their dock login tokens are revoked; tokens they created by hand keep working until revoked in their organization's Tokens.";

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
    operator: (
      <Confirm
        danger={u.operator}
        action={u.operator ? "Stop operating" : "Make operator"}
        title={
          u.operator
            ? `Take the console from ${u.email}?`
            : `Make ${u.email} a platform operator?`
        }
        pending={setOperator.isPending}
        error={setOperator.error}
        onConfirm={() =>
          setOperator.mutate(
            { id: u.id, operator: !u.operator },
            { onSuccess: close },
          )
        }
        onCancel={close}
      >
        {u.operator
          ? "They keep their organizations, but no longer run the console: servers, users, backups and updates."
          : "They run this console too: servers, users, every organization's administration, backups and updates. Their organizations' content stays theirs to join."}
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
        <PersonCell name={u.name} email={u.email} you={you} />
      </td>
      <td className="py-2.5 whitespace-normal">
        {u.operator && (
          <span className="block text-2xs text-warn">platform operator</span>
        )}
        {(u.orgs ?? []).length === 0 && !u.operator && (
          <span className="text-faint">none</span>
        )}
        {(u.orgs ?? []).map((o) => (
          <span key={o.org} className="mr-3 inline-block text-xs">
            <Link to="/console/orgs" search={{ org: o.org }}>
              {o.org}
            </Link>{" "}
            <span className="text-mut">
              {o.owner ? "owner" : o.role}
              {o.expires_at && ", joined as operator"}
            </span>
          </span>
        ))}
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
        <TwoFactorState on={u.two_factor} />
      </td>
      <td className="py-2.5 text-faint">{day(u.created_at)}</td>
      <td className="w-full py-1.5 text-right">
        {!you && (
          // Small bordered buttons, like Members; each shows its CLI
          // equivalent, and each confirm too.
          <span className="inline-flex gap-1.5">
            {disabled ? (
              <Act
                small
                cli={`dock user enable ${u.email}`}
                disabled={setDisabled.isPending}
                onClick={() =>
                  setDisabled.mutate(
                    { id: u.id, disabled: false },
                    { onSuccess: () => notify(`${u.name} enabled`) },
                  )
                }
              >
                Enable
              </Act>
            ) : (
              <Act
                small
                cli={`dock user disable ${u.email}`}
                onClick={() => setAsking("disable")}
              >
                Disable
              </Act>
            )}
            {/* The rest in the row's menu, like token rows. */}
            <Menu
              label={`${u.name}: more actions`}
              right
              className="inline-flex"
              button={
                <span className="grid h-6 w-7 place-items-center rounded-ctl text-sec hover:bg-hover hover:text-fg">
                  <Icon name="more" size={14} />
                </span>
              }
            >
              {(close) => (
                <>
                  <button
                    type="button"
                    role="menuitem"
                    className={menuItem}
                    onClick={() => {
                      close();
                      setAsking("operator");
                    }}
                  >
                    {u.operator
                      ? "Stop operating the platform"
                      : "Make platform operator"}
                  </button>
                  <button
                    type="button"
                    role="menuitem"
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
                    role="menuitem"
                    className={`${menuItem} text-fail hover:text-fail`}
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
  // The least privilege, as on Members' invite form.
  const [role, setRole] = useState<Scope>("read");
  const [operator, setOperator] = useState(false);
  const orgs = useConsoleOrgs();
  const [org, setOrg] = useState(orgs[0]?.slug ?? "");
  const v = useValidation();
  const emailOk = /^[^\s@]+@[^\s@]+$/.test(email.trim());
  const errors = {
    email:
      v.required(!email.trim(), "Enter their email address.") ??
      v.required(!emailOk, "Enter an email address, like name@company.com."),
    name: v.required(!name.trim(), "Enter their name."),
    password:
      (password && password.length < 12
        ? "Use at least 12 characters."
        : undefined) ?? v.required(!password, "Enter a first password."),
  };
  const field = (error: string | undefined) =>
    `field text-sm text-fg ${error ? "border-fail" : ""}`;
  const label = "flex flex-col gap-1.5 text-xs text-mut";
  return (
    <form
      ref={v.form}
      noValidate
      aria-label="Add a user"
      className="flex flex-col gap-3 border-b border-line pb-5"
      onKeyDown={(e) => e.key === "Escape" && onClose()}
      onSubmit={(e) => {
        e.preventDefault();
        if (!v.check(emailOk && !!name.trim() && password.length >= 12)) {
          return;
        }
        create.mutate(
          {
            email: email.trim(),
            name: name.trim(),
            password,
            org,
            role,
            operator,
          },
          {
            onSuccess: () => {
              notify(`${name.trim()} added`);
              onClose();
            },
          },
        );
      }}
    >
      <div className="flex flex-wrap items-start gap-3">
        <label className={`${label} min-w-0 flex-[2_1_220px]`}>
          Email
          <input
            type="email"
            placeholder="name@company.com"
            // biome-ignore lint/a11y/noAutofocus: opened by "Add user"
            autoFocus
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            aria-invalid={!!errors.email}
            className={field(errors.email)}
          />
          {errors.email && <span className="text-fail">{errors.email}</span>}
        </label>
        <label className={`${label} min-w-0 flex-[1_1_150px]`}>
          Name
          <input
            placeholder="Ada Lovelace"
            value={name}
            onChange={(e) => setName(e.target.value)}
            aria-invalid={!!errors.name}
            className={field(errors.name)}
          />
          {errors.name && <span className="text-fail">{errors.name}</span>}
        </label>
        <label className={`${label} min-w-0 flex-[1_1_180px]`}>
          Password, 12+ characters
          <input
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            aria-invalid={!!errors.password}
            className={field(errors.password)}
          />
          {errors.password && (
            <span className="text-fail">{errors.password}</span>
          )}
        </label>
        <label className={label}>
          Organization
          <select
            value={org}
            onChange={(e) => setOrg(e.target.value)}
            className="py-[7px] text-sm text-fg"
          >
            {orgs.map((o) => (
              <option key={o.slug} value={o.slug}>
                {o.slug}
              </option>
            ))}
          </select>
        </label>
        <label className={label}>
          Role there
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
        {create.error ? (
          <ErrorText error={create.error} />
        ) : (
          <span className="text-xs text-fail">
            {v.problem(
              [
                !email.trim() && "an email",
                !name.trim() && "a name",
                !password && "a password",
              ],
              true,
            )}
          </span>
        )}
      </div>
    </form>
  );
}
