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
import {
  Act,
  Button,
  ErrorText,
  lockHolds,
  Menu,
  menuItem,
} from "../../ui/kit";
import { isPending, useRequestsIfAdmin } from "../requests/data";

// An environment's lock on the project overview (ADR 0044, mock "Header
// B"): a strip under its header while locked, with who and why, the
// requests waiting there, Open for… and Unlock; a quiet Lock link and a
// one-line form while unlocked.

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

/** What Open for… offers, as minutes and as `--for`. */
const openFor = [
  [15, "15 min", "15m"],
  [60, "1 h", "1h"],
  [240, "4 h", "4h"],
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
  const waiting = useRequestsIfAdmin().filter(
    (r) =>
      r.project === ref.project &&
      r.environment === ref.environment &&
      isPending(r),
  );
  const error = lockNow.error ?? unlock.error;
  const busy = lockNow.isPending || unlock.isPending;
  const env = ref.environment;
  return (
    <div className={strip}>
      {lockHolds(lock) ? (
        <>
          <span>▣ Locked</span>
          <span className="min-w-0 text-mut">
            by {lock.locked_by}, {ago(lock.locked_at)}
            {lock.reason && <> · {lock.reason}</>}
          </span>
        </>
      ) : (
        <span>
          ▣ Open until {clock(lock.open_until ?? "")}, then locked again
        </span>
      )}
      <span className="ml-auto flex flex-wrap items-center gap-2">
        {waiting[0] && (
          <Link
            to="/$org/requests"
            params={{ org }}
            search={{ id: waiting[0].id }}
            className="hover:underline"
          >
            {plural(waiting.length, "request")} waiting
          </Link>
        )}
        {admin &&
          (lockHolds(lock) ? (
            <>
              <Menu
                label={`Open ${env} for a while`}
                right
                button={<span className={`btn sm ${warnBtn}`}>Open for…</span>}
              >
                {(close) =>
                  openFor.map(([minutes, label, flag]) => (
                    <button
                      key={minutes}
                      type="button"
                      className={menuItem}
                      title={`dock environment unlock ${env} --for ${flag}`}
                      disabled={busy}
                      onClick={() => {
                        close();
                        unlock.mutate({ ref, minutes });
                      }}
                    >
                      {label}
                    </button>
                  ))
                }
              </Menu>
              <Act
                small
                className={warnBtn}
                cli={`dock environment unlock ${env}`}
                disabled={busy}
                onClick={() => unlock.mutate({ ref, minutes: 0 })}
              >
                Unlock
              </Act>
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
      <span>▣ Lock {ref.environment}</span>
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
        Tokens can read; every change they try waits for approval.
      </span>
      <ErrorText error={lock.error} />
    </form>
  );
}
