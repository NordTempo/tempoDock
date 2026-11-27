import { lazy, Suspense, useState } from "react";
import type { EnvRef } from "../../data/types";
import { ErrorText } from "../../ui/kit";
import { useComposeSource, useSyncCompose, useUnfollowCompose } from "./data";
import { SyncOutcome } from "./SyncOutcome";

const UpdateFromCompose = lazy(() => import("../apply/UpdateFromCompose"));
const FollowCompose = lazy(() => import("./FollowCompose"));

/**
 * The environment's Compose row in its Settings (mock A): update its
 * services from a newer docker-compose.yml once (ADR 0049), or follow
 * one in GitHub (ADR 0108), when the row shows the file, its last sync
 * and what waits for review. canChange is false for sessions that may
 * only read, which see the row without its buttons.
 */
export function ComposeRow({
  env,
  canChange,
}: {
  env: EnvRef;
  canChange: boolean;
}) {
  const source = useComposeSource(env);
  const sync = useSyncCompose(env);
  const unfollow = useUnfollowCompose(env);
  const [open, setOpen] = useState<"update" | "follow" | null>(null);
  const flags = `-p ${env.project} -e ${env.environment}`;
  const update = canChange && (
    <button
      type="button"
      className="btn sm"
      title={`dock import compose ${flags} --stage`}
      onClick={() => setOpen("update")}
    >
      Update from a file…
    </button>
  );
  return (
    <div className="flex flex-col gap-2.5">
      {source ? (
        <>
          <SyncOutcome env={env} source={source} />
          {canChange && (
            <div className="flex flex-wrap items-center gap-2">
              <button
                type="button"
                className="btn sm"
                title={`dock compose sync ${flags}`}
                disabled={sync.isPending}
                onClick={() => sync.mutate()}
              >
                {sync.isPending ? "Syncing…" : "Sync now"}
              </button>
              <button
                type="button"
                className="btn sm"
                title={`dock compose follow ${source.repo} ${flags}`}
                onClick={() => setOpen("follow")}
              >
                Edit
              </button>
              {update}
              <button
                type="button"
                className="btn sm text-fail"
                title={`dock compose unfollow ${flags}`}
                disabled={unfollow.isPending}
                onClick={() => unfollow.mutate()}
              >
                Stop following
              </button>
              <ErrorText error={sync.error ?? unfollow.error} />
            </div>
          )}
        </>
      ) : (
        <div className="flex flex-wrap items-center gap-2 text-mut">
          <span className="mr-1 min-w-0 flex-1">
            shape this environment's services from a docker-compose.yml
          </span>
          {update}
          {canChange && (
            <button
              type="button"
              className="btn sm"
              title={`dock compose follow OWNER/REPO ${flags}`}
              onClick={() => setOpen("follow")}
            >
              Follow a file in GitHub…
            </button>
          )}
        </div>
      )}
      {open === "update" && (
        <Suspense fallback={null}>
          <UpdateFromCompose env={env} onClose={() => setOpen(null)} />
        </Suspense>
      )}
      {open === "follow" && (
        <Suspense fallback={null}>
          <FollowCompose
            env={env}
            source={source}
            onClose={() => setOpen(null)}
          />
        </Suspense>
      )}
    </div>
  );
}
