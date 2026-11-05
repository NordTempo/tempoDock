import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import type { components } from "../../api/schema";
import { useIsAdmin, useIsOperator, useSession } from "../../data/queries";
import type { Scope, User } from "../../data/types";
import { useOrg } from "../../shell/location";
import { CliHint } from "../../ui/CliHint";
import { day, plural } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  CopyButton,
  ErrorText,
  Glyph,
  Page,
  PageBar,
  Panel,
} from "../../ui/kit";
import { addressQuery } from "../address/data";
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

/** A created invite: whom for, and the code its link carries. */
type InviteLinkInfo = { email: string; code: string };

/** Hostnames only this machine (or its private network) can open. */
const privateHost =
  /^(localhost|127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.|\[?::1\]?$)/;

/**
 * Where invite links point: Dock's public address when one is set, else
 * the address this page was opened at. `local` when that is an address
 * others likely can't open (no public address, or a private one).
 * Operators read the public address (ADR 0029, only they may); for
 * everyone else the page's own address decides. Loaded once inviting.
 */
function useInviteBase(enabled: boolean) {
  const operator = useIsOperator();
  const address = useQuery({
    ...addressQuery(),
    enabled: enabled && operator,
  }).data;
  const base = address?.url ?? window.location.origin;
  const local = operator
    ? !!address && !address.url
    : privateHost.test(window.location.hostname);
  return { base, local, operator };
}

/**
 * The warning beside an invite when its link won't open for others, with
 * the way to fix it: the console's Address page, or whom to ask.
 */
function LocalLinkNote({ operator }: { operator: boolean }) {
  return (
    <p className="text-warn-text">
      <span className="text-warn">▲</span> This link only opens on this machine
      or its network: Dock has no public address.{" "}
      {operator ? (
        <Link to="/console/address" className="text-warn-text underline">
          Set a public address
        </Link>
      ) : (
        "Ask an operator to set a public address."
      )}
    </p>
  );
}

function Members({ org }: { org: string }) {
  const members = useSuspenseQuery(orgQueries.members()).data;
  const required = useSuspenseQuery(orgQueries.org()).data.require_two_factor;
  const without = members.filter((u) => !u.two_factor).length;
  const invites = useSuspenseQuery(orgQueries.invites()).data;
  const me = useSession();
  const slug = useOrg();
  const [inviting, setInviting] = useState(false);
  const [link, setLink] = useState<InviteLinkInfo>();
  const invite = useInviteBase(inviting || !!link);
  return (
    <>
      <PageBar
        parent={org}
        title="Members"
        after={
          <span className="text-xs text-faint">
            {plural(members.length, "person", "people")}
            {invites.length > 0 && ` · ${invites.length} invited`}
            {without > 0 && (
              <>
                {" · "}
                <Link
                  to="/$org/settings/general"
                  params={{ org: slug }}
                  className="text-warn underline decoration-warn/50 underline-offset-[3px] hover:decoration-warn"
                  title={
                    required
                      ? "They can't act here until they add one"
                      : "Require two-factor sign-in in General"
                  }
                >
                  {without} without two-factor
                </Link>
              </>
            )}
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
                    <th title="Whether they sign in with a second factor">
                      Two-factor
                    </th>
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
                      required={required}
                      you={me?.email === u.email}
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
function InviteLink({
  link,
  url,
  note,
  onDone,
}: {
  link: InviteLinkInfo;
  url: string;
  /** Why the link may not open for them, when it may not. */
  note: ReactNode;
  onDone: () => void;
}) {
  return (
    <section
      aria-labelledby="invite-link"
      className="flex flex-col gap-2.5 rounded-panel border border-ok-edge bg-ok-bg/30 p-4"
    >
      <h2 id="invite-link" className="font-semibold">
        <span className="text-ok">✓</span> Invite for{" "}
        <span className="font-mono font-medium">{link.email}</span>
      </h2>
      {note}
      <p className="text-mut">
        Send them this link. It works once, for 7 days, and only signed in as{" "}
        {link.email}. It isn't shown again; New link on their row makes another.
      </p>
      <div className="flex flex-wrap items-center gap-2.5 rounded-ctl border border-line bg-side px-3 py-2.5">
        <code className="min-w-0 flex-1 font-mono break-all">{url}</code>
        <CopyButton text={url} primary />
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
  onCreated: (link: InviteLinkInfo) => void;
  onCancel: () => void;
}) {
  const create = useCreateInvite();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Scope>("deploy");
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
      className="flex flex-wrap items-center gap-2.5"
      onSubmit={(e) => {
        e.preventDefault();
        if (!v.check(/^[^\s@]+@[^\s@]+$/.test(typed))) return;
        create.mutate(
          { email: email.trim(), role },
          {
            onSuccess: (c) =>
              onCreated({
                email: c.invite.email,
                code: c.code,
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
        aria-invalid={!!emailError}
        aria-describedby={emailError ? "invite-email-error" : undefined}
        className={`field min-w-0 flex-[1_1_260px] py-[7px] ${emailError ? "border-fail" : ""}`}
      />
      <RoleSelect value={role} onChange={setRole} label="Role" />
      <Act
        primary
        type="submit"
        cli={`dock org invite ${email.trim() || "<email>"} --role ${role}`}
        disabled={create.isPending}
      >
        Invite
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

function MemberRow({
  member,
  required,
  you,
}: {
  member: User;
  /** The org requires a second factor (ADR 0057): those without can't act here. */
  required: boolean;
  you: boolean;
}) {
  const { id, email, name, role } = member;
  const setRole = useSetMemberRole();
  const remove = useRemoveMember();
  const [asking, setAsking] = useState(false);
  const access = signedIn(member);
  return (
    <tr className="*:align-middle">
      <td className="py-2">
        <span className="flex items-center gap-2.5">
          <Avatar name={name || email} />
          <span>
            {email}
            {you && <span className="text-faint"> (you)</span>}
            {name && name !== email && (
              <span className="block text-2xs text-faint">{name}</span>
            )}
          </span>
        </span>
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
      <td className="py-2">
        {member.two_factor ? (
          <span className="text-ok">
            <Glyph char="●" /> on
          </span>
        ) : (
          <span
            className="text-warn"
            title={required ? "Can't act here until they add one" : undefined}
          >
            off
          </span>
        )}
      </td>
      <td
        className={`min-w-[120px] py-2 whitespace-normal ${access ? "text-mut" : "text-faint"}`}
      >
        {access || "none"}
      </td>
      <td className="py-2 text-faint">{day(member.created_at)}</td>
      <td className="w-full py-1.5 text-right">
        {!you && (
          <Button small onClick={() => setAsking(true)}>
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
  const renew = useRenewInvite();
  const [renewing, setRenewing] = useState(false);
  return (
    <tr className="*:align-middle">
      <td className="py-2">
        <span className="flex items-center gap-2.5">
          <Avatar />
          <span className="text-mut">
            {email}
            <span className="block text-2xs text-warn">
              {invite.expired ? "invite expired" : "invited"}
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
            small
            cli={`dock org invites revoke ${id}`}
            disabled={revoke.isPending}
            onClick={() => revoke.mutate(id)}
          >
            Revoke
          </Act>
        </span>
        <ErrorText error={revoke.error} />
        <div className="contents text-left whitespace-normal">
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
                      onLink({ email: c.invite.email, code: c.code });
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
