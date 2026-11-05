import { useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { setOrg } from "../../api/client";
import { useIsAdmin, useSession } from "../../data/queries";
import { day } from "../../ui/format";
import {
  Act,
  Button,
  Check,
  Confirm,
  ErrorText,
  Page,
  PageBar,
  Panel,
} from "../../ui/kit";
import {
  orgQueries,
  useDeleteOrg,
  useLeaveOrg,
  useRenameOrg,
  useSetLockSettings,
} from "./data";

/**
 * The organization itself: its name, address (slug), locked environments
 * (admins), leaving it, and deleting it while it has no projects.
 */
export function GeneralPage() {
  const org = useSuspenseQuery(orgQueries.org()).data;
  const admin = useIsAdmin();
  const user = useSession()?.kind === "user";
  const rename = useRenameOrg();
  const [name, setName] = useState(org.name);
  return (
    <>
      <PageBar
        parent={org.name}
        title="General"
        after={
          <span className="text-xs text-faint">your role: {org.role}</span>
        }
      />
      <Page width="max-w-[760px]" gap="gap-5">
        <Panel>
          <table>
            <tbody>
              <tr>
                <td className="w-32 py-2.5 text-faint">Name</td>
                <td className="py-1.5">
                  {admin ? (
                    <form
                      className="flex flex-wrap items-center gap-2"
                      onSubmit={(e) => {
                        e.preventDefault();
                        rename.mutate(name.trim());
                      }}
                    >
                      <input
                        aria-label="Name"
                        value={name}
                        onChange={(e) => setName(e.target.value)}
                        className="field min-w-0 flex-[1_1_220px] py-[5px]"
                      />
                      <Act
                        small
                        type="submit"
                        cli={`dock org rename ${JSON.stringify(name.trim())}`}
                        disabled={
                          !name.trim() ||
                          name.trim() === org.name ||
                          rename.isPending
                        }
                      >
                        Save
                      </Act>
                      <ErrorText error={rename.error} />
                    </form>
                  ) : (
                    org.name
                  )}
                </td>
              </tr>
              <tr>
                <td className="py-2.5 text-faint">Address</td>
                <td className="py-2.5 font-mono">
                  {window.location.host}/
                  <b className="font-medium">{org.slug}</b>{" "}
                  <span className="font-sans text-faint">
                    (the slug can't change yet)
                  </span>
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
        {user && <Leave name={org.name} />}
        {admin && <Delete name={org.name} slug={org.slug} />}
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
      <h2 id="locks" className="text-sm font-semibold">
        Locked environments
      </h2>
      <Panel>
        <table>
          <tbody>
            <tr>
              <td className="w-32 py-2.5 text-faint">New production</td>
              <td className="py-2.5">
                <Check
                  label="Lock new production environments"
                  title={`dock org locks --lock-production ${settings.lock_production ? "off" : "on"}`}
                  checked={settings.lock_production}
                  disabled={save.isPending}
                  onChange={(lock_production) =>
                    save.mutate({ lock_production })
                  }
                />
              </td>
            </tr>
            <tr>
              <td className="py-2.5 text-faint">Access requests</td>
              <td className="py-1.5">
                <form
                  className="flex flex-wrap items-center gap-2"
                  onSubmit={(e) => {
                    e.preventDefault();
                    save.mutate({
                      lock_production: settings.lock_production,
                      request_ttl_hours: n,
                    });
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
                      className="field w-20 py-[5px] font-mono"
                    />
                    hours
                  </label>
                  <Button
                    small
                    type="submit"
                    title={`dock org locks --request-ttl ${n}h`}
                    disabled={!valid || n === current || save.isPending}
                  >
                    Save
                  </Button>
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

function Leave({ name }: { name: string }) {
  const leave = useLeaveOrg();
  const gone = useGone();
  const [asking, setAsking] = useState(false);
  return (
    <section aria-labelledby="leave" className="flex flex-col gap-2">
      <h2 id="leave" className="text-sm font-semibold">
        Leave {name}
      </h2>
      {asking ? (
        <Confirm
          action={`Leave ${name}`}
          cli="dock org leave"
          pending={leave.isPending}
          error={leave.error}
          onConfirm={() => leave.mutate(undefined, { onSuccess: gone })}
          onCancel={() => setAsking(false)}
        >
          Leave <b className="font-semibold">{name}</b>?{" "}
          <span className="text-mut">
            You lose access to its projects. An admin can invite you again.
          </span>
        </Confirm>
      ) : (
        <p className="flex flex-wrap items-center gap-3 text-mut">
          <Act small cli="dock org leave" onClick={() => setAsking(true)}>
            Leave
          </Act>
          The last admin can't leave; make someone else admin first.
        </p>
      )}
    </section>
  );
}

function Delete({ name, slug }: { name: string; slug: string }) {
  const remove = useDeleteOrg();
  const gone = useGone();
  const [asking, setAsking] = useState(false);
  return (
    <section aria-labelledby="delete" className="flex flex-col gap-2">
      <h2 id="delete" className="text-sm font-semibold">
        Delete {name}
      </h2>
      {asking ? (
        <Confirm
          danger
          action={`Delete ${slug}`}
          cli="dock org delete"
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() => remove.mutate(undefined, { onSuccess: gone })}
          onCancel={() => setAsking(false)}
        >
          Delete <b className="font-semibold">{name}</b>?{" "}
          <span className="text-mut">
            Its members, invites, variables and tokens go. Its audit log stays.
          </span>
        </Confirm>
      ) : (
        <p className="flex flex-wrap items-center gap-3 text-mut">
          <Act
            small
            danger
            cli="dock org delete"
            onClick={() => setAsking(true)}
          >
            Delete organization
          </Act>
          Only an organization without projects (archived ones count).
        </p>
      )}
    </section>
  );
}
