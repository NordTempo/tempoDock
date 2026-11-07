import {
  useQuery,
  useQueryClient,
  useSuspenseQuery,
} from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import { setOrg } from "../../api/client";
import { queries, useIsAdmin, useSession } from "../../data/queries";
import { CliHint } from "../../ui/CliHint";
import { day, plural } from "../../ui/format";
import {
  Act,
  Button,
  Check,
  Confirm,
  ErrorText,
  Page,
  Panel,
} from "../../ui/kit";
import { Saved, useValidation } from "../create/form";
import {
  orgQueries,
  useDeleteOrg,
  useLeaveOrg,
  useRenameOrg,
  useSetLockSettings,
  useSetOrgTwoFactor,
} from "./data";
import { OrgSettingsBar } from "./SettingsBar";

/**
 * The organization itself: its name, address (slug), whether members
 * must sign in with a second factor or single sign-on, locked
 * environments (admins), leaving it, and deleting it while it has no
 * projects.
 */
export function GeneralPage() {
  const org = useSuspenseQuery(orgQueries.org()).data;
  const admin = useIsAdmin();
  const user = useSession()?.kind === "user";
  const rename = useRenameOrg();
  const [name, setName] = useState(org.name);
  const v = useValidation();
  const nameError = v.required(!name.trim(), "Enter a name.");
  return (
    <>
      <OrgSettingsBar />
      <Page>
        <Panel>
          <table>
            <tbody>
              <tr>
                <td className="w-32 py-2.5 text-faint">Name</td>
                <td className="py-1.5">
                  {admin ? (
                    <form
                      ref={v.form}
                      className="flex flex-wrap items-center gap-2"
                      onSubmit={(e) => {
                        e.preventDefault();
                        if (v.check(!!name.trim())) rename.mutate(name.trim());
                      }}
                      onKeyDown={(e) => {
                        if (e.key !== "Escape") return;
                        rename.reset();
                        setName(org.name);
                      }}
                    >
                      <input
                        aria-label="Name"
                        value={name}
                        onChange={(e) => setName(e.target.value)}
                        aria-invalid={!!nameError}
                        className={`field min-w-0 flex-[1_1_220px] py-[5px] ${nameError ? "border-fail" : ""}`}
                      />
                      {/* Always there, so the field reads as one that saves. */}
                      <Act
                        small
                        primary
                        type="submit"
                        cli={`dock org rename ${JSON.stringify(name.trim())}`}
                        disabled={rename.isPending || name.trim() === org.name}
                      >
                        Save
                      </Act>
                      {name.trim() !== org.name && (
                        <Button
                          small
                          onClick={() => {
                            rename.reset();
                            setName(org.name);
                          }}
                        >
                          Cancel
                        </Button>
                      )}
                      {nameError && (
                        <span role="alert" className="text-xs text-fail">
                          {nameError}
                        </span>
                      )}
                      <Saved
                        ok={rename.isSuccess && name.trim() === org.name}
                      />
                      <ErrorText error={rename.error} />
                    </form>
                  ) : (
                    org.name
                  )}
                </td>
              </tr>
              <tr>
                <td className="py-2.5 text-faint">Your role</td>
                <td className="py-2.5">{org.role}</td>
              </tr>
              <tr>
                <td className="py-2.5 text-faint">Address</td>
                <td className="py-2.5 font-mono">
                  {window.location.host}/
                  <b className="font-medium">{org.slug}</b>
                </td>
              </tr>
              <tr>
                <td className="py-2.5 text-faint">Sign-in</td>
                <td className="py-2.5">
                  <RequireTwoFactor on={org.require_two_factor} admin={admin} />
                </td>
              </tr>
              <tr>
                <td className="py-2.5 text-faint">Single sign-on</td>
                <td className="py-2.5">
                  {org.sso_only ? "Required for members" : "Not required"}
                  {admin && (
                    <>
                      {" · "}
                      <Link to="/$org/settings/sso" params={{ org: org.slug }}>
                        manage providers
                      </Link>
                    </>
                  )}
                </td>
              </tr>
              <tr>
                <td className="py-2.5 text-faint">Created</td>
                <td className="py-2.5 text-mut">{day(org.created_at)}</td>
              </tr>
            </tbody>
          </table>
        </Panel>
        {admin && <LockSettings />}
        {(user || admin) && (
          <section
            aria-label="Leave or delete"
            className="flex flex-col border-t border-fail-edge pt-1"
          >
            {user && <Leave name={org.name} admin={admin} />}
            {admin && <Delete name={org.name} slug={org.slug} />}
          </section>
        )}
      </Page>
    </>
  );
}

