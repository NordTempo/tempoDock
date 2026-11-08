import { Link, useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { currentOrg, setOrg } from "../../api/client";
import { useSession } from "../../data/queries";
import { Dialog } from "../../ui/Dialog";
import { day, plural, size } from "../../ui/format";
import {
  Button,
  Confirm,
  DialogFoot,
  ErrorText,
  Page,
  PageBar,
  StateText,
} from "../../ui/kit";
import {
  autofocus,
  Field,
  Fields,
  Footer,
  input,
  invalid,
  nameProblem,
  toName,
  useValidation,
} from "../create/form";
import {
  InviteLink,
  type InviteLinkInfo,
  LocalLinkNote,
  useInviteBase,
} from "../org/inviteLink";
import {
  type ConsoleOrg,
  type Role,
  useAddConsoleMember,
  useConsoleInvites,
  useConsoleMembers,
  useConsoleOrgs,
  useCreateConsoleInvite,
  useCreateConsoleOrg,
  useDeleteConsoleOrg,
  useJoinConsoleOrg,
  useRemoveConsoleMember,
  useRenameConsoleOrg,
  useRevokeConsoleInvite,
  useSetConsoleMemberRole,
  useStopConsoleOrg,
  useSuspendConsoleOrg,
} from "./data";

// The console's Organizations page (ADR 0062, mock B): every org, biggest
// first, with the numbers that rank them, and the selected one's people,
// use and the operator's actions in a column beside it (under it below
// 1400px). Orgs and use only: an org's alerts and errors are its admins'.
// The operator may create an org with nobody in it and invite its people.

/** Cores from a CPU percent of one core: "1.84". */
const cores = (percent: number) => (percent / 100).toFixed(2);

/** CPU growth over a week as "+62%", when there is a week to compare. */
function growth(o: ConsoleOrg): number | null {
  const { cpu_percent: now, cpu_week_ago_percent: then } = o.usage;
  return then >= 5 ? Math.round((100 * (now - then)) / then) : null;
}

/** How fast use grows before it reads as worth a look. */
const FAST = 50;

const statusState = {
  active: { tone: "ok", label: "active" },
  suspended: { tone: "mut", label: "suspended" },
  no_admin: { tone: "warn", label: "no admin" },
} as const;

export function OrgsPage({ selected }: { selected?: string }) {
  const orgs = useConsoleOrgs(true);
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  // The org just created opens on its people, to invite them.
  const [created, setCreated] = useState<string>();
  const current = orgs.find((o) => o.slug === selected) ?? orgs[0];
  const maxCPU = Math.max(1, ...orgs.map((o) => o.usage.cpu_percent));
  const maxMem = Math.max(1, ...orgs.map((o) => o.usage.memory_bytes));
  const totals = orgs.reduce(
    (t, o) => ({
      services: t.services + o.services,
      cpu: t.cpu + o.usage.cpu_percent,
      memory: t.memory + o.usage.memory_bytes,
    }),
    { services: 0, cpu: 0, memory: 0 },
  );
  const pick = (slug: string) =>
    navigate({ to: "/console/orgs", search: { org: slug }, replace: true });
  return (
    <>
      <PageBar
        title="Organizations"
        after={
          <span className="text-sm text-mut">
            {orgs.length} · {plural(totals.services, "service")} ·{" "}
            {cores(totals.cpu)} CPU · {size(totals.memory)}
          </span>
        }
        actions={
          <Button primary onClick={() => setCreating(true)}>
            New organization
          </Button>
        }
      />
      {creating && (
        <CreateOrg
          taken={orgs.map((o) => o.slug)}
          onCreated={(slug) => {
            setCreating(false);
            setCreated(slug);
            pick(slug);
          }}
          onClose={() => setCreating(false)}
        />
      )}
      <Page>
        <div className="grid gap-x-8 gap-y-6 min-[1400px]:grid-cols-[minmax(0,1fr)_360px]">
          <div className="min-w-0 overflow-x-auto border-t border-line">
            <table>
              <thead>
                <tr>
                  <th>Organization</th>
                  <th className="text-right">Services</th>
                  <th className="text-right">CPU</th>
                  <th className="text-right">Memory</th>
                  <th className="text-right">Volumes</th>
                  <th className="text-right">Bandwidth 30d</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {orgs.map((o) => {
                  const up = growth(o);
                  return (
                    <tr
                      key={o.slug}
                      data-selected={o.slug === current?.slug}
                      onClick={() => pick(o.slug)}
                      className="cursor-pointer hover:bg-hover data-[selected=true]:bg-sel"
                    >
                      <td className="py-2">
                        <Link
                          to="/console/orgs"
                          search={{ org: o.slug }}
                          replace
                          className="font-semibold text-fg no-underline"
                        >
                          {o.slug}
                        </Link>{" "}
                        {o.status !== "active" && (
                          <StateText state={statusState[o.status]} />
                        )}
                        <span className="block text-xs text-mut">
                          {(o.admins ?? []).join(", ") || "no admin"} ·{" "}
                          {plural(o.members, "member")}
                        </span>
                      </td>
                      <td className="py-2 text-right tabular-nums">
                        {o.services}
                      </td>
                      <td className="py-2 text-right tabular-nums">
                        <Bar
                          share={o.usage.cpu_percent / maxCPU}
                          hot={up !== null && up >= FAST}
                        />
                        {cores(o.usage.cpu_percent)}
                        {up !== null && up >= FAST && (
                          <span className="ml-1 text-xs text-warn">+{up}%</span>
                        )}
                      </td>
                      <td className="py-2 text-right tabular-nums">
                        <Bar share={o.usage.memory_bytes / maxMem} />
                        {size(o.usage.memory_bytes)}
                      </td>
                      <td className="py-2 text-right tabular-nums">
                        {o.usage.volume_read_at
                          ? size(o.usage.volume_bytes)
                          : "·"}
                      </td>
                      <td className="py-2 text-right tabular-nums">
                        {size(o.usage.bandwidth_bytes)}
                      </td>
                      <td className="w-6 py-2 text-faint" aria-hidden>
                        ›
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          {current && (
            <Detail
              key={current.slug}
              org={current}
              initial={current.slug === created ? "members" : undefined}
            />
          )}
        </div>
      </Page>
    </>
  );
}

/** A thin bar for a share of the biggest org's use. */
function Bar({ share, hot }: { share: number; hot?: boolean }) {
  return (
    <span className="mr-2 inline-block h-1 w-14 overflow-hidden rounded-full bg-chip align-middle">
      <span
        className={`block h-full rounded-full ${hot ? "bg-warn" : "bg-sec"}`}
        style={{ width: `${Math.max(2, Math.min(100, 100 * share))}%` }}
      />
    </span>
  );
}

type Opened = "rename" | "members" | "join" | "stop" | "suspend" | "delete";

/** The selected org: who runs it, what it uses, and the operator's actions. */
function Detail({ org: o, initial }: { org: ConsoleOrg; initial?: Opened }) {
  const [open, setOpen] = useState<Opened | null>(initial ?? null);
  const up = growth(o);
  const close = () => setOpen(null);
  const u = o.usage;
  return (
    <aside
      aria-label={`Organization ${o.slug}`}
      className="flex flex-col gap-4"
    >
      <div>
        <h2 className="flex items-center gap-2 text-[16px] font-semibold">
          {o.slug} <StateText state={statusState[o.status]} />
        </h2>
        <p className="text-xs text-mut">
          {o.name} · created {day(o.created_at)}
          {o.owner && <> by {o.owner}</>}
          {o.suspended_at && (
            <>
              {" "}
              · suspended {day(o.suspended_at)} by {o.suspended_by}
            </>
          )}
        </p>
      </div>
      {/* Side by side while the column sits under the table (below
          1400px), one column beside it. */}
      <div className="grid items-start gap-x-10 gap-y-4 sm:max-[1400px]:grid-cols-[minmax(0,1fr)_320px]">
        <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-[13px] [&_dt]:text-mut">
          <dt>Admins</dt>
          <dd>
            {(o.admins ?? []).join(", ") || (
              <span className="text-warn">none</span>
            )}
          </dd>
          <dt>Members</dt>
          <dd>{o.members}</dd>
          <dt>Size</dt>
          <dd>
            {plural(o.projects, "project")}, {plural(o.services, "service")}
          </dd>
          <dt>CPU</dt>
          <dd>
            {cores(u.cpu_percent)}
            {up !== null && (
              <span className={up >= FAST ? "text-warn" : "text-mut"}>
                {" "}
                {up >= 0 ? "+" : ""}
                {up}% in 7 days
              </span>
            )}
          </dd>
          <dt>Memory</dt>
          <dd>{size(u.memory_bytes)}</dd>
          <dt>Volumes</dt>
          <dd>{u.volume_read_at ? size(u.volume_bytes) : "not read yet"}</dd>
          <dt>Backups</dt>
          <dd>{size(u.backup_bytes)}</dd>
          <dt>Images</dt>
          <dd>{plural(u.images, "image")} in the registry</dd>
          <dt>Bandwidth</dt>
          <dd>{size(u.bandwidth_bytes)} in 30 days</dd>
        </dl>
        <div className="flex flex-col gap-1.5">
          <h3 className="text-[13px] font-semibold">Operate</h3>
          <Button onClick={() => setOpen("rename")}>Edit settings</Button>
          <Button onClick={() => setOpen("members")}>
            Members and invites
          </Button>
          <Button onClick={() => setOpen("join")}>Join as operator</Button>
          <Button onClick={() => setOpen("stop")} disabled={o.services === 0}>
            Stop all services
          </Button>
          <Button onClick={() => setOpen("suspend")}>
            {o.status === "suspended"
              ? "Unsuspend"
              : "Suspend: block deploys and changes"}
          </Button>
          <Button onClick={() => setOpen("delete")}>Delete organization</Button>
          <p className="text-xs text-faint">
            Its variables, logs, data and code stay hidden. Joining makes you an
            admin until it runs out and shows in its activity log.
          </p>
        </div>
      </div>
      {open === "rename" && <Rename org={o} onClose={close} />}
      {open === "members" && <Members org={o} onClose={close} />}
      {open === "join" && <Join org={o} onClose={close} />}
      {open === "stop" && <Stop org={o} onClose={close} />}
      {open === "suspend" && <Suspend org={o} onClose={close} />}
      {open === "delete" && <Delete org={o} onClose={close} />}
    </aside>
  );
}

type DialogProps = { org: ConsoleOrg; onClose: () => void };

/**
 * A new org with nobody in it: a slug and an optional name. Its people
 * come in through invites, opened once it exists.
 */
function CreateOrg({
  taken,
  onCreated,
  onClose,
}: {
  taken: readonly string[];
  onCreated: (slug: string) => void;
  onClose: () => void;
}) {
  const create = useCreateConsoleOrg();
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");
  const v = useValidation();
  const problem = nameProblem("org", slug, taken);
  const slugError = problem ?? v.required(!slug, "Enter a slug, like acme.");
  return (
    <Dialog title="New organization" onClose={onClose}>
      <form
        ref={v.form}
        aria-label="New organization"
        onSubmit={(e) => {
          e.preventDefault();
          if (!v.check(!!slug && !problem)) return;
          create.mutate(
            { slug, ...(name.trim() && { name: name.trim() }) },
            { onSuccess: (o) => onCreated(o.slug) },
          );
        }}
      >
        <Fields>
          <Field
            label="Slug"
            error={slugError}
            note={`Its address: ${window.location.host}/${slug || "acme"}`}
          >
            <input
              ref={autofocus}
              value={slug}
              onChange={(e) => setSlug(toName(e.target.value))}
              placeholder="acme"
              spellCheck={false}
              autoComplete="off"
              aria-invalid={!!slugError}
              className={`${input} ${invalid(slugError)}`}
            />
          </Field>
          <Field label="Name" note="Optional; the slug if empty.">
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Acme"
              autoComplete="off"
              className={input}
            />
          </Field>
        </Fields>
        <p className="mt-3 text-xs text-mut">
          Nobody is in it yet, not even you. Invite its admin next.
        </p>
        <Footer
          submit="Create organization"
          pending={create.isPending}
          problem={v.problem([!slug && "a slug"], !!problem)}
          error={create.error}
          cli={[]}
          onClose={onClose}
        />
      </form>
    </Dialog>
  );
}

/**
 * An org's name and slug (ADR 0064). A new slug asks first: the org's
 * pages move, and its old address keeps working.
 */
function Rename({ org, onClose }: DialogProps) {
  const rename = useRenameConsoleOrg();
  const navigate = useNavigate();
  const [name, setName] = useState(org.name);
  const [slug, setSlug] = useState(org.slug);
  const [asking, setAsking] = useState(false);
  const want = { name: name.trim(), slug: slug.trim() };
  const moved = want.slug !== org.slug;
  const problem = moved ? nameProblem("org", want.slug) : undefined;
  const changed = moved || (want.name !== org.name && want.name !== "");
  const save = () =>
    rename.mutate(
      {
        org: org.slug,
        name: want.name !== org.name ? want.name : undefined,
        slug: moved ? want.slug : undefined,
      },
      {
        onSuccess: (o) => {
          // The operator's own org too, so "Back to projects" finds it.
          if (currentOrg() === org.slug) setOrg(o.slug);
          if (moved)
            navigate({
              to: "/console/orgs",
              search: { org: o.slug },
              replace: true,
            });
          onClose();
        },
      },
    );
  return (
    <Dialog title={`Edit ${org.slug}`} onClose={onClose}>
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!changed || problem) return;
          if (moved) setAsking(true);
          else save();
        }}
      >
        <label className="flex flex-col gap-1.5 text-xs text-mut">
          Name
          <input
            className="field"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <label className="flex flex-col gap-1.5 text-xs text-mut">
          Slug
          <input
            className={`field font-mono ${problem ? "border-fail" : ""}`}
            value={slug}
            maxLength={32}
            spellCheck={false}
            autoComplete="off"
            aria-invalid={!!problem}
            onChange={(e) => setSlug(toName(e.target.value))}
          />
        </label>
        <p className={`text-xs ${problem ? "text-fail" : "text-faint"}`}>
          {problem ??
            (moved
              ? `Its addresses move to /${want.slug}/... Old links to /${org.slug}/ keep working.`
              : "Its address. Old links keep working after a change.")}
        </p>
        <DialogFoot note={<ErrorText error={rename.error} />}>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            type="submit"
            primary
            disabled={!changed || !!problem || rename.isPending}
          >
            Save
          </Button>
        </DialogFoot>
      </form>
      {asking && (
        <Confirm
          action={`Move ${org.slug} to /${want.slug}`}
          pending={rename.isPending}
          error={rename.error}
          onConfirm={save}
          onCancel={() => setAsking(false)}
        >
          Its pages move from <b>/{org.slug}/...</b> to <b>/{want.slug}/...</b>,
          and the CLI takes <b>--org {want.slug}</b>. Links, bookmarks, scripts
          and CLI contexts that use <b>{org.slug}</b> keep working until another
          organization takes that slug. Its admins see the change in their
          activity log.
        </Confirm>
      )}
    </Dialog>
  );
}

const roles: Role[] = ["read", "deploy", "admin"];

/**
 * An org's people: members with their roles, open invites, and a row to
 * invite someone by email or add an existing account. The first invite
 * of an org without an admin is for an admin.
 */
function Members({ org, onClose }: DialogProps) {
  const members = useConsoleMembers(org.slug);
  const invites = useConsoleInvites(org.slug);
  const setRole = useSetConsoleMemberRole();
  const remove = useRemoveConsoleMember();
  const revoke = useRevokeConsoleInvite();
  const add = useAddConsoleMember();
  const invite = useCreateConsoleInvite();
  const base = useInviteBase(true);
  const [email, setEmail] = useState("");
  const [role, setNewRole] = useState<Role>(
    org.admins?.length ? "read" : "admin",
  );
  const [link, setLink] = useState<InviteLinkInfo>();
  const typed = email.trim();
  const valid = /^[^\s@]+@[^\s@]+$/.test(typed);
  const empty = members.data?.length === 0 && invites.data?.length === 0;
  return (
    <Dialog title={`People of ${org.slug}`} onClose={onClose} wide>
      <div className="flex flex-col gap-3">
        {link && (
          <InviteLink
            link={link}
            url={`${base.base}/invite/${link.code}`}
            note={base.local && <LocalLinkNote operator />}
            again="revoke it and invite them again for another."
            onDone={() => setLink(undefined)}
          />
        )}
        {empty ? (
          <p className="text-[13px] text-mut">
            Nobody yet. Invite its first admin below.
          </p>
        ) : (
          <table>
            <tbody>
              {members.data?.map((m) => (
                <tr key={m.id}>
                  <td className="py-1.5">
                    {m.name || m.email}
                    <span className="block text-xs text-mut">
                      {m.email}
                      {m.expires_at && (
                        <> · operator, until {day(m.expires_at)}</>
                      )}
                    </span>
                  </td>
                  <td className="py-1.5">
                    <select
                      aria-label={`Role of ${m.email}`}
                      className="field min-h-[24px] py-0.5 text-xs"
                      value={m.role}
                      disabled={setRole.isPending}
                      onChange={(e) =>
                        setRole.mutate({
                          org: org.slug,
                          user: m.id,
                          role: e.target.value as Role,
                        })
                      }
                    >
                      {roles.map((r) => (
                        <option key={r} value={r}>
                          {r}
                        </option>
                      ))}
                    </select>
                  </td>
                  <td className="py-1.5 text-right">
                    <Button
                      small
                      disabled={remove.isPending}
                      onClick={() =>
                        remove.mutate({ org: org.slug, user: m.id })
                      }
                    >
                      Remove
                    </Button>
                  </td>
                </tr>
              ))}
              {invites.data?.map((i) => (
                <tr key={`invite-${i.id}`}>
                  <td className="py-1.5 text-mut">
                    {i.email}
                    <span className="block text-xs">
                      {i.expired ? (
                        <StateText state={{ tone: "mut", label: "expired" }} />
                      ) : (
                        <>
                          <StateText
                            state={{ tone: "warn", label: "invited" }}
                          />{" "}
                          until {day(i.expires_at)}
                        </>
                      )}
                    </span>
                  </td>
                  <td className="py-1.5 text-xs text-mut">{i.role}</td>
                  <td className="py-1.5 text-right">
                    <Button
                      small
                      disabled={revoke.isPending}
                      onClick={() => revoke.mutate({ org: org.slug, id: i.id })}
                    >
                      Revoke
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <ErrorText
          error={
            members.error ??
            invites.error ??
            setRole.error ??
            remove.error ??
            revoke.error
          }
        />
        {base.local && !link && <LocalLinkNote operator />}
        <form
          className="flex flex-wrap items-end gap-2 border-t border-line pt-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!valid) return;
            invite.mutate(
              { org: org.slug, email: typed, role },
              {
                onSuccess: (c) => {
                  setEmail("");
                  setLink({ email: c.invite.email, code: c.code });
                },
              },
            );
          }}
        >
          <label className="flex flex-1 flex-col gap-1.5 text-xs text-mut">
            Email
            <input
              className="field"
              type="email"
              placeholder="name@company.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </label>
          <select
            aria-label="Role"
            className="field"
            value={role}
            onChange={(e) => setNewRole(e.target.value as Role)}
          >
            {roles.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
          <Button type="submit" primary disabled={!valid || invite.isPending}>
            Create invite link
          </Button>
          <Button
            disabled={!valid || add.isPending}
            onClick={() =>
              add.mutate(
                { org: org.slug, email: typed, role },
                { onSuccess: () => setEmail("") },
              )
            }
          >
            Add existing user
          </Button>
          {!link && (
            <p className="basis-full text-xs text-faint">
              Dock doesn't send email: an invite gives you a link to send them.
              It works once, for 7 days, for that address. An existing user is
              added at once.
            </p>
          )}
        </form>
        <DialogFoot note={<ErrorText error={invite.error ?? add.error} />}>
          <Button onClick={onClose}>Done</Button>
        </DialogFoot>
      </div>
    </Dialog>
  );
}

const joinHours = [1, 4, 24];

function Join({ org, onClose }: DialogProps) {
  const join = useJoinConsoleOrg();
  const me = useSession();
  const [hours, setHours] = useState(1);
  return (
    <Dialog title={`Join ${org.slug} as operator?`} onClose={onClose}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          join.mutate({ org: org.slug, hours });
        }}
      >
        {join.data ? (
          <p className="text-[13px] text-sec">
            You are an admin of {org.slug} until{" "}
            {new Date(join.data.expires_at).toLocaleString()}.{" "}
            {/* No preload: from the console it would read without the org. */}
            <Link to="/$org" params={{ org: org.slug }} preload={false}>
              Open {org.slug}
            </Link>
          </p>
        ) : (
          <div className="flex flex-col gap-2 text-[13px] text-sec">
            <p>
              You become an admin of <b>{org.slug}</b>, so you can see its
              projects, variables, logs and data. Its admins see that you joined
              in their activity log, and the membership ends by itself.
            </p>
            <label className="flex items-center gap-2 text-xs text-mut">
              For
              <select
                className="field"
                value={hours}
                onChange={(e) => setHours(Number(e.target.value))}
              >
                {joinHours.map((h) => (
                  <option key={h} value={h}>
                    {plural(h, "hour")}
                  </option>
                ))}
              </select>
            </label>
          </div>
        )}
        <DialogFoot note={<ErrorText error={join.error} />}>
          <Button onClick={onClose}>{join.data ? "Done" : "Cancel"}</Button>
          {!join.data && (
            <Button
              type="submit"
              primary
              disabled={join.isPending || me?.kind !== "user"}
            >
              Join for {plural(hours, "hour")}
            </Button>
          )}
        </DialogFoot>
      </form>
    </Dialog>
  );
}

function Stop({ org, onClose }: DialogProps) {
  const stop = useStopConsoleOrg();
  return (
    <Confirm
      danger
      action={`Stop ${plural(org.services, "service")}`}
      title={`Stop every service of ${org.slug}?`}
      pending={stop.isPending}
      error={stop.error}
      onConfirm={() => stop.mutate(org.slug, { onSuccess: onClose })}
      onCancel={onClose}
    >
      Its sites go down: their domains show the 503 page. Volumes, variables and
      releases stay, and starting or deploying a service runs it again.
    </Confirm>
  );
}

function Suspend({ org, onClose }: DialogProps) {
  const suspend = useSuspendConsoleOrg();
  const lifting = org.status === "suspended";
  const text: ReactNode = lifting
    ? "Its members can deploy and change things again."
    : "Its apps keep running, but every deploy and change is refused, and its members and tokens may only read, until you unsuspend it.";
  return (
    <Confirm
      danger={!lifting}
      action={lifting ? "Unsuspend" : "Suspend"}
      title={`${lifting ? "Unsuspend" : "Suspend"} ${org.slug}?`}
      pending={suspend.isPending}
      error={suspend.error}
      onConfirm={() =>
        suspend.mutate(
          { org: org.slug, suspend: !lifting },
          { onSuccess: onClose },
        )
      }
      onCancel={onClose}
    >
      {text}
    </Confirm>
  );
}

function Delete({ org, onClose }: DialogProps) {
  const del = useDeleteConsoleOrg();
  return (
    <Confirm
      danger
      typed={org.slug}
      action={`Delete ${org.slug}`}
      title={`Delete ${org.slug}?`}
      pending={del.isPending}
      error={del.error}
      onConfirm={() => del.mutate(org.slug, { onSuccess: onClose })}
      onCancel={onClose}
    >
      {org.projects > 0 ? (
        <>
          Its <b>{plural(org.projects, "project")}</b> are archived: every
          service stops, volumes and history are kept. Its members, invites,
          variables and tokens are removed, and nobody can sign in to it.
        </>
      ) : (
        <>
          Its members, invites, variables and tokens are removed. Its activity
          log stays.
        </>
      )}
    </Confirm>
  );
}
