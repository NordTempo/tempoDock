import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Suspense, useState } from "react";
import type { components } from "../../api/schema";
import { useIsAdmin, useSession } from "../../data/queries";
import type { Scope, User } from "../../data/types";
import { useShortcuts } from "../../shell/keys";
import { useOrg } from "../../shell/location";
import { CliHint } from "../../ui/CliHint";
import { ago, day, plural } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  ErrorText,
  Page,
  Panel,
  StateText,
} from "../../ui/kit";
import { useGroups } from "../access/data";
import { PersonAccess } from "../access/PersonAccess";
import { ShadowNote } from "../access/ShadowNote";
import { Saved, useValidation } from "../create/form";
import {
  orgQueries,
  roleNote,
  useCreateInvite,
  useOrgName,
  useRemoveMember,
  useRenewInvite,
  useRevokeInvite,
  useSetMemberRole,
} from "./data";
import {
  InviteLink,
  type InviteLinkInfo,
  LocalLinkNote,
  linkInfo,
  useInviteBase,
} from "./inviteLink";
import { Avatar, PersonCell, TwoFactorState } from "./people";
import { OrgSettingsBar } from "./SettingsBar";

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
        <OrgSettingsBar />
        <Page>
          <p className="text-mut">
            Ask an admin of {org} to invite people or change roles.
          </p>
        </Page>
      </>
    );
  }
  return <Members />;
}

function Members() {
  const members = useSuspenseQuery(orgQueries.members()).data;
  const required = useSuspenseQuery(orgQueries.org()).data.require_two_factor;
  const without = members.filter((u) => !u.two_factor).length;
  const invites = useSuspenseQuery(orgQueries.invites()).data;
  const me = useSession();
  const slug = useOrg();
  const [inviting, setInviting] = useState(false);
  const [link, setLink] = useState<InviteLinkInfo>();
  const groups = useGroups();
  // The person whose grants and effective access show beside the table.
  const [person, setPerson] = useState<string>();
  const groupsOf = (id: number) =>
    groups
      .filter((g) => (g.members ?? []).some((m) => m.user_id === id))
      .map((g) => g.name);
  const invite = useInviteBase(inviting || !!link);
  useShortcuts([
    { key: "n", label: "invite people", run: () => setInviting(true) },
  ]);
  return (
    <>
      <OrgSettingsBar
        actions={
          !inviting && (
            <Button primary onClick={() => setInviting(true)}>
              Invite people <kbd>N</kbd>
            </Button>
          )
        }
      />
      <Page>
        {/* The roles sit beside the table when the page itself is wide
            (inside the settings column, the window's width says little),
            under it otherwise, so the table's actions never clip. */}
        <div className="@container">
          <div className="grid items-start gap-8 @min-[1080px]:grid-cols-[minmax(0,1fr)_320px]">
            <div className="flex min-w-0 flex-col gap-5">
              <ShadowNote />
              {without > 0 && (
                <p className="text-warn-text">
                  {plural(without, "person", "people")} without two-factor
                  sign-in.{" "}
                  <Link
                    to="/$org/settings/general"
                    params={{ org: slug }}
                    className="text-warn underline"
                  >
                    {required
                      ? "They can't act here until they add one"
                      : "Require it in General"}
                  </Link>
                </p>
              )}
              {inviting && invite.local && (
                <LocalLinkNote operator={invite.operator} />
              )}
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
                <InviteLink
                  link={link}
                  live={invites.find((i) => i.id === link.id)?.mail}
                  url={`${invite.base}/invite/${link.code}`}
                  note={
                    invite.local && <LocalLinkNote operator={invite.operator} />
                  }
                  onDone={() => setLink(undefined)}
                />
              )}
              <Panel>
                <table>
                  <thead>
                    <tr>
                      <th>Person</th>
                      <th>Role</th>
                      <th>Groups</th>
                      <th title="Whether they sign in with a second factor">
                        Two-factor
                      </th>
                      <th>Access</th>
                      <th>Added</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {members.map((u) => (
                      <MemberRow
                        key={u.id}
                        member={u}
                        required={required}
                        you={me?.email === u.email}
                        groups={groupsOf(u.id)}
                        selected={person === u.email}
                        onSelect={() =>
                          setPerson(person === u.email ? undefined : u.email)
                        }
                      />
                    ))}
                    {invites.map((i) => (
                      <InviteRow
                        key={`invite-${i.id}`}
                        invite={i}
                        onLink={setLink}
                      />
                    ))}
                  </tbody>
                </table>
              </Panel>
              <p className="text-xs text-mut">
                Removing someone takes them out of this organization; their
                account stays. Leave the organization from General.
              </p>
            </div>
            {person ? (
              <Suspense fallback={<p className="text-faint">Loading.</p>}>
                <PersonAccess
                  email={person}
                  onClose={() => setPerson(undefined)}
                />
              </Suspense>
            ) : (
              <Roles />
            )}
          </div>
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
      className="flex max-w-[640px] min-w-0 flex-col gap-3"
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

