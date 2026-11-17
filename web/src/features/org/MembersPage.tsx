import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Suspense, useState } from "react";
import type { components } from "../../api/schema";
import { useIsAdmin, useSession } from "../../data/queries";
import type { Scope, User } from "../../data/types";
import { useShortcuts } from "../../shell/keys";
import { useOrg } from "../../shell/location";
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
import { useGroupMember, useGroups } from "../access/data";
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
  // Under enforce groups decide; a role is only their summary (ADR 0080).
  const enforce = me?.access_model === "enforce";
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
            under it otherwise, so the table's last columns never clip. */}
        <div className="@container">
          <div className="grid items-start gap-8 @min-[1240px]:grid-cols-[minmax(0,1fr)_320px]">
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
                      {!enforce && <th>Role</th>}
                      <th>Groups</th>
                      <th title="Whether they sign in with a second factor">
                        Two-factor
                      </th>
                      {/* Access and Added are left out on a narrow page, so
                          the row's actions stay in view. */}
                      <th className="@max-[900px]:hidden">Access</th>
                      <th className="@max-[900px]:hidden">Added</th>
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
                        allGroups={groups.map((g) => g.name)}
                        enforce={enforce}
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
                        enforce={enforce}
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
              {/* One command a line, so where one ends is plain. */}
              <p className="flex flex-col gap-0.5 font-mono text-2xs text-mut">
                {(enforce
                  ? [
                      "dock group add|remove <group> <email>",
                      "dock can <email>",
                      "dock org member remove <email>",
                    ]
                  : [
                      "dock org member role <email> read|deploy|admin",
                      "dock group add|remove <group> <email>",
                      "dock can <email>",
                    ]
                ).map((c) => (
                  <span key={c} className="whitespace-nowrap">
                    {c}
                  </span>
                ))}
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
              // Roles only decide until the model enforces (ADR 0080).
              !enforce && <Roles />
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

/**
 * Invites someone by email with a role; opened from the bar's button. No
 * role is picked for them: the admin chooses one, each with what it can do
 * beside it, so nobody gets the wrong access by default.
 */
function InviteForm({
  onCreated,
  onCancel,
}: {
  onCreated: (link: InviteLinkInfo) => void;
  onCancel: () => void;
}) {
  const create = useCreateInvite();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Scope>();
  const v = useValidation();
  const typed = email.trim();
  const validEmail = /^[^\s@]+@[^\s@]+$/.test(typed);
  const emailError =
    v.required(!typed, "Enter their email address.") ??
    v.required(
      !!typed && !validEmail,
      "Enter an email address, like name@company.com.",
    );
  const roleError = v.required(
    !role,
    "Pick a role: it decides what they can do here.",
  );
  return (
    <form
      ref={v.form}
      noValidate
      aria-label="Invite someone"
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        if (!v.check(validEmail && !!role) || !role) return;
        create.mutate(
          { email: typed, role },
          {
            onSuccess: (c) => onCreated(linkInfo(c)),
          },
        );
      }}
    >
      <label className="flex max-w-[420px] flex-col gap-1">
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
        {emailError && (
          <span
            id="invite-email-error"
            role="alert"
            className="text-xs text-fail"
          >
            {emailError}
          </span>
        )}
      </label>
      <fieldset
        className="flex flex-col"
        aria-describedby={roleError ? "invite-role-error" : undefined}
      >
        <legend className="mb-1 text-xs text-sec">Role</legend>
        {roles.map((r) => (
          <label key={r} className="flex min-h-[24px] items-center gap-2">
            <input
              type="radio"
              name="invite-role"
              className="accent-fg"
              value={r}
              checked={role === r}
              onChange={() => setRole(r)}
              aria-invalid={!!roleError}
            />
            <span className="w-16 font-medium">{r}</span>
            <span className="text-xs text-mut">{roleNote[r]}</span>
          </label>
        ))}
        {roleError && (
          <span
            id="invite-role-error"
            role="alert"
            className="text-xs text-fail"
          >
            {roleError}
          </span>
        )}
      </fieldset>
      <div className="flex flex-wrap items-center gap-2.5">
        <Act
          primary
          type="submit"
          cli={`dock org invite ${typed || "<email>"} --role ${role ?? "<role>"}`}
          disabled={create.isPending}
        >
          Create invite link
        </Act>
        <Button onClick={onCancel}>Cancel</Button>
      </div>
      <ErrorText error={create.error} />
      {/* Before they press it, not after: whether a mail goes out depends
          on the platform's email (ADR 0079), which members can't see. */}
      <p className="text-xs text-mut">
        You get a link to copy, also emailed to them when this Dock sends email.
        It works once, for this address.
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

