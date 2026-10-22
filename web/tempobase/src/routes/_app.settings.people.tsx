import { createFileRoute, redirect } from "@tanstack/react-router";
import { useState } from "react";
import {
  Btn,
  CopyBtn,
  ErrorText,
  Head,
  initials,
  Scroll,
} from "../../../src/features/data/kit";
import { day } from "../../../src/ui/format";
import type { Link as InviteLink, Role, User } from "../api/types";
import {
  queries,
  useCreateInvite,
  useDeleteInvite,
  useDeleteUser,
  useInvites,
  useMe,
  useResetLink,
  useUpdateUser,
  useUsers,
} from "../data/queries";

// People and invites (admins). One role per person; what each role may do
// with the data is the policy's (Access).
export const Route = createFileRoute("/_app/settings/people")({
  beforeLoad: async ({ context }) => {
    const me = await context.queryClient.ensureQueryData(queries.me());
    if (me?.role !== "admin") throw redirect({ to: "/" });
  },
  component: People,
});

const roles: Role[] = ["viewer", "editor", "admin"];
const roleNames: Record<Role, string> = {
  viewer: "Viewer",
  editor: "Editor",
  admin: "Admin",
};

function People() {
  const users = useUsers();
  const invites = useInvites();
  const [inviting, setInviting] = useState(false);
  // A just-made invite or reset link: shown once, with its url.
  const [made, setMade] = useState<InviteLink | null>(null);

  return (
    <>
      <Head
        title="People"
        actions={
          <Btn
            primary
            aria-expanded={inviting}
            onClick={() => setInviting((v) => !v)}
          >
            Invite
          </Btn>
        }
      >
        <span className="text-xs text-(--g-mut)">
          One role each. Admins see every connection.
        </span>
      </Head>
      <main className="flex max-w-[860px] flex-col gap-7 px-5 py-6">
        {inviting && (
          <InviteForm
            onMade={(link) => {
              setMade(link);
              setInviting(false);
            }}
          />
        )}
        {made && <MadeLink link={made} onDone={() => setMade(null)} />}

        <section className="flex flex-col gap-2">
          <h2 className="text-[13px] font-semibold">
            People{" "}
            <span className="font-normal text-(--g-mut)">
              {users.data?.length}
            </span>
          </h2>
          <ErrorText error={users.error} />
          <Scroll>
            <table className="g-table min-w-[620px]">
              <tbody>
                {users.data?.map((u) => (
                  <Person key={u.id} user={u} onLink={setMade} />
                ))}
              </tbody>
            </table>
          </Scroll>
        </section>

        {!!invites.data?.length && (
          <section className="flex flex-col gap-2">
            <h2 className="text-[13px] font-semibold">
              Open invites{" "}
              <span className="font-normal text-(--g-mut)">
                {invites.data.length}
              </span>
            </h2>
            <Scroll>
              <table className="g-table min-w-[620px]">
                <tbody>
                  {invites.data.map((i) => (
                    <Invite key={i.id} invite={i} />
                  ))}
                </tbody>
              </table>
            </Scroll>
          </section>
        )}

        <section className="flex flex-col gap-2">
          <h2 className="text-[13px] font-semibold">Roles</h2>
          <dl className="g-box text-[13px]">
            {(
              [
                ["Viewer", "Browse tables and run read-only queries."],
                [
                  "Editor",
                  "Also change rows. Every change is reviewed as a changeset and logged.",
                ],
                [
                  "Admin",
                  "Everything: connections, people, the policy, approving agents' proposals.",
                ],
              ] as const
            ).map(([name, text]) => (
              <div
                key={name}
                className="grid grid-cols-[64px_minmax(0,1fr)] gap-3 px-3 py-2.5"
              >
                <dt className="font-medium">{name}</dt>
                <dd className="text-(--g-dim)">{text}</dd>
              </div>
            ))}
          </dl>
          <p className="text-xs text-(--g-mut)">
            Defaults; the policy on Access can change what viewers and editors
            may do, per connection.
          </p>
        </section>
      </main>
    </>
  );
}

function InviteForm({ onMade }: { onMade: (link: InviteLink) => void }) {
  const create = useCreateInvite();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  return (
    <form
      className="g-box flex flex-wrap items-end gap-3 p-3"
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate({ email: email.trim(), role }, { onSuccess: onMade });
      }}
    >
      <label className="flex flex-col gap-1.5">
        <span className="text-xs text-(--g-dim)">Email</span>
        <input
          className="g-field h-8 w-64"
          type="email"
          required
          // biome-ignore lint/a11y/noAutofocus: opened on request
          autoFocus
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
      </label>
      <RoleSelect value={role} onChange={setRole} />
      <Btn primary type="submit" disabled={create.isPending} className="h-8">
        Create invite link
      </Btn>
      <ErrorText error={create.error} />
      <p className="basis-full text-xs text-(--g-mut)">
        tempoBase sends no email: copy the link to the person. It works once,
        for 7 days.
      </p>
    </form>
  );
}

