import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
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
import { ago, clock, day, plural } from "../../ui/format";
import { Icon } from "../../ui/icons";
import { Act, Button, Confirm, ErrorText, lockHolds, Menu } from "../../ui/kit";
import { isPending, useRequestsIfAdmin } from "../requests/data";

// An environment's lock on the project overview (ADR 0044): a pill in the
// environment's header row that opens the lock's details and controls
// (LockPill, mock B), and a one-line form under the header while unlocked
// (LockForm). One description of what a lock does everywhere: a lock is
// for agents, whose changes go to the Inbox for approval (`lockEffect`); a
// lock for people too is a full freeze (`lockPeopleEffect`).

/** What a lock does, in the words every lock control uses. */
export const lockEffect =
  "agents' changes (any token: MCP, the CLI) go to the Inbox for an admin's approval";

/** What a lock for people too does (ADR 0080, protected environments). */
export const lockPeopleEffect =
  "only its approvers change it; everyone else's changes and GitHub pushes go to the Inbox for them";

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
    async ({
      ref,
      reason,
      people,
    }: {
      ref: EnvRef;
      reason: string;
      people?: boolean;
    }) =>
      patchEnvironment(
        queryClient,
        await body(
          api.POST(
            "/api/v1/projects/{project}/environments/{environment}/lock",
            { params: { path: envPath(ref) }, body: { reason, people } },
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

/** What Pause for offers, as minutes and as `--for`. */
const openFor = [
  [15, "15 min", "15m"],
  [60, "1 hour", "1h"],
  [240, "4 hours", "4h"],
] as const;

const strip =
  "mb-2.5 flex flex-wrap items-center gap-x-2.5 gap-y-1.5 rounded-ctl border border-warn-edge bg-stage-bg px-3 py-1.5 text-xs text-warn-text";
const warnBtn = "border-warn-edge text-warn-text";

/**
 * A locked environment's pill in the header (mock B): the lock's state,
 * and for admins how many agent changes wait. Clicking it opens who
 * locked it, when and why, where the held changes go, Review, Pause for,
 * Unlock (or Lock now while paused) and the lock in Activity.
 */
export function LockPill({
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
  const [asking, setAsking] = useState(false);
  const error = lockNow.error ?? (asking ? null : unlock.error);
  const busy = lockNow.isPending || unlock.isPending;
  const env = ref.environment;
  const holds = lockHolds(lock);
  const state = holds
    ? lock.people
      ? "Locked for people too"
      : "Locked for agents"
    : `Lock paused until ${clock(lock.open_until ?? "")}`;
  const inbox = admin ? (
    <Link
      to="/$org/inbox"
      params={{ org }}
      className="text-fg underline underline-offset-3"
    >
      the Inbox
    </Link>
  ) : (
    "the Inbox"
  );
  return (
    <>
      <Menu
        label={`${env}: ${state.toLowerCase()}`}
        right
        dialog
        popupClassName="w-[22rem] max-w-[calc(100vw-1rem)] p-3"
        button={
          <span
            className={`inline-flex items-center gap-1.5 rounded-tag border px-2 py-0.5 text-xs hover:border-warn ${holds ? "border-warn-edge bg-stage-bg text-warn-text" : "border-edge text-mut"}`}
          >
            <Icon name="lock" size={12} />
            {state}
            {waiting.length > 0 && (
              <>
                <span aria-hidden className="w-px self-stretch bg-warn-edge" />
                <span className="text-fg">{waiting.length} waiting</span>
              </>
            )}
          </span>
        }
      >
        {(close) => (
          <div className="flex flex-col gap-2.5 text-xs">
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-sec [&_dt]:text-mut">
              {lock.reason && (
                <>
                  <dt>Why</dt>
                  <dd className="min-w-0 break-words">{lock.reason}</dd>
                </>
              )}
              <dt>By</dt>
              <dd className="min-w-0 truncate">{lock.locked_by}</dd>
              <dt>Since</dt>
              <dd>
                {day(lock.locked_at)} ({ago(lock.locked_at)})
              </dd>
            </dl>
            <p className="border-t border-line pt-2.5 text-sec">
              {!holds ? (
                <>
                  Paused: tokens change {env} without approval until{" "}
                  {clock(lock.open_until ?? "")}, then it locks again.
                </>
              ) : lock.people ? (
                <>
                  Only its approvers change it. Everyone else's changes and
                  GitHub pushes go to {inbox} for them.
                </>
              ) : (
                <>
                  Agents' changes go to {inbox} for an admin's approval. People
                  and GitHub pushes still change it directly.
                </>
              )}
            </p>
            {admin && (waiting[0] || holds) && (
              <div className="flex flex-col items-start gap-2">
                {waiting[0] && (
                  <Link
                    to="/$org/inbox"
                    params={{ org }}
                    search={{ request: waiting[0].id }}
                    className={`btn sm ${warnBtn}`}
                    onClick={close}
                  >
                    Review {plural(waiting.length, "waiting request")}
                  </Link>
                )}
                {holds && (
                  <span className="flex flex-wrap items-center gap-1.5">
                    <span className="mr-0.5 text-mut">Pause for</span>
                    {openFor.map(([minutes, label, flag]) => (
                      // The CLI as a tooltip, as in a menu: three Act
                      // hints would crowd the row.
                      <Button
                        key={minutes}
                        small
                        title={`dock environment unlock ${env} --for ${flag}`}
                        disabled={busy}
                        onClick={() =>
                          unlock.mutate({ ref, minutes }, { onSuccess: close })
                        }
                      >
                        {label}
                      </Button>
                    ))}
                  </span>
                )}
              </div>
            )}
            <ErrorText error={error} />
            <div className="flex items-center gap-2 border-t border-line pt-2.5">
              {admin &&
                (holds ? (
                  <Act
                    small
                    className={warnBtn}
                    cli={`dock environment unlock ${env}`}
                    disabled={busy}
                    onClick={() => {
                      close();
                      setAsking(true);
                    }}
                  >
                    Unlock
                  </Act>
                ) : (
                  <Act
                    small
                    className={warnBtn}
                    cli={`dock environment lock ${env}`}
                    disabled={busy}
                    onClick={() =>
                      lockNow.mutate(
                        {
                          ref,
                          reason: lock.reason ?? "",
                          people: lock.people,
                        },
                        { onSuccess: close },
                      )
                    }
                  >
                    Lock now
                  </Act>
                ))}
              <Link
                to="/$org/settings/activity"
                params={{ org }}
                search={{
                  project: ref.project,
                  environment: env,
                  action: "environment.lock",
                }}
                className="ml-auto text-mut underline underline-offset-3 hover:text-fg"
                onClick={close}
              >
                Locks in Activity
              </Link>
            </div>
          </div>
        )}
      </Menu>
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
          . Tokens can change {env} without approval until someone locks it
          again. To let them in for a while, use Pause for instead.
        </Confirm>
      )}
    </>
  );
}

/**
 * Asks for an optional reason and locks an unlocked environment. It opens
 * right under the environment's heading and scrolls itself into view, so
 * it is seen next to the Lock button that opened it.
 */
export function LockForm({
  envRef: ref,
  onDone,
}: {
  envRef: EnvRef;
  onDone: () => void;
}) {
  const lock = useLock();
  const [reason, setReason] = useState("");
  // For agents by default, as the CLI: the full freeze is a choice (ADR
  // 0044, amended 2026-11-26).
  const [people, setPeople] = useState(false);
  // Opened by clicking Lock: the reason is the first thing to type, and the
  // whole form (with what locking does) is on screen.
  const form = useRef<HTMLFormElement>(null);
  const reasonField = useRef<HTMLInputElement>(null);
  useEffect(() => {
    reasonField.current?.focus({ preventScroll: true });
    form.current?.scrollIntoView({ block: "nearest" });
  }, []);
  const cli = `dock environment lock ${ref.environment}${reason.trim() ? ` --reason ${JSON.stringify(reason.trim())}` : ""}${people ? " --people" : ""}`;
  return (
    <form
      ref={form}
      className={`${strip} scroll-mb-4`}
      onSubmit={(e) => {
        e.preventDefault();
        lock.mutate(
          { ref, reason: reason.trim(), people },
          { onSuccess: onDone },
        );
      }}
    >
      <span className="inline-flex items-center gap-1.5">
        <Icon name="lock" size={12} /> Lock {ref.environment}
      </span>
      <input
        aria-label="Reason (optional)"
        placeholder="Reason (optional)"
        ref={reasonField}
        value={reason}
        onChange={(e) => setReason(e.target.value)}
        className="field min-w-0 flex-[1_1_220px] py-[3px] text-fg"
      />
      <label className="flex min-h-[24px] items-center gap-1.5 text-fg">
        <input
          type="checkbox"
          className="accent-fg"
          checked={people}
          onChange={(e) => setPeople(e.target.checked)}
        />
        Hold people's changes and GitHub pushes too (full code freeze)
      </label>
      <Act small primary type="submit" cli={cli} disabled={lock.isPending}>
        Lock
      </Act>
      <Button small onClick={onDone}>
        Cancel
      </Button>
      <span className="basis-full text-mut">
        Then{" "}
        {people
          ? `${lockPeopleEffect}, people's and agents' alike`
          : `${lockEffect}; people and GitHub pushes still change it directly`}
        .
      </span>
      <ErrorText error={lock.error} />
    </form>
  );
}
