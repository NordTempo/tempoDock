import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { api, body } from "../../api/client";
import { useAction } from "../../data/mutations";
import { envPath, keys, useIsAdmin } from "../../data/queries";
import type {
  Environment,
  EnvironmentLock,
  EnvRef,
  ProjectOverview,
} from "../../data/types";
import { useOrg } from "../../shell/location";
import { ago, clock, plural } from "../../ui/format";
import { Icon } from "../../ui/icons";
import {
  Act,
  Button,
  Confirm,
  ErrorText,
  lockHolds,
  Menu,
  menuItem,
} from "../../ui/kit";
import { isPending, useRequestsIfAdmin } from "../requests/data";

// An environment's lock on the project overview (ADR 0044, mock "Header
// B"): a strip under its header while locked, with who and why, the
// requests waiting there, Pause lock for… and Unlock; a one-line form
// while unlocked. One description of what a lock does everywhere:
// changes by tokens wait for an admin's approval (`lockEffect`).

/** What a lock does, in the words every lock control uses. */
export const lockEffect = "changes by tokens wait for an admin's approval";

const touches = ({ ref }: { ref: EnvRef }) => [
  keys.overview(ref.project),
  ["audit"],
];

/** Puts the environment a lock call answers with into the overview at once. */
function patchEnvironment(queryClient: QueryClient, environment: Environment) {
  queryClient.setQueryData<ProjectOverview>(
    keys.overview(environment.project),
    (o) =>
      o && {
        ...o,
        environments: o.environments.map((e) =>
          e.environment.id === environment.id ? { ...e, environment } : e,
        ),
      },
  );
  return environment;
}

/** Locks an environment, or closes one opened for a while. */
function useLock() {
  const queryClient = useQueryClient();
  return useAction(
    async ({ ref, reason }: { ref: EnvRef; reason: string }) =>
      patchEnvironment(
        queryClient,
        await body(
          api.POST(
            "/api/v1/projects/{project}/environments/{environment}/lock",
            { params: { path: envPath(ref) }, body: { reason } },
          ),
        ),
      ),
    touches,
  );
}

/** Unlocks an environment (0 minutes) or opens it for that long. */
function useUnlock() {
  const queryClient = useQueryClient();
  return useAction(
    async ({ ref, minutes }: { ref: EnvRef; minutes: number }) =>
      patchEnvironment(
        queryClient,
        await body(
          api.POST(
            "/api/v1/projects/{project}/environments/{environment}/unlock",
            { params: { path: envPath(ref) }, body: { minutes } },
          ),
        ),
      ),
    touches,
  );
}

/** What Pause lock for… offers, as minutes and as `--for`. */
const openFor = [
  [15, "15 minutes", "15m"],
  [60, "1 hour", "1h"],
  [240, "4 hours", "4h"],
] as const;

const strip =
  "mb-2.5 flex flex-wrap items-center gap-x-2.5 gap-y-1.5 rounded-ctl border border-warn-edge bg-stage-bg px-3 py-1.5 text-xs text-warn-text";
const warnBtn = "border-warn-edge text-warn-text";

