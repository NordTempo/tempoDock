import { useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { components } from "../../api/schema";
import { useIsAdmin, useSession } from "../../data/queries";
import type { Scope, User } from "../../data/types";
import { CliHint } from "../../ui/CliHint";
import { day, plural } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  CopyButton,
  ErrorText,
  Page,
  PageBar,
  Panel,
} from "../../ui/kit";
import {
  orgQueries,
  roleNote,
  useCreateInvite,
  useOrgName,
  useRemoveMember,
  useRevokeInvite,
  useSetMemberRole,
} from "./data";

type Invite = components["schemas"]["Invite"];

const roles: Scope[] = ["read", "deploy", "admin"];

/**
 * The organization's members and open invites (`dock org members`,
 * `dock org invite`), with what each role may do beside them. Admins
 * only: the API refuses everyone else, so others are told whom to ask.
 */
export function MembersPage() {
  const org = useOrgName();
  if (!useIsAdmin()) {
    // Non-admins can't list members, so the admins can't be named.
    return (
      <>
        <PageBar parent={org} title="Members" />
        <Page>
          <p className="text-mut">
            Ask an admin of {org} to invite people or change roles.
          </p>
        </Page>
      </>
    );
  }
  return <Members org={org} />;
}

/** A created invite's link, shown once. */
type Link = { email: string; url: string };

function Members({ org }: { org: string }) {
  const members = useSuspenseQuery(orgQueries.members()).data;
  const invites = useSuspenseQuery(orgQueries.invites()).data;
  const me = useSession();
  const [inviting, setInviting] = useState(false);
  const [link, setLink] = useState<Link>();
  return (
    <>
      <PageBar
        parent={org}
        title="Members"
        after={
          <span className="text-xs text-faint">
            {plural(members.length, "person", "people")}
            {invites.length > 0 && ` · ${invites.length} invited`}
          </span>
        }
        actions={
          !inviting && (
            <Button primary onClick={() => setInviting(true)}>
              Invite people
            </Button>
          )
        }
      />
      <Page gap="gap-5">
        <div className="flex flex-wrap items-start gap-8">
          <div className="flex min-w-0 flex-[999_1_560px] flex-col gap-5">
            {inviting && (
              <InviteForm
                onCreated={(created) => {
                  setInviting(false);
                  setLink(created);
                }}
                onCancel={() => setInviting(false)}
              />
            )}
            {link && (
              <InviteLink link={link} onDone={() => setLink(undefined)} />
            )}
            <Panel>
              <table>
                <thead>
                  <tr>
                    <th>Person</th>
                    <th>Role</th>
                    <th>Access</th>
                    <th>Since</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {members.map((u) => (
                    <MemberRow
                      key={u.id}
                      member={u}
                      you={me?.email === u.email}
                    />
                  ))}
                  {invites.map((i) => (
                    <InviteRow key={`invite-${i.id}`} invite={i} />
                  ))}
                </tbody>
              </table>
            </Panel>
            <p className="text-2xs text-faint">
              Removing someone takes them out of this organization; their
              account stays. Leave the organization from General.
            </p>
          </div>
          <Roles />
        </div>
      </Page>
    </>
  );
}

/** What each role can do, beside the members table. */
function Roles() {
  return (
    <aside
      aria-labelledby="roles"
      className="flex max-w-[360px] min-w-0 flex-[1_1_280px] flex-col gap-3"
    >
      <h2 id="roles" className="font-semibold">
        What each role can do
      </h2>
      <dl className="panel">
        {roles.map((r) => (
          <div
            key={r}
            className="border-b border-soft px-3.5 py-3 last:border-b-0"
          >
            <dt className="font-medium">{r}</dt>
            <dd className="text-xs text-mut">{roleNote[r]}</dd>
          </div>
        ))}
      </dl>
    </aside>
  );
}