/** Hours an access request may wait, as the API allows. */
const TTL_MIN = 1;
const TTL_MAX = 720;

/**
 * Locked environments (ADR 0044): whether new production environments
 * start locked, and how long an access request waits for a decision.
 */
function LockSettings() {
  const settings = useSuspenseQuery(orgQueries.lockSettings()).data;
  const save = useSetLockSettings();
  const current = settings.request_ttl_hours ?? 24;
  const [hours, setHours] = useState(String(current));
  const n = Number(hours);
  const valid = Number.isInteger(n) && n >= TTL_MIN && n <= TTL_MAX;
  return (
    <section aria-labelledby="locks" className="flex flex-col gap-2">
      <h2 id="locks" className="text-md font-semibold">
        Locked environments
      </h2>
      <p className="-mt-1 text-xs text-mut">These save as you change them.</p>
      <Panel>
        <table>
          <tbody>
            <tr>
              <td className="w-32 py-2.5 text-faint">New production</td>
              <td className="py-2.5">
                <span className="flex flex-wrap items-center gap-2">
                  <Check
                    label="Lock new production environments"
                    title={`dock org locks --lock-production ${settings.lock_production ? "off" : "on"}`}
                    checked={settings.lock_production}
                    disabled={save.isPending}
                    onChange={(lock_production) =>
                      save.mutate({ lock_production })
                    }
                  />
                  <Saved
                    ok={
                      save.isSuccess &&
                      save.variables?.request_ttl_hours === undefined
                    }
                  />
                </span>
              </td>
            </tr>
            <tr>
              <td className="py-2.5 text-faint">Access requests</td>
              <td className="py-1.5">
                <form
                  className="flex flex-wrap items-center gap-2"
                  onSubmit={(e) => {
                    e.preventDefault();
                    if (!valid) return;
                    save.mutate({
                      lock_production: settings.lock_production,
                      request_ttl_hours: n,
                    });
                  }}
                  onKeyDown={(e) => {
                    if (e.key !== "Escape") return;
                    save.reset();
                    setHours(String(current));
                  }}
                >
                  <label className="flex items-center gap-2">
                    Expire after
                    <input
                      type="number"
                      min={TTL_MIN}
                      max={TTL_MAX}
                      value={hours}
                      onChange={(e) => setHours(e.target.value)}
                      aria-invalid={!valid}
                      className={`field w-20 py-[5px] font-mono ${valid ? "" : "border-fail"}`}
                    />
                    hours
                  </label>
                  {hours !== String(current) && (
                    <Button
                      small
                      primary
                      type="submit"
                      title={`dock org locks --request-ttl ${n}h`}
                      disabled={save.isPending}
                    >
                      Save
                    </Button>
                  )}
                  {hours !== String(current) && (
                    <Button
                      small
                      onClick={() => {
                        save.reset();
                        setHours(String(current));
                      }}
                    >
                      Cancel
                    </Button>
                  )}
                  {!valid && (
                    <span className="text-xs text-fail">
                      {TTL_MIN} to {TTL_MAX} hours
                    </span>
                  )}
                  <Saved
                    ok={
                      save.isSuccess &&
                      save.variables?.request_ttl_hours !== undefined &&
                      hours === String(current)
                    }
                  />
                </form>
              </td>
            </tr>
          </tbody>
        </table>
      </Panel>
      <ErrorText error={save.error} />
    </section>
  );
}

/**
 * Whether members must sign in with a second factor (`dock org two-factor`):
 * a switch for admins, the setting for everyone else.
 */
