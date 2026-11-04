import { useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useIsAdmin, useSession } from "../../data/queries";
import type { Scope } from "../../data/types";
import { day } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  CopyButton,
  ErrorText,
  Glyph,
  Page,
  Panel,
  TitleRow,
} from "../../ui/kit";
import {
  orgQueries,
  useCreateInvite,
  useRemoveMember,
  useRevokeInvite,
  useSetMemberRole,
} from "./data";

const roles: Scope[] = ["read", "deploy", "admin"];

/**
 * The organization's members and open invites (`dock org members`,
 * `dock org invite`). Admins only: the API refuses everyone else.
 */
export function MembersPage() {
  if (!useIsAdmin()) {
    return (
      <Page>
        <TitleRow title="Members" />
        <p className="text-mut">Only the organization's admins see members.</p>
      </Page>
    );
  }
  return <Members />;
}

/** A created invite's link, shown once. */
type Link = { email: string; url: string };

function Members() {
  const members = useSuspenseQuery(orgQueries.members()).data;
  const required = useSuspenseQuery(orgQueries.org()).data.require_two_factor;
  const without = members.filter((u) => !u.two_factor).length;
  const invites = useSuspenseQuery(orgQueries.invites()).data;
  const me = useSession();
  const [link, setLink] = useState<Link>();
  return (
    <Page width="max-w-[900px]" gap="gap-5">
      <TitleRow
        title="Members"
        context={`${members.length} ${members.length === 1 ? "person" : "people"}${invites.length ? ` · ${invites.length} invited` : ""}${without ? ` · ${without} without two-factor` : ""}`}
      />
      <InviteForm onCreated={setLink} />
      {link && (
        <section
          aria-labelledby="invite-link"
          className="flex flex-col gap-2.5 rounded-panel border border-ok-edge bg-ok-bg/30 p-4"
        >
          <h2 id="invite-link" className="font-semibold">
            <span className="text-ok">✓</span> Invite for{" "}
            <span className="font-mono font-medium">{link.email}</span>
          </h2>
          <p className="text-mut">
            Send them this link. It works once, for 7 days, and only signed in
            as {link.email}. It isn't shown again.
          </p>
          <div className="flex flex-wrap items-center gap-2.5 rounded-ctl border border-line bg-side px-3 py-2.5">
            <code className="min-w-0 flex-1 overflow-x-auto font-mono whitespace-nowrap">
              {link.url}
            </code>
            <CopyButton text={link.url} primary />
          </div>
          <div>
            <Button small onClick={() => setLink(undefined)}>
              Done
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
              <th title="Whether they sign in with a second factor">
                Two-factor
              </th>
              <th>Since</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {members.map((u) => (
              <MemberRow
                key={u.id}
                id={u.id}
                email={u.email}
                name={u.name}
                role={u.role}
                twoFactor={u.two_factor}
                required={required}
                since={u.created_at}
                you={me?.email === u.email}
              />
            ))}
            {invites.map((i) => (
              <InviteRow
                key={`invite-${i.id}`}
                id={i.id}
                email={i.email}
                role={i.role}
                expires={i.expires_at}
                expired={!!i.expired}
              />
            ))}
          </tbody>
        </table>
      </Panel>
      <p className="text-2xs text-faint">
        Removing someone takes them out of this organization; their account
        stays. Leave the organization from General.
      </p>
    </Page>
  );
}

function InviteForm({ onCreated }: { onCreated: (link: Link) => void }) {
  const create = useCreateInvite();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Scope>("deploy");
  return (
    <form
      aria-label="Invite someone"
      className="flex flex-wrap items-center gap-2.5"
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate(
          { email: email.trim(), role },
          {
            onSuccess: (c) => {
              setEmail("");
              onCreated({
                email: c.invite.email,
                url: `${window.location.origin}/invite/${c.code}`,
              });
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
        className="field min-w-0 flex-[1_1_260px] py-[7px]"
      />
      <RoleSelect value={role} onChange={setRole} label="Role" />
      <Act
        primary
        type="submit"
        cli={`dock org invite ${email.trim() || "<email>"} --role ${role}`}
        disabled={create.isPending || !email.includes("@")}
      >
        Invite
      </Act>
      <ErrorText error={create.error} />
    </form>
  );
}

function RoleSelect({
  value,
  onChange,
  label,
  disabled,
}: {
  value: Scope;
  onChange: (role: Scope) => void;
  label: string;
  disabled?: boolean;
}) {
  return (
    <select
      aria-label={label}
      value={value}
      disabled={disabled}
      onChange={(e) =>
        onChange(roles.find((r) => r === e.target.value) ?? value)
      }
      className="field py-[5px]"
    >
      {roles.map((r) => (
        <option key={r}>{r}</option>
      ))}
    </select>
  );
}

function MemberRow({
  id,
  email,
  name,
  role,
  twoFactor,
  required,
  since,
  you,
}: {
  id: number;
  email: string;
  name: string;
  role: Scope;
  /** They have a second factor (ADR 0050). */
  twoFactor: boolean;
  /** The org requires one: those without can't act here. */
  required: boolean;
  since: string;
  you: boolean;
}) {
  const setRole = useSetMemberRole();
  const remove = useRemoveMember();
  const [asking, setAsking] = useState(false);
  if (asking) {
    return (
      <tr>
        <td colSpan={5} className="py-2">
          <Confirm
            danger
            action={`Remove ${email}`}
            cli={`dock org member remove ${email}`}
            pending={remove.isPending}
            error={remove.error}
            onConfirm={() =>
              remove.mutate(id, { onSuccess: () => setAsking(false) })
            }
            onCancel={() => setAsking(false)}
          >
            Remove <b className="font-semibold">{email}</b> from the
            organization? <span className="text-mut">Their account stays.</span>
          </Confirm>
        </td>
      </tr>
    );
  }
  return (
    <tr>
      <td className="py-2">
        <span className="font-mono">{email}</span>
        {name && name !== email && (
          <span className="text-faint"> · {name}</span>
        )}
        {you && <span className="text-faint"> · you</span>}
      </td>
      <td className="py-1.5">
        <span className="act">
          <RoleSelect
            label={`Role of ${email}`}
            value={role}
            disabled={setRole.isPending}
            onChange={(r) => setRole.mutate({ id, role: r })}
          />
          <span className="cli">{`dock org member role ${email} <role>`}</span>
        </span>
        <ErrorText error={setRole.error} />
      </td>
      <td className="py-2">
        {twoFactor ? (
          <span className="text-ok">
            <Glyph char="●" /> on
          </span>
        ) : (
          <span
            className={required ? "text-warn" : "text-faint"}
            title={required ? "Can't act here until they add one" : undefined}
          >
            <Glyph char="○" /> off
          </span>
        )}
      </td>
      <td className="py-2 text-faint">{day(since)}</td>
      <td className="w-full py-1.5 text-right">
        {!you && (
          <Button small onClick={() => setAsking(true)}>
            Remove
          </Button>
        )}
      </td>
    </tr>
  );
}

function InviteRow({
  id,
  email,
  role,
  expires,
  expired,
}: {
  id: number;
  email: string;
  role: Scope;
  expires: string;
  expired: boolean;
}) {
  const revoke = useRevokeInvite();
  return (
    <tr>
      <td className="py-2">
        <span className="font-mono">{email}</span>{" "}
        <span className="rounded-full border border-warn-edge bg-stage-bg px-1.5 text-2xs text-warn-text">
          {expired ? "invite expired" : "invited"}
        </span>
      </td>
      <td className="py-2 font-mono text-mut">{role}</td>
      <td />
      <td className="py-2 text-faint">
        {expired ? "expired" : `until ${day(expires)}`}
      </td>
      <td className="w-full py-1.5 text-right">
        <Act
          small
          cli={`dock org invites revoke ${id}`}
          disabled={revoke.isPending}
          onClick={() => revoke.mutate(id)}
        >
          Revoke
        </Act>
        <ErrorText error={revoke.error} />
      </td>
    </tr>
  );
}