/** The link of an invite just created, with copy; it isn't shown again. */
function InviteLink({ link, onDone }: { link: Link; onDone: () => void }) {
  return (
    <section
      aria-labelledby="invite-link"
      className="flex flex-col gap-2.5 rounded-panel border border-ok-edge bg-ok-bg/30 p-4"
    >
      <h2 id="invite-link" className="font-semibold">
        <span className="text-ok">✓</span> Invite for{" "}
        <span className="font-mono font-medium">{link.email}</span>
      </h2>
      <p className="text-mut">
        Send them this link. It works once, for 7 days, and only signed in as{" "}
        {link.email}. It isn't shown again.
      </p>
      <div className="flex flex-wrap items-center gap-2.5 rounded-ctl border border-line bg-side px-3 py-2.5">
        <code className="min-w-0 flex-1 overflow-x-auto font-mono whitespace-nowrap">
          {link.url}
        </code>
        <CopyButton text={link.url} primary />
      </div>
      <div>
        <Button small onClick={onDone}>
          Done
        </Button>
      </div>
    </section>
  );
}

/** Invites someone by email with a role; opened from the bar's button. */
function InviteForm({
  onCreated,
  onCancel,
}: {
  onCreated: (link: Link) => void;
  onCancel: () => void;
}) {
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
            onSuccess: (c) =>
              onCreated({
                email: c.invite.email,
                url: `${window.location.origin}/invite/${c.code}`,
              }),
          },
        );
      }}
    >
      <input
        type="email"
        aria-label="Email"
        placeholder="name@company.com"
        // biome-ignore lint/a11y/noAutofocus: opened by "Invite people"
        autoFocus
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
      <Button onClick={onCancel}>Cancel</Button>
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

/** A person's initial in a circle; an invite gets an empty dashed one. */
function Avatar({ name }: { name?: string }) {
  if (!name) {
    return (
      <span
        aria-hidden
        className="size-6 flex-none rounded-full border border-dashed border-dash"
      />
    );
  }
  return (
    <span
      aria-hidden
      className="grid size-6 flex-none place-items-center rounded-full bg-edge text-2xs font-semibold text-fg"
    >
      {name.slice(0, 1).toUpperCase()}
    </span>
  );
}

/** A member's live sessions and usable tokens, "2 sessions · 1 token". */
const signedIn = (u: User) =>
  [
    u.sessions > 0 && plural(u.sessions, "session"),
    u.tokens > 0 && plural(u.tokens, "token"),
  ]
    .filter(Boolean)
    .join(" · ");

function MemberRow({ member, you }: { member: User; you: boolean }) {
  const { id, email, name, role } = member;
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
  const access = signedIn(member);
  return (
    <tr className="*:align-middle">
      <td className="py-2">
        <span className="flex items-center gap-2.5">
          <Avatar name={name || email} />
          <span>
            {email}
            {name && name !== email && (
              <span className="text-faint"> · {name}</span>
            )}
            {you && <span className="text-faint"> (you)</span>}
          </span>
        </span>
      </td>
      <td className="py-1.5">
        <CliHint cli={`dock org member role ${email} <role>`} className="act">
          <RoleSelect
            label={`Role of ${email}`}
            value={role}
            disabled={setRole.isPending}
            onChange={(r) => setRole.mutate({ id, role: r })}
          />
        </CliHint>
        <ErrorText error={setRole.error} />
      </td>
      <td className={`py-2 ${access ? "text-mut" : "text-faint"}`}>
        {access || "none"}
      </td>
      <td className="py-2 text-faint">{day(member.created_at)}</td>
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

function InviteRow({ invite }: { invite: Invite }) {
  const { id, email, role, expires_at } = invite;
  const revoke = useRevokeInvite();
  return (
    <tr className="*:align-middle">
      <td className="py-2">
        <span className="flex items-center gap-2.5">
          <Avatar />
          <span className="text-mut">{email}</span>
          <span className="text-2xs text-warn">
            {invite.expired ? "invite expired" : "invited"}
          </span>
        </span>
      </td>
      <td className="py-2 text-mut">{role}</td>
      <td />
      <td className="py-2 text-faint">
        {invite.expired ? "expired" : `until ${day(expires_at)}`}
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