/** The strip under a locked environment's header. */
export function LockStrip({
  envRef: ref,
  lock,
}: {
  envRef: EnvRef;
  lock: EnvironmentLock;
}) {
  const org = useOrg();
  const admin = useIsAdmin();
  const lockNow = useLock();
  const unlock = useUnlock();
  const pending = useRequestsIfAdmin().filter(isPending);
  const waiting = pending.filter(
    (r) => r.project === ref.project && r.environment === ref.environment,
  );
  // None here yet: still point at the ones waiting elsewhere, so the
  // strip always leads to what the lock is for.
  const elsewhere = pending.filter((r) => !waiting.includes(r));
  const [asking, setAsking] = useState(false);
  const error = lockNow.error ?? (asking ? null : unlock.error);
  const busy = lockNow.isPending || unlock.isPending;
  const env = ref.environment;
  return (
    <div className={strip}>
      {lockHolds(lock) ? (
        <>
          <span className="inline-flex items-center gap-1.5">
            <Icon name="lock" size={12} /> Locked
          </span>
          <span className="min-w-0 text-mut">
            by {lock.locked_by}, {ago(lock.locked_at)}
            {lock.reason && <> · {lock.reason}</>} · {lockEffect}
          </span>
        </>
      ) : (
        <span className="inline-flex items-center gap-1.5">
          <Icon name="lock" size={12} /> Lock paused until{" "}
          {clock(lock.open_until ?? "")}, then locked again
        </span>
      )}
      <span className="ml-auto flex flex-wrap items-center gap-2">
        {waiting[0] && (
          <Link
            to="/$org/requests"
            params={{ org }}
            search={{ id: waiting[0].id }}
            className={`btn sm ${warnBtn}`}
          >
            Review {plural(waiting.length, "waiting request")}
          </Link>
        )}
        {/* Not this lock's: said as elsewhere, so it isn't read as tied
            to this environment. */}
        {!waiting[0] && elsewhere[0] && (
          <span className="flex items-center gap-2">
            <span className="text-mut">
              {elsewhere.length === 1
                ? `None here; 1 request waits in ${elsewhere[0].project}/${elsewhere[0].environment}`
                : `None here; ${elsewhere.length} requests wait elsewhere`}
            </span>
            <Link
              to="/$org/requests"
              params={{ org }}
              search={{ id: elsewhere[0].id }}
              className={`btn sm ${warnBtn}`}
            >
              Review
            </Link>
          </span>
        )}
        {admin &&
          (lockHolds(lock) ? (
            <>
              <Menu
                label={`Pause lock for… (${env})`}
                right
                button={
                  <span className={`btn sm ${warnBtn}`}>Pause lock for…</span>
                }
              >
                {(close) => [
                  <span
                    key="head"
                    role="presentation"
                    className="max-w-60 px-2.5 pt-1 pb-1.5 text-2xs text-mut"
                  >
                    Tokens change {env} without approval for
                  </span>,
                  ...openFor.map(([minutes, label, flag]) => (
                    <button
                      key={minutes}
                      type="button"
                      className={menuItem}
                      role="menuitem"
                      title={`dock environment unlock ${env} --for ${flag}`}
                      disabled={busy}
                      onClick={() => {
                        close();
                        unlock.mutate({ ref, minutes });
                      }}
                    >
                      {label}
                    </button>
                  )),
                ]}
              </Menu>
              <Act
                small
                className={warnBtn}
                cli={`dock environment unlock ${env}`}
                disabled={busy}
                onClick={() => setAsking(true)}
              >
                Unlock
              </Act>
              {asking && (
                <Confirm
                  action={`Unlock ${env}`}
                  cli={`dock environment unlock ${env}`}
                  pending={unlock.isPending}
                  error={unlock.error}
                  onConfirm={() =>
                    unlock.mutate(
                      { ref, minutes: 0 },
                      { onSuccess: () => setAsking(false) },
                    )
                  }
                  onCancel={() => {
                    unlock.reset();
                    setAsking(false);
                  }}
                >
                  {lock.locked_by} locked it {ago(lock.locked_at)}
                  {lock.reason ? (
                    <>
                      {" "}
                      for <b>{lock.reason}</b>
                    </>
                  ) : (
                    ""
                  )}
                  . Tokens can change {env} without approval until someone locks
                  it again. To let them in for a while, use Pause lock for…
                  instead.
                </Confirm>
              )}
            </>
          ) : (
            <Act
              small
              className={warnBtn}
              cli={`dock environment lock ${env}`}
              disabled={busy}
              onClick={() => lockNow.mutate({ ref, reason: lock.reason ?? "" })}
            >
              Lock now
            </Act>
          ))}
      </span>
      <ErrorText error={error} />
    </div>
  );
}

/** Asks for an optional reason and locks an unlocked environment. */
export function LockForm({
  envRef: ref,
  onDone,
}: {
  envRef: EnvRef;
  onDone: () => void;
}) {
  const lock = useLock();
  const [reason, setReason] = useState("");
  const cli = `dock environment lock ${ref.environment}${reason.trim() ? ` --reason ${JSON.stringify(reason.trim())}` : ""}`;
  return (
    <form
      className={strip}
      onSubmit={(e) => {
        e.preventDefault();
        lock.mutate({ ref, reason: reason.trim() }, { onSuccess: onDone });
      }}
    >
      <span className="inline-flex items-center gap-1.5">
        <Icon name="lock" size={12} /> Lock {ref.environment}
      </span>
      <input
        aria-label="Reason (optional)"
        placeholder="Reason (optional)"
        // biome-ignore lint/a11y/noAutofocus: opened by clicking Lock
        autoFocus
        value={reason}
        onChange={(e) => setReason(e.target.value)}
        className="field min-w-0 flex-[1_1_220px] py-[3px] text-fg"
      />
      <Act small primary type="submit" cli={cli} disabled={lock.isPending}>
        Lock
      </Act>
      <Button small onClick={onDone}>
        Cancel
      </Button>
      <span className="text-mut">
        Then {lockEffect}. People signed in here aren't affected.
      </span>
      <ErrorText error={lock.error} />
    </form>
  );
}