/** A link with its url, shown once after it was made. */
function MadeLink({ link, onDone }: { link: InviteLink; onDone: () => void }) {
  return (
    <div className="g-box flex flex-col gap-2 p-3">
      <p>
        {link.kind === "invite" ? "Invite" : "Reset link"} for {link.email},
        valid until {day(link.expires_at)}. Copy it now: it isn't shown again.
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <code className="min-w-0 flex-1 font-mono text-xs break-all text-(--g-code)">
          {link.url}
        </code>
        {link.url && <CopyBtn text={link.url} />}
        <Btn small ghost onClick={onDone}>
          Done
        </Btn>
      </div>
    </div>
  );
}

function RoleSelect({
  value,
  onChange,
  label = "Role",
  disabled,
}: {
  value: Role;
  onChange: (role: Role) => void;
  label?: string;
  disabled?: boolean;
}) {
  return (
    <label className="flex flex-col gap-1.5">
      <span className="text-xs text-(--g-dim)">{label}</span>
      <select
        className="g-field h-8"
        value={value}
        disabled={disabled}
        onChange={(e) => {
          const role = roles.find((r) => r === e.target.value);
          if (role) onChange(role);
        }}
      >
        {roles.map((r) => (
          <option key={r} value={r}>
            {roleNames[r]}
          </option>
        ))}
      </select>
    </label>
  );
}

function Person({
  user: u,
  onLink,
}: {
  user: User;
  onLink: (link: InviteLink) => void;
}) {
  const me = useMe();
  const update = useUpdateUser();
  const remove = useDeleteUser();
  const reset = useResetLink();
  const self = me.email === u.email;
  const [confirming, setConfirming] = useState(false);
  return (
    <tr className={u.disabled ? "opacity-60" : ""}>
      <td className="w-full py-2">
        <span className="flex min-w-0 items-center gap-2.5">
          <span className="g-avatar h-6! w-6! flex-[0_0_24px]!" aria-hidden>
            {initials(u.name)}
          </span>
          <span className="min-w-0">
            <span className="block font-medium">
              {u.name}
              {self && <span className="font-normal text-(--g-mut)"> you</span>}
            </span>
            <span className="block text-xs text-(--g-mut)">
              {u.email}
              {u.disabled && " · disabled"}
            </span>
          </span>
        </span>
      </td>
      <td>
        <select
          className="g-field h-7"
          aria-label={`${u.name}'s role`}
          value={u.role}
          disabled={self || update.isPending}
          onChange={(e) => {
            const role = roles.find((r) => r === e.target.value);
            if (role) update.mutate({ id: u.id, role });
          }}
        >
          {roles.map((r) => (
            <option key={r} value={r}>
              {roleNames[r]}
            </option>
          ))}
        </select>
      </td>
      <td>
        <span className="flex items-center justify-end gap-1">
          <ErrorText error={update.error ?? remove.error ?? reset.error} />
          <Btn
            small
            ghost
            disabled={reset.isPending}
            onClick={() => reset.mutate(u.id, { onSuccess: onLink })}
          >
            Reset link
          </Btn>
          {!self && (
            <Btn
              small
              ghost
              disabled={update.isPending}
              onClick={() => update.mutate({ id: u.id, disabled: !u.disabled })}
            >
              {u.disabled ? "Enable" : "Disable"}
            </Btn>
          )}
          {!self &&
            (confirming ? (
              <Btn
                small
                danger
                disabled={remove.isPending}
                onBlur={() => setConfirming(false)}
                onClick={() => remove.mutate(u.id)}
              >
                Delete {u.name}
              </Btn>
            ) : (
              <Btn small ghost danger onClick={() => setConfirming(true)}>
                Delete
              </Btn>
            ))}
        </span>
      </td>
    </tr>
  );
}

function Invite({ invite }: { invite: InviteLink }) {
  const remove = useDeleteInvite();
  return (
    <tr>
      <td className="w-full">{invite.email}</td>
      <td className="text-(--g-dim)">
        {invite.role ? roleNames[invite.role] : ""}
      </td>
      <td className="text-xs text-(--g-mut)">until {day(invite.expires_at)}</td>
      <td className="text-right">
        <Btn
          small
          ghost
          danger
          disabled={remove.isPending}
          onClick={() => remove.mutate(invite.id)}
        >
          Withdraw
        </Btn>
      </td>
    </tr>
  );
}