/** Invites someone by email with a role; opened from the bar's button. */
function InviteForm({
  onCreated,
  onCancel,
}: {
  onCreated: (link: InviteLinkInfo) => void;
  onCancel: () => void;
}) {
  const create = useCreateInvite();
  const [email, setEmail] = useState("");
  // The least privilege: an admin raises it on purpose.
  const [role, setRole] = useState<Scope>("read");
  const v = useValidation();
  const typed = email.trim();
  const emailError =
    v.required(!typed, "Enter their email address.") ??
    v.required(
      !!typed && !/^[^\s@]+@[^\s@]+$/.test(typed),
      "Enter an email address, like name@company.com.",
    );
  return (
    <form
      ref={v.form}
      noValidate
      aria-label="Invite someone"
      className="flex flex-wrap items-end gap-2.5"
      onSubmit={(e) => {
        e.preventDefault();
        if (!v.check(/^[^\s@]+@[^\s@]+$/.test(typed))) return;
        create.mutate(
          { email: email.trim(), role },
          {
            onSuccess: (c) => onCreated(linkInfo(c)),
          },
        );
      }}
    >
      <label className="flex min-w-0 flex-[1_1_260px] flex-col gap-1">
        <span className="text-xs text-sec">Email</span>
        <input
          type="email"
          placeholder="name@company.com"
          // biome-ignore lint/a11y/noAutofocus: opened by "Invite people"
          autoFocus
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          aria-invalid={!!emailError}
          aria-describedby={emailError ? "invite-email-error" : undefined}
          className={`field ${emailError ? "border-fail" : ""}`}
        />
      </label>
      <span className="flex flex-col gap-1">
        <label htmlFor="invite-role" className="text-xs text-sec">
          Role
        </label>
        <RoleSelect id="invite-role" value={role} onChange={setRole} />
      </span>
      <Act
        primary
        type="submit"
        cli={`dock org invite ${email.trim() || "<email>"} --role ${role}`}
        disabled={create.isPending}
      >
        Create invite link
      </Act>
      <Button onClick={onCancel}>Cancel</Button>
      {emailError && (
        <span
          id="invite-email-error"
          role="alert"
          className="basis-full text-xs text-fail"
        >
          {emailError}
        </span>
      )}
      <ErrorText error={create.error} />
      {/* Before they press it, not after: no email goes out. */}
      <p className="basis-full text-xs text-mut">
        Dock doesn't send email. You get a link to copy and send them; it works
        once, for this address.
      </p>
    </form>
  );
}

