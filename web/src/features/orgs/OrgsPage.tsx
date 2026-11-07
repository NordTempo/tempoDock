import { Link, useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
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
  type ConsoleOrg,
  type Role,
  useAddConsoleMember,
  useConsoleMembers,
  useConsoleOrgs,
  useDeleteConsoleOrg,
  useJoinConsoleOrg,
  useRemoveConsoleMember,
  useRenameConsoleOrg,
  useSetConsoleMemberRole,
  useStopConsoleOrg,
  useSuspendConsoleOrg,
} from "./data";

// The console's Organizations page (ADR 0062, mock B): every org, biggest
// first, with the numbers that rank them, and the selected one's people,
// use and the operator's actions in a column beside it (under it below
// 1400px). Orgs and use only: an org's alerts and errors are its admins'.

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
      />
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
          {current && <Detail key={current.slug} org={current} />}
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
function Detail({ org: o }: { org: ConsoleOrg }) {
  const [open, setOpen] = useState<Opened | null>(null);
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
          <Button onClick={() => setOpen("members")}>Manage members</Button>
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

function Rename({ org, onClose }: DialogProps) {
  const rename = useRenameConsoleOrg();
  const [name, setName] = useState(org.name);
  return (
    <Dialog title={`Edit ${org.slug}`} onClose={onClose}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          rename.mutate({ org: org.slug, name }, { onSuccess: onClose });
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
        <p className="mt-2 text-xs text-faint">
          Its slug, {org.slug}, is its address and stays.
        </p>
        <DialogFoot note={<ErrorText error={rename.error} />}>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            type="submit"
            primary
            disabled={!name.trim() || name === org.name || rename.isPending}
          >
            Save
          </Button>
        </DialogFoot>
      </form>
    </Dialog>
  );
}

const roles: Role[] = ["read", "deploy", "admin"];

function Members({ org, onClose }: DialogProps) {
  const members = useConsoleMembers(org.slug);
  const setRole = useSetConsoleMemberRole();
  const remove = useRemoveConsoleMember();
  const add = useAddConsoleMember();
  const [email, setEmail] = useState("");
  const [role, setNewRole] = useState<Role>("admin");
  return (
    <Dialog title={`Members of ${org.slug}`} onClose={onClose} wide>
      <div className="flex flex-col gap-3">
        {members.data ? (
          <table>
            <tbody>
              {members.data.map((m) => (
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
            </tbody>
          </table>
        ) : (
          <ErrorText error={members.error} />
        )}
        <ErrorText error={setRole.error ?? remove.error} />
        <form
          className="flex flex-wrap items-end gap-2 border-t border-line pt-3"
          onSubmit={(e) => {
            e.preventDefault();
            add.mutate(
              { org: org.slug, email: email.trim(), role },
              { onSuccess: () => setEmail("") },
            );
          }}
        >
          <label className="flex flex-1 flex-col gap-1.5 text-xs text-mut">
            Add an existing user
            <input
              className="field"
              type="email"
              placeholder="email"
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
          <Button
            type="submit"
            disabled={!email.includes("@") || add.isPending}
          >
            Add
          </Button>
        </form>
        <DialogFoot note={<ErrorText error={add.error} />}>
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