/** A member's live sessions and usable tokens, "2 sessions, 1 token". */
const signedIn = (u: User) =>
  [
    u.sessions > 0 && plural(u.sessions, "session"),
    u.tokens > 0 && plural(u.tokens, "token"),
  ]
    .filter(Boolean)
    .join(", ");

function MemberRow({
  member,
  required,
  you,
  groups,
  allGroups,
  enforce,
  selected,
  onSelect,
}: {
  member: User;
  /** The org requires a second factor (ADR 0057): those without can't act here. */
  required: boolean;
  you: boolean;
  /** The groups they are in, and the org's (ADR 0080). */
  groups: string[];
  allGroups: string[];
  /** Groups decide: no role to set, only groups (ADR 0080). */
  enforce: boolean;
  selected: boolean;
  /** Shows their grants and effective access under the table. */
  onSelect: () => void;
}) {
  const { id, email, name, role } = member;
  const setRole = useSetMemberRole();
  const membership = useGroupMember();
  const others = allGroups.filter((g) => !groups.includes(g));
  const remove = useRemoveMember();
  const [asking, setAsking] = useState(false);
  const access = signedIn(member);
  return (
    <tr className="*:align-middle">
      <td className="py-2">
        <button
          type="button"
          className={`text-left ${selected ? "underline" : ""}`}
          aria-expanded={selected}
          title="Grants and what they can do"
          onClick={onSelect}
        >
          <PersonCell name={name} email={email} you={you} />
        </button>
      </td>
      {!enforce && (
        <td className="py-1.5">
          {you ? (
            // Your own role changes through another admin.
            <span className="px-[9px]">{role}</span>
          ) : (
            <>
              {/* Its CLI line is under the table: a hint here would cover
                  the next row. */}
              <RoleSelect
                label={`Role of ${email}`}
                value={role}
                disabled={setRole.isPending}
                onChange={(r) => setRole.mutate({ id, role: r })}
              />
              {setRole.isSuccess && (
                <div>
                  <Saved ok />
                </div>
              )}
              <ErrorText error={setRole.error} />
            </>
          )}
        </td>
      )}
      <td className="min-w-[150px] py-2 @min-[1000px]:min-w-[240px]">
        <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
          {groups.map((g) => (
            <span key={g} className="whitespace-nowrap">
              {g}
              <button
                type="button"
                className="-my-1 inline-grid min-h-[24px] min-w-[24px] place-items-center text-mut hover:text-fg"
                aria-label={`Take ${email} out of ${g}`}
                disabled={membership.isPending}
                onClick={() =>
                  membership.mutate({ group: g, member: email, add: false })
                }
              >
                ×
              </button>
            </span>
          ))}
          {others.length > 0 && (
            <select
              aria-label={`Add ${email} to a group`}
              className="cursor-pointer appearance-none border-0 bg-transparent bg-none p-0 text-mut hover:text-fg"
              value=""
              disabled={membership.isPending}
              onChange={(e) =>
                e.target.value &&
                membership.mutate({
                  group: e.target.value,
                  member: email,
                  add: true,
                })
              }
            >
              <option value="">+ group</option>
              {others.map((g) => (
                <option key={g} value={g}>
                  {g}
                </option>
              ))}
            </select>
          )}
        </span>
        <ErrorText error={membership.error} />
      </td>
      <td className="py-2">
        <TwoFactorState
          on={member.two_factor}
          required={required}
          note={required ? "Can't act here until they add one" : undefined}
        />
      </td>
      <td
        className={`py-2 @max-[900px]:hidden ${access ? "text-mut" : "text-faint"}`}
      >
        {access || "none"}
      </td>
      <td className="py-2 text-faint @max-[900px]:hidden">
        {day(member.created_at)}
      </td>
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
  enforce,
  onLink,
}: {
  invite: Invite;
  /** Groups decide: the role shows as what they join with (ADR 0080). */
  enforce: boolean;
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
      {enforce ? (
        <td className="py-2 text-mut">invited as {role}</td>
      ) : (
        <>
          <td className="py-2 text-mut">{role}</td>
          <td />
        </>
      )}
      <td />
      <td className="py-2 whitespace-nowrap text-faint @max-[900px]:hidden">
        {invite.expired ? "expired" : `until ${day(expires_at)}`}
      </td>
      <td className="@max-[900px]:hidden" />
      <td className="w-full py-1.5 text-right">
        <span className="inline-flex flex-wrap justify-end gap-1.5">
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