/** The role picker; `label` names it where no visible label does. */
function RoleSelect({
  id,
  value,
  onChange,
  label,
  disabled,
}: {
  id?: string;
  value: Scope;
  onChange: (role: Scope) => void;
  label?: string;
  disabled?: boolean;
}) {
  return (
    <select
      id={id}
      aria-label={label}
      value={value}
      disabled={disabled}
      onChange={(e) =>
        onChange(roles.find((r) => r === e.target.value) ?? value)
      }
    >
      {roles.map((r) => (
        <option key={r}>{r}</option>
      ))}
    </select>
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

function MemberRow({
  member,
  required,
  you,
  groups,
  selected,
  onSelect,
}: {
  member: User;
  /** The org requires a second factor (ADR 0057): those without can't act here. */
  required: boolean;
  you: boolean;
  /** The groups they are in (ADR 0080). */
  groups: string[];
  selected: boolean;
  /** Shows their grants and effective access beside the table. */
  onSelect: () => void;
}) {
  const { id, email, name, role } = member;
  const setRole = useSetMemberRole();
  const remove = useRemoveMember();
  const [asking, setAsking] = useState(false);
  const access = signedIn(member);
  return (
    <tr className="*:align-middle">
      <td className="py-2">
        <PersonCell name={name} email={email} you={you} />
      </td>
      <td className="py-1.5">
        {you ? (
          // Your own role changes through another admin.
          <span className="px-[9px]">{role}</span>
        ) : (
          <>
            <CliHint
              cli={`dock org member role ${email} <role>`}
              className="act"
            >
              <RoleSelect
                label={`Role of ${email}`}
                value={role}
                disabled={setRole.isPending}
                onChange={(r) => setRole.mutate({ id, role: r })}
              />
            </CliHint>
            {setRole.isSuccess && (
              <div>
                <Saved ok />
              </div>
            )}
            <ErrorText error={setRole.error} />
          </>
        )}
      </td>
      <td className="py-2 whitespace-normal">
        <button
          type="button"
          className={selected ? "text-fg underline" : "text-sec hover:text-fg"}
          title="Their grants and what they can do"
          onClick={onSelect}
        >
          {groups.length ? groups.join(", ") : "no group"}
        </button>
      </td>
      <td className="py-2">
        <TwoFactorState
          on={member.two_factor}
          required={required}
          note={required ? "Can't act here until they add one" : undefined}
        />
      </td>
      <td
        className={`min-w-[120px] py-2 whitespace-normal ${access ? "text-mut" : "text-faint"}`}
      >
        {access || "none"}
      </td>
      <td className="py-2 text-faint">{day(member.created_at)}</td>
      <td className="w-full py-1.5 text-right">
        {!you && (
          <Button danger small onClick={() => setAsking(true)}>
            Remove
          </Button>
        )}
        {/* The modal inherits the cell's alignment and nowrap. */}
        <div className="contents text-left whitespace-normal">
          {asking && (
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
              They lose access to this organization's projects right away
              {access ? `, and their ${access} here stop working` : ""}. Their
              account stays, and an admin can invite them again.
            </Confirm>
          )}
        </div>
      </td>
    </tr>
  );
}

/** How an invite's latest mail went, after its state. */
function MailNote({ mail }: { mail: Invite["mail"] }) {
  if (!mail) return null;
  if (mail.status === "failed")
    return (
      <span className="text-xs text-fail" title={mail.error}>
        {" "}
        · email failed
      </span>
    );
  return (
    <span className="text-xs text-faint">
      {" "}
      · {mail.status === "sent" ? "emailed" : "emailing"}
      {mail.at && ` ${ago(mail.at)}`}
    </span>
  );
}

/**
 * An open invite. New link revokes it and invites them again, for a link
 * that was lost (it is shown only once); `onLink` shows the new one.
 */
function InviteRow({
  invite,
  onLink,
}: {
  invite: Invite;
  onLink: (link: InviteLinkInfo) => void;
}) {
  const { id, email, role, expires_at } = invite;
  const revoke = useRevokeInvite();
  const [revoking, setRevoking] = useState(false);
  const renew = useRenewInvite();
  const [renewing, setRenewing] = useState(false);
  return (
    <tr className="*:align-middle">
      <td className="py-2">
        <span className="flex items-center gap-2.5">
          <Avatar />
          <span className="text-mut">
            {email}
            <span className="mt-0.5 block">
              <StateText
                state={
                  invite.expired
                    ? { tone: "mut", label: "invite expired" }
                    : { tone: "warn", label: "invited" }
                }
              />
              <MailNote mail={invite.mail} />
            </span>
          </span>
        </span>
      </td>
      <td className="py-2 text-mut">{role}</td>
      <td />
      <td />
      <td className="py-2 text-faint">
        {invite.expired ? "expired" : `until ${day(expires_at)}`}
      </td>
      <td className="w-full py-1.5 text-right">
        <span className="inline-flex gap-1.5">
          <Button small onClick={() => setRenewing(true)}>
            New link
          </Button>
          <Act
            danger
            small
            cli={`dock org invites revoke ${id}`}
            disabled={revoke.isPending}
            onClick={() => setRevoking(true)}
          >
            Revoke
          </Act>
        </span>
        <div className="contents text-left whitespace-normal">
          {revoking && (
            <Confirm
              danger
              action="Revoke invite"
              title={`Revoke the invite for ${email}?`}
              cli={`dock org invites revoke ${id}`}
              pending={revoke.isPending}
              error={revoke.error}
              onConfirm={() =>
                revoke.mutate(id, { onSuccess: () => setRevoking(false) })
              }
              onCancel={() => setRevoking(false)}
            >
              Their link stops working. You can invite them again.
            </Confirm>
          )}
          {renewing && (
            <Confirm
              action="Make a new link"
              title={`New invite link for ${email}?`}
              cli={`dock org invite ${email} --role ${role} && dock org invites revoke ${id}`}
              pending={renew.isPending}
              error={renew.error}
              onConfirm={() =>
                renew.mutate(
                  { id, email, role },
                  {
                    onSuccess: (c) => {
                      setRenewing(false);
                      onLink(linkInfo(c));
                    },
                  },
                )
              }
              onCancel={() => setRenewing(false)}
            >
              The link sent earlier stops working. The new one is shown once,
              for 7 days, with the same role ({role}).
            </Confirm>
          )}
        </div>
      </td>
    </tr>
  );
}
