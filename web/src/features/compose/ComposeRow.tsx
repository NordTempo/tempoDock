import { lazy, Suspense, useState } from "react";
import type { EnvRef } from "../../data/types";

const UpdateFromCompose = lazy(() => import("../apply/UpdateFromCompose"));

/**
 * The environment's Compose row in its Settings (ADR 0049): update its
 * services from a newer docker-compose.yml. canChange is false for
 * sessions that may only read, which see the row without its button.
 */
export function ComposeRow({
  env,
  canChange,
}: {
  env: EnvRef;
  canChange: boolean;
}) {
  const [updating, setUpdating] = useState(false);
  return (
    <div className="flex flex-wrap items-center gap-2 text-mut">
      <span className="mr-1 min-w-0 flex-1">
        shape this environment's services from a docker-compose.yml
      </span>
      {canChange && (
        <button
          type="button"
          className="btn sm"
          title={`dock import compose -p ${env.project} -e ${env.environment} --stage`}
          onClick={() => setUpdating(true)}
        >
          Update from a file…
        </button>
      )}
      {updating && (
        <Suspense fallback={null}>
          <UpdateFromCompose env={env} onClose={() => setUpdating(false)} />
        </Suspense>
      )}
    </div>
  );
}