function RequireTwoFactor({ on, admin }: { on: boolean; admin: boolean }) {
  const set = useSetOrgTwoFactor();
  // Turning it on can lock members out of acting, so that asks first.
  const [asking, setAsking] = useState(false);
  if (!admin) {
    return on ? "Two-factor sign-in required" : "Password alone is enough";
  }
  return (
    <span className="flex flex-col gap-1 whitespace-normal">
      <span className="flex flex-wrap items-center gap-2">
        <CliHint
          cli={`dock org two-factor ${on ? "off" : "on"}`}
          className="act"
        >
          <Check
            label="Require two-factor sign-in"
            checked={on}
            disabled={set.isPending}
            onChange={(require) =>
              require ? setAsking(true) : set.mutate(false)
            }
          />
        </CliHint>
        <Saved ok={set.isSuccess} />
      </span>
      {asking && (
        <Confirm
          action="Require two-factor sign-in"
          cli="dock org two-factor on"
          pending={set.isPending}
          error={set.error}
          onConfirm={() =>
            set.mutate(true, { onSuccess: () => setAsking(false) })
          }
          onCancel={() => setAsking(false)}
        >
          Members who signed in without a second factor can't act in this
          organization until they add one in their account. API tokens keep
          working.
        </Confirm>
      )}
      <span className="text-xs text-mut">
        Members who signed in without a second factor can't act here until they
        add one. API tokens are not affected.
      </span>
      <ErrorText error={set.error} />
    </span>
  );
}

/** Drops everything cached for the org just left, and goes home. */
function useGone() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  return async () => {
    setOrg(undefined);
    queryClient.removeQueries({ predicate: (q) => q.queryKey[0] !== "orgs" });
    await navigate({ to: "/" });
  };
}

/** A row of the danger block: what it does, and its button. */
function DangerRow({
  title,
  note,
  children,
}: {
  title: string;
  note: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-center gap-x-6 gap-y-2 border-b border-soft py-3">
      <div className="flex min-w-0 flex-[1_1_320px] flex-col gap-0.5">
        <span className="font-medium">{title}</span>
        <span className="text-mut">{note}</span>
      </div>
      <div className="flex flex-wrap items-center">{children}</div>
    </div>
  );
}

/**
 * Leaving the organization. An org keeps at least one admin, so its only
 * admin can't leave; the button says so instead of failing.
 */
function Leave({ name, admin }: { name: string; admin: boolean }) {
  const leave = useLeaveOrg();
  const gone = useGone();
  const [asking, setAsking] = useState(false);
  const admins = useQuery({
    ...orgQueries.members(),
    enabled: admin,
    select: (members) => members.filter((m) => m.role === "admin").length,
  }).data;
  const last = admin && admins === 1;
  return (
    <DangerRow
      title={`Leave ${name}`}
      note="You lose access to its projects. An admin can invite you again."
    >
      <Act
        danger
        small
        cli="dock org leave"
        disabled={last}
        reason="You're its only admin. Make someone else admin first."
        onClick={() => setAsking(true)}
      >
        Leave
      </Act>
      {asking && (
        <Confirm
          action={`Leave ${name}`}
          cli="dock org leave"
          pending={leave.isPending}
          error={leave.error}
          onConfirm={() => leave.mutate(undefined, { onSuccess: gone })}
          onCancel={() => setAsking(false)}
        >
          You lose access to its projects right away. An admin can invite you
          again.
        </Confirm>
      )}
    </DangerRow>
  );
}

/**
 * Deleting the organization: only once it has no projects, archived ones
 * included (the API refuses otherwise), and after typing its slug.
 */
function Delete({ name, slug }: { name: string; slug: string }) {
  const remove = useDeleteOrg();
  const gone = useGone();
  const [asking, setAsking] = useState(false);
  const live = useQuery(queries.projects()).data;
  const archived = useQuery(queries.archivedProjects()).data;
  const known = live !== undefined && archived !== undefined;
  const count = (live?.length ?? 0) + (archived?.length ?? 0);
  const reason = !known
    ? "Checking its projects…"
    : count > 0
      ? `It still has ${plural(count, "project")}${archived?.length ? ` (${archived.length} archived)` : ""}.`
      : undefined;
  return (
    <DangerRow
      title={`Delete ${name}`}
      note="Its members, invites, variables and tokens go. Only an organization without projects can be deleted."
    >
      <Act
        danger
        small
        cli="dock org delete"
        disabled={!!reason}
        reason={reason}
        onClick={() => setAsking(true)}
      >
        Delete organization
      </Act>
      {asking && (
        <Confirm
          danger
          action={`Delete ${name}`}
          typed={slug}
          cli="dock org delete"
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() => remove.mutate(undefined, { onSuccess: gone })}
          onCancel={() => setAsking(false)}
        >
          Its members, invites, variables and tokens are deleted. This can't be
          undone. Its activity stays.
        </Confirm>
      )}
    </DangerRow>
  );
}
